package migrate_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/database"
	"github.com/gasmod/gas/migrate"
)

// Readers on the app's pool hold SQLite's shared lock while RunPending
// commits, which SQLite answers with SQLITE_BUSY. Each migration commits
// on its own, and a busy commit is tried again, so every round applies
// every migration, with gas/database's default busy timeout and with the
// busy timeout turned off. When the run was one transaction, a busy
// commit at its end lost every migration in most rounds.
func TestSQLite_ReadersAtCommit(t *testing.T) {
	t.Parallel()

	for _, busy := range []string{"default", "off"} {
		t.Run(busy, func(t *testing.T) {
			t.Parallel()

			for round := range 8 {
				dsn := "file:" + t.TempDir() + "/app.db"
				if busy == "off" {
					dsn += "?_pragma=busy_timeout(0)"
				}

				db := openDB(t, sqliteBackend, dsn)

				_, err := db.DB().ExecContext(t.Context(), "CREATE TABLE app_data (id INTEGER)")
				require.NoError(t, err)

				svc, _ := started(t, db)

				for i := range 20 {
					v := fmt.Sprintf("%03d", i)
					svc.Register(app, mig(v, "t"+v))
				}

				stop := make(chan struct{})

				var wg sync.WaitGroup

				for range 4 {
					wg.Go(func() {
						for {
							select {
							case <-stop:
								return
							default:
							}

							var n int

							// A reader without a busy timeout may meet the
							// commit; only the run's result is checked.
							_ = db.DB().
								QueryRowContext(t.Context(), "SELECT count(*) FROM app_data").
								Scan(&n)
						}
					})
				}

				err = svc.RunPending()

				close(stop)
				wg.Wait()

				require.NoError(t, err, "round %d", round)
				assert.Len(t, rows(t, svc), 20, "round %d", round)
			}
		})
	}
}

// A migration's session settings do not reach the app: on PostgreSQL and
// MySQL the connection a run used is closed, not returned to the pool, so
// a pool limited to one connection gets a fresh one with the server's
// defaults. SQLite returns it, for the reason TestSQLite_InMemory gives.
func TestSession_StateDoesNotLeak(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		up, query, want string
	}{
		"mysql": {
			up:    "SET FOREIGN_KEY_CHECKS = 0; CREATE TABLE a (id INT)",
			query: "SELECT CAST(@@foreign_key_checks AS CHAR)",
			want:  "1",
		},
		"postgres": {
			up:    "SET search_path TO elsewhere, public; CREATE TABLE a (id INT)",
			query: "SHOW search_path",
			want:  `"$user", public`,
		},
	}

	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()

			c, ok := cases[b.name]
			if !ok {
				t.Skip("SQLite returns the connection to the pool")
			}

			e := newEnv(t, b)

			e.db.DB().SetMaxOpenConns(1)
			e.svc.Register(app, gas.Migration{Version: "001", Up: c.up})
			require.NoError(t, e.svc.RunPending())

			var got string

			require.NoError(t, e.db.DB().QueryRowContext(t.Context(), c.query).Scan(&got))
			assert.Equal(t, c.want, got)
			assert.Zero(t, e.db.DB().Stats().InUse)
		})
	}
}

// On SQLite the connection a call used goes back to the pool: closing it
// would lose an in-memory database, which a pool of one connection to
// `:memory:` keeps for as long as that connection lives.
func TestSQLite_InMemory(t *testing.T) {
	t.Parallel()

	cfg := database.DefaultConfig()

	cfg.Database.Dialect = database.DialectSQLite
	cfg.Database.DSN = ":memory:"
	cfg.Database.MaxOpenConns = 1
	cfg.Database.MaxIdleConns = 1

	db, err := database.New(database.WithConfig(cfg))(nil, &gas.NopLogger{})
	require.NoError(t, err)
	require.NoError(t, db.Init(t.Context()))
	t.Cleanup(func() { require.NoError(t, db.Close(context.Background())) })

	svc, _ := started(t, db)

	svc.Register(app, mig("001", "a"))
	require.NoError(t, svc.RunPending())
	require.NoError(t, svc.CheckReady(t.Context()))
	require.NoError(t, svc.Down(1))
	require.ErrorIs(t, svc.CheckReady(t.Context()), migrate.ErrPending)
	require.NoError(t, svc.RunPending())
	assert.Equal(t, []string{"001"}, versions(rows(t, svc)))
}

// errPanic is what the panicking logger panics with.
var errPanic = errors.New("logger panicked")

// A panic inside a run, here from the logger once the migration is
// applied, still releases the lock and the connection: another instance
// takes the lock at once, and the pool has no connection in use.
func TestLock_ReleasedOnPanic(t *testing.T) {
	t.Parallel()

	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()

			dsn := b.fresh(t)
			db := openDB(t, b, dsn)

			log := &recorder{onSend: func(en entry) {
				if en.msg == "migration applied" {
					panic(errPanic)
				}
			}}

			svc, err := migrate.New()(db, log)
			require.NoError(t, err)
			require.NoError(t, svc.Init(t.Context()))

			svc.Register(app, mig("001", "a"))

			func() {
				defer func() { assert.Equal(t, errPanic, recover()) }()

				_ = svc.RunPending()
			}()

			assert.Zero(t, db.DB().Stats().InUse)

			e := &env{b: b, dsn: dsn, db: db}
			assertUnlocked(t, e)

			peer, _ := e.peer(t, migrate.WithLockTimeout(time.Second))
			peer.Register(app, mig("001", "a"))
			require.NoError(t, peer.RunPending())
			assert.Equal(t, []string{"001"}, versions(rows(t, peer)))
		})
	}
}

// MySQL marks a migration dirty before it runs. A run whose context ends
// in the middle of a migration, after its first statement committed,
// leaves the migration dirty, and the next run refuses to go on.
func TestMySQL_CancelLeavesDirty(t *testing.T) {
	t.Parallel()

	e := newEnv(t, mysqlBackend)

	m := gas.Migration{
		Version: "001",
		Up:      "CREATE TABLE a (id INT); SELECT SLEEP(5); CREATE TABLE b (id INT)",
		Down:    "DROP TABLE b; DROP TABLE a",
	}

	e.svc.Register(app, m)

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()

	err := e.svc.RunPendingContext(ctx)
	require.ErrorIs(t, err, migrate.ErrMigrationFailed)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.True(t, e.log.has("migration marked dirty"))

	assert.Equal(
		t,
		[]migrate.AppliedMigration{
			{Version: "001", Service: "app", Dirty: true, Checksum: sum(m.Up)},
		},
		rows(t, e.svc),
	)
	assert.True(t, e.exists(t, "a"))
	assert.False(t, e.exists(t, "b"))

	peer, _ := e.peer(t)
	peer.Register(app, gas.Migration{Version: "001", Up: "SELECT 1"})
	require.ErrorIs(t, peer.RunPending(), migrate.ErrDirty)
	require.ErrorIs(t, peer.CheckReady(t.Context()), migrate.ErrDirty)
}

// mysqlModeDSN returns the DSN of a new MySQL database whose sessions run
// in sqlMode, with multiStatements on when multi is.
func mysqlModeDSN(t *testing.T, sqlMode string, multi bool) string {
	t.Helper()

	cfg, err := mysql.ParseDSN(mysqlDSN(t))
	require.NoError(t, err)

	cfg.Params = map[string]string{"sql_mode": "'" + sqlMode + "'"}
	cfg.MultiStatements = multi

	return cfg.FormatDSN()
}

// The statements migrate sends on MySQL are those the server would find
// in the same text, in the session's SQL mode: a text that holds a DROP
// only in the default mode drops the table there, and leaves it with
// NO_BACKSLASH_ESCAPES or ANSI_QUOTES, through migrate as through the
// server's own parsing with multiStatements.
func TestMySQL_SplitFollowsSQLMode(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name, mode, up string
		dropped        bool
	}{
		{"nbe", "NO_BACKSLASH_ESCAPES", `SELECT '\' # '; DROP TABLE victim; -- '`, false},
		{"nbe default", "", `SELECT '\' # '; DROP TABLE victim; -- '`, true},
		{"ansi", "ANSI_QUOTES", `SELECT 1 AS "\" # "; DROP TABLE victim; -- "`, false},
		{"ansi via ANSI", "ANSI", `SELECT 1 AS "\" # "; DROP TABLE victim; -- "`, false},
		{"ansi default", "", `SELECT 1 AS "\" # "; DROP TABLE victim; -- "`, true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			victim := func(t *testing.T, db *database.Service) bool {
				t.Helper()

				var n int

				require.NoError(t, db.DB().QueryRowContext(t.Context(),
					"SELECT COUNT(*) FROM information_schema.tables "+
						"WHERE table_schema = DATABASE() AND table_name = 'victim'").Scan(&n))

				return n == 1
			}

			server := openDB(t, mysqlBackend, mysqlModeDSN(t, c.mode, true))

			_, err := server.DB().ExecContext(t.Context(), "CREATE TABLE victim (x INT)")
			require.NoError(t, err)

			_, err = server.DB().ExecContext(t.Context(), c.up)
			require.NoError(t, err)
			assert.Equal(t, c.dropped, !victim(t, server), "the server's own parse")

			db := openDB(t, mysqlBackend, mysqlModeDSN(t, c.mode, false))

			_, err = db.DB().ExecContext(t.Context(), "CREATE TABLE victim (x INT)")
			require.NoError(t, err)

			svc, _ := started(t, db)
			svc.Register(app, gas.Migration{Version: "001", Up: c.up})
			require.NoError(t, svc.RunPending())
			assert.Equal(t, c.dropped, !victim(t, db), "migrate's split")
		})
	}
}

// panicProvider is a SQLite provider whose SafeError panics with errPanic
// on an error while armed.
type panicProvider struct {
	*rawProvider

	// armed makes SafeError panic.
	armed atomic.Bool
}

// SafeError panics on an error while armed, and otherwise returns err.
func (p *panicProvider) SafeError(err error) error {
	if err != nil && p.armed.Load() {
		panic(errPanic)
	}

	return err
}

// A panic inside a SQLite migration's transaction, here from SafeError on
// the migration's failure, rolls the transaction back before the
// connection goes back to the pool: the next run on the pool's one
// connection begins its own transaction and applies, and the failed
// migration left nothing, not even a dirty mark.
func TestSQLite_PanicRollsBack(t *testing.T) {
	t.Parallel()

	p := &panicProvider{rawProvider: openRaw(t, "sqlite", sqliteBackend.fresh(t), "sqlite")}
	p.db.SetMaxOpenConns(1)

	svc, _ := started(t, p)
	svc.Register(app, gas.Migration{
		Version: "001",
		Up:      "CREATE TABLE a (id INT); INSERT INTO missing VALUES (1)",
	})

	p.armed.Store(true)

	func() {
		defer func() { assert.Equal(t, errPanic, recover()) }()

		_ = svc.RunPending()
	}()

	p.armed.Store(false)
	assert.Zero(t, p.db.Stats().InUse)

	next, _ := started(t, p, migrate.WithLockTimeout(time.Second))
	next.Register(app, mig("002", "b"))
	require.NoError(t, next.RunPending())
	assert.Equal(t, []string{"002"}, versions(rows(t, next)))

	var n int

	require.NoError(t, p.db.QueryRowContext(t.Context(),
		"SELECT COUNT(*) FROM sqlite_master WHERE name = 'a'").Scan(&n))
	assert.Zero(t, n, "the failed migration's CREATE TABLE was rolled back")
}
