package migrate_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/database"
	"github.com/gasmod/gas/migrate"
)

// legacyDDL is the tracking table of migrate v0.5.6, as it created it, by
// dialect.
var legacyDDL = map[string]string{
	"postgres": `CREATE TABLE IF NOT EXISTS __gas_migrations
(
    version         TEXT PRIMARY KEY,
    service         TEXT        NOT NULL,
    description     TEXT        NOT NULL DEFAULT '',
    migrate_version TEXT        NOT NULL DEFAULT '',
    module_version  TEXT        NOT NULL DEFAULT '',
    applied_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    dirty           BOOLEAN     NOT NULL DEFAULT FALSE
)`,
	"mysql": `CREATE TABLE IF NOT EXISTS __gas_migrations
(
    version         VARCHAR(255) PRIMARY KEY,
    service         TEXT         NOT NULL,
    description     TEXT         NOT NULL,
    migrate_version TEXT         NOT NULL,
    module_version  TEXT         NOT NULL,
    applied_at      DATETIME     NOT NULL DEFAULT NOW(),
    dirty           BOOLEAN      NOT NULL DEFAULT FALSE
)`,
	"sqlite": `CREATE TABLE IF NOT EXISTS __gas_migrations
(
    version         TEXT PRIMARY KEY,
    service         TEXT    NOT NULL,
    description     TEXT    NOT NULL DEFAULT '',
    migrate_version TEXT    NOT NULL DEFAULT '',
    module_version  TEXT    NOT NULL DEFAULT '',
    applied_at      TEXT    NOT NULL DEFAULT (datetime('now')),
    dirty           INTEGER NOT NULL DEFAULT 0
)`,
}

// legacyTable creates the tracking table of migrate v0.5.6 on db, a
// database of b.
func legacyTable(t *testing.T, b backend, db gas.DatabaseProvider) {
	t.Helper()

	_, err := db.DB().ExecContext(t.Context(), legacyDDL[b.name])
	require.NoError(t, err)
}

// legacyInsert records version as applied by service on db, a database of
// b, with migrate v0.5.6's INSERT.
func legacyInsert(t *testing.T, b backend, db gas.DatabaseProvider, version, service string) {
	t.Helper()

	q := "INSERT INTO __gas_migrations (version, service, description, migrate_version, module_version) " +
		"VALUES (?, ?, ?, ?, ?)"
	if b.name == "postgres" {
		q = "INSERT INTO __gas_migrations (version, service, description, migrate_version, module_version) " +
			"VALUES ($1, $2, $3, $4, $5)"
	}

	_, err := db.DB().ExecContext(t.Context(), q, version, service, "", "v0.5.6", "")
	require.NoError(t, err)
}

// checksumColumns returns how many checksum columns the tracking table
// that db's unqualified name reaches has, on a database of b.
func checksumColumns(t *testing.T, b backend, db gas.DatabaseProvider) int {
	t.Helper()

	var q string

	switch b.name {
	case "sqlite":
		q = "SELECT COUNT(*) FROM pragma_table_info('__gas_migrations') WHERE name = 'checksum'"
	case "postgres":
		q = "SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = current_schema() " +
			"AND table_name = '__gas_migrations' AND column_name = 'checksum'"
	default:
		q = "SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = DATABASE() " +
			"AND table_name = '__gas_migrations' AND column_name = 'checksum'"
	}

	var n int

	require.NoError(t, db.DB().QueryRowContext(t.Context(), q).Scan(&n))

	return n
}

// counted returns the total `count` of log's `checksums recorded` events.
func counted(log *recorder) int {
	total := 0

	for _, e := range log.entries() {
		if e.msg == "checksums recorded" {
			n, _ := e.fields["count"].(int)

			total += n
		}
	}

	return total
}

// The checksum is the SHA-256, in lowercase hex, of the up SQL with CRLF
// read as LF: an Up registered with CRLF line ends matches the row an LF
// Up wrote, while a lone carriage return, or a directive line, changes
// it.
func TestChecksum_Normalization(t *testing.T) {
	t.Parallel()

	const lf = "CREATE TABLE a (id INT);\nCREATE TABLE b (id INT)"

	e := sqliteEnv(t)
	e.svc.Register(app, gas.Migration{Version: "001", Up: "SELECT 1"})
	e.svc.Register(app, gas.Migration{Version: "002", Up: lf})
	require.NoError(t, e.svc.RunPending())

	got := rows(t, e.svc)
	require.Len(t, got, 2)
	assert.Equal(
		t,
		"e004ebd5b5532a4b85984a62f8ad48a81aa3460c1ca07701f386135d72cdecf5",
		got[0].Checksum,
	)
	assert.Equal(t, sum(lf), got[1].Checksum)

	cases := map[string]struct {
		up       string
		mismatch bool
	}{
		"crlf":      {up: strings.ReplaceAll(lf, "\n", "\r\n")},
		"lone cr":   {up: strings.ReplaceAll(lf, "\n", "\r"), mismatch: true},
		"directive": {up: "-- gas:no-split\n" + lf, mismatch: true},
	}

	for name, c := range cases {
		peer, _ := e.peer(t)
		peer.Register(app, gas.Migration{Version: "001", Up: "SELECT 1"})
		peer.Register(app, gas.Migration{Version: "002", Up: c.up})

		err := peer.RunPending()
		if !c.mismatch {
			require.NoError(t, err, name)

			continue
		}

		require.ErrorIs(t, err, migrate.ErrChecksumMismatch, name)
		assert.Contains(t, err.Error(), "registered "+sum(c.up), name)
	}
}

// Every row the service writes carries its migration's checksum, a dirty
// row too, including the row MySQL writes ahead of a migration that
// then fails.
func TestChecksum_Recorded(t *testing.T) {
	t.Parallel()

	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()

			e := newEnv(t, b)

			const broken = "CREATE TABLE b (id INT); SELECT id FROM missing"

			e.svc.Register(app, mig("001", "a"))
			e.svc.Register(app, gas.Migration{Version: "002", Up: broken})
			require.ErrorIs(t, e.svc.RunPending(), migrate.ErrMigrationFailed)

			assert.Equal(t, []migrate.AppliedMigration{
				{
					Version:     "001",
					Service:     "app",
					Description: "create a",
					Checksum:    sum("CREATE TABLE a (id INT)"),
				},
				{Version: "002", Service: "app", Dirty: true, Checksum: sum(broken)},
			}, rows(t, e.svc))
		})
	}
}

// An applied migration whose up SQL was edited fails RunPending with
// ErrChecksumMismatch, naming each such version in version order with
// both checksums, and logging each; nothing is applied, a pending
// migration included. The check runs when nothing is pending too.
//
//nolint:funlen // one scenario, checked in full.
func TestChecksum_Mismatch(t *testing.T) {
	t.Parallel()

	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()

			e := newEnv(t, b)

			e.svc.RegisterSlice(app, []gas.Migration{mig("001", "a"), mig("002", "b")})
			require.NoError(t, e.svc.RunPending())

			const (
				edited1 = "CREATE TABLE a (id INT, x INT)"
				edited2 = "CREATE TABLE b (id INT, x INT)"
			)

			peer, peerLog := e.peer(t)
			peer.RegisterSlice(app, []gas.Migration{
				{Version: "002", Up: edited2},
				{Version: "001", Up: edited1},
				mig("003", "c"),
			})

			err := peer.RunPending()
			require.ErrorIs(t, err, migrate.ErrChecksumMismatch)
			require.EqualError(t, err, fmt.Sprintf(
				"gas/migrate: checksum mismatch: "+
					"001 (owner app) stored %s registered %s, "+
					"002 (owner app) stored %s registered %s",
				sum("CREATE TABLE a (id INT)"), sum(edited1),
				sum("CREATE TABLE b (id INT)"), sum(edited2),
			))
			assert.False(t, e.exists(t, "c"))
			assert.Equal(t, []string{"001", "002"}, versions(rows(t, e.svc)))
			assert.False(t, peerLog.has("migration applied"))

			warns := peerLog.messages("warn")
			require.Len(t, warns, 2)

			for i, w := range []struct{ version, stored, registered string }{
				{"001", sum("CREATE TABLE a (id INT)"), sum(edited1)},
				{"002", sum("CREATE TABLE b (id INT)"), sum(edited2)},
			} {
				assert.Equal(t, "checksum mismatch", warns[i].msg)
				assert.Equal(t, map[string]any{
					"service":    migrate.ServiceName,
					"version":    w.version,
					"owner":      "app",
					"stored":     w.stored,
					"registered": w.registered,
				}, warns[i].fields)
			}

			idle, idleLog := e.peer(t)
			idle.RegisterSlice(app, []gas.Migration{
				{Version: "001", Up: edited1},
				mig("002", "b"),
			})

			err = idle.RunPending()
			require.ErrorIs(t, err, migrate.ErrChecksumMismatch)
			require.EqualError(t, err, fmt.Sprintf(
				"gas/migrate: checksum mismatch: 001 (owner app) stored %s registered %s",
				sum("CREATE TABLE a (id INT)"), sum(edited1),
			))
			assert.False(t, idleLog.has("no pending migrations"))
		})
	}
}

// A dirty row takes precedence over a checksum mismatch.
func TestChecksum_DirtyFirst(t *testing.T) {
	t.Parallel()

	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()

			e := newEnv(t, b)

			e.svc.Register(app, mig("001", "a"))
			require.NoError(t, e.svc.RunPending())
			markDirty(t, e, "002")

			peer, peerLog := e.peer(t)
			peer.Register(app, gas.Migration{Version: "001", Up: "CREATE TABLE a (id INT, x INT)"})

			err := peer.RunPending()
			require.ErrorIs(t, err, migrate.ErrDirty)
			require.EqualError(t, err, "gas/migrate: dirty migrations: 002")
			assert.False(t, peerLog.has("checksum mismatch"))
		})
	}
}

// Down and CheckReady neither check nor record checksums: both succeed
// with a mismatched row present, and leave an empty checksum empty.
func TestChecksum_DownAndCheckReadyIgnore(t *testing.T) {
	t.Parallel()

	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()

			e := newEnv(t, b)

			e.svc.RegisterSlice(app, []gas.Migration{mig("001", "a"), mig("002", "b")})
			require.NoError(t, e.svc.RunPending())
			insertRow(t, e, "000", "app", false)

			peer, peerLog := e.peer(t)
			peer.RegisterSlice(app, []gas.Migration{
				{Version: "000", Up: "SELECT 1"},
				{Version: "001", Up: "CREATE TABLE a (id INT, x INT)", Down: "DROP TABLE a"},
				mig("002", "b"),
			})

			require.NoError(t, peer.CheckReady(t.Context()))
			require.NoError(t, peer.Down(2))
			assert.False(t, e.exists(t, "a"))
			assert.Equal(t, []migrate.AppliedMigration{{Version: "000", Service: "app"}},
				rows(t, peer))
			assert.False(t, peerLog.has("checksums recorded"))
			assert.False(t, peerLog.has("checksum mismatch"))
		})
	}
}

// The development loop for the latest migration: Down(1) reverts it with
// its down SQL and deletes its row, and RunPending applies the edited up
// SQL and records its checksum.
func TestChecksum_DevelopmentLoop(t *testing.T) {
	t.Parallel()

	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()

			e := newEnv(t, b)

			e.svc.RegisterSlice(app, []gas.Migration{mig("001", "a"), mig("002", "b")})
			require.NoError(t, e.svc.RunPending())

			const edited = "CREATE TABLE b (id INT, x INT)"

			peer, _ := e.peer(t)
			peer.RegisterSlice(app, []gas.Migration{
				mig("001", "a"),
				{Version: "002", Up: edited, Down: "DROP TABLE b"},
			})

			require.ErrorIs(t, peer.RunPending(), migrate.ErrChecksumMismatch)
			require.NoError(t, peer.Down(1))
			require.NoError(t, peer.RunPending())

			got := rows(t, peer)
			require.Len(t, got, 2)
			assert.Equal(t, sum(edited), got[1].Checksum)
		})
	}
}

// The repair of last resort: writing the registered checksum the error
// names into the row makes RunPending succeed.
func TestChecksum_HandRepair(t *testing.T) {
	t.Parallel()

	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()

			e := newEnv(t, b)

			e.svc.Register(app, mig("001", "a"))
			require.NoError(t, e.svc.RunPending())

			peer, _ := e.peer(t)
			peer.Register(app, gas.Migration{Version: "001", Up: "CREATE TABLE a (id INT, x INT)"})

			err := peer.RunPending()
			require.ErrorIs(t, err, migrate.ErrChecksumMismatch)

			_, registered, ok := strings.Cut(err.Error(), " registered ")
			require.True(t, ok)

			q := "UPDATE __gas_migrations SET checksum = '" + registered + "' WHERE version = '001'"

			_, execErr := e.db.DB().ExecContext(t.Context(), q)
			require.NoError(t, execErr)
			require.NoError(t, peer.RunPending())
		})
	}
}

// Init adds the checksum column to the tracking table of v0.5, and logs
// it; a second Init finds it and logs nothing. The first RunPending
// records the checksums of the registered rows, logs how many, and then
// verifies them. A row v0.5 inserts afterwards gets an empty checksum,
// which the next RunPending records.
func TestChecksum_UpgradeFromV05(t *testing.T) {
	t.Parallel()

	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()

			dsn := b.fresh(t)
			db := openDB(t, b, dsn)

			legacyTable(t, b, db)
			legacyInsert(t, b, db, "001", "app")
			legacyInsert(t, b, db, "002", "app")
			legacyInsert(t, b, db, "999", "gone")

			svc, log := started(t, db)
			assert.Equal(t, 1, checksumColumns(t, b, db))

			info := log.messages("info")
			require.GreaterOrEqual(t, len(info), 2)
			assert.Equal(t, "checksum column added", info[0].msg)
			assert.Equal(t, map[string]any{"service": migrate.ServiceName}, info[0].fields)

			_, againLog := started(t, openDB(t, b, dsn))
			assert.False(t, againLog.has("checksum column added"))

			for _, r := range rows(t, svc) {
				assert.Empty(t, r.Checksum, r.Version)
			}

			svc.RegisterSlice(app, []gas.Migration{mig("001", "a"), mig("002", "b")})
			require.NoError(t, svc.RunPending())
			assert.Equal(t, 2, counted(log))

			got := rows(t, svc)
			require.Len(t, got, 3)
			assert.Equal(t, sum("CREATE TABLE a (id INT)"), got[0].Checksum)
			assert.Equal(t, sum("CREATE TABLE b (id INT)"), got[1].Checksum)
			assert.Empty(t, got[2].Checksum, "an unregistered row is left alone")

			legacyInsert(t, b, db, "003", "app")
			assert.Empty(t, rows(t, svc)[2].Checksum)

			svc.Register(app, mig("003", "c"))
			require.NoError(t, svc.RunPending())
			assert.Equal(t, 3, counted(log))
			assert.Equal(t, sum("CREATE TABLE c (id INT)"), rows(t, svc)[2].Checksum)
			assert.False(t, tableExists(t, b, db, "c"), "a recorded row is not applied")

			edited, _ := started(t, openDB(t, b, dsn))
			edited.Register(
				app,
				gas.Migration{Version: "001", Up: "CREATE TABLE a (id INT, x INT)"},
			)
			require.ErrorIs(t, edited.RunPending(), migrate.ErrChecksumMismatch)
		})
	}
}

// tableExists reports whether db, a database of b, has the table.
func tableExists(t *testing.T, b backend, db *database.Service, table string) bool {
	t.Helper()

	return (&env{b: b, db: db}).exists(t, table)
}

// Eight instances initialize at once on a v0.5 tracking table: all
// succeed, the column is added once, and only one logs it.
func TestChecksum_UpgradeTogether(t *testing.T) {
	t.Parallel()

	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()

			dsn := b.fresh(t)
			db := openDB(t, b, dsn)
			legacyTable(t, b, db)

			svcs := make([]*migrate.Service, 8)
			logs := make([]*recorder, len(svcs))

			for i := range svcs {
				svcs[i], logs[i] = newService(t, openDB(t, b, dsn))
			}

			var wg sync.WaitGroup

			errs := make([]error, len(svcs))

			for i, svc := range svcs {
				wg.Go(func() { errs[i] = svc.Init(t.Context()) })
			}

			wg.Wait()

			added := 0

			for i, err := range errs {
				require.NoError(t, err, "instance %d", i)

				if logs[i].has("checksum column added") {
					added++
				}
			}

			assert.Equal(t, 1, added)
			assert.Equal(t, 1, checksumColumns(t, b, db))

			for _, svc := range svcs {
				require.NoError(t, svc.Close(t.Context()))
			}
		})
	}
}

// Two SQLite instances, which hold no lock across a run, record the
// checksums of the same rows at once: both succeed, each row is recorded
// once, and both verify it.
func TestChecksum_SQLiteRecordTogether(t *testing.T) {
	t.Parallel()

	dsn := sqliteBackend.fresh(t)
	db := openDB(t, sqliteBackend, dsn)

	legacyTable(t, sqliteBackend, db)

	migs := make([]gas.Migration, 0, 10)

	for i := range 10 {
		v := fmt.Sprintf("%03d", i)
		legacyInsert(t, sqliteBackend, db, v, "app")

		migs = append(migs, mig(v, "t"+v))
	}

	a, aLog := started(t, db)
	b, bLog := started(t, openDB(t, sqliteBackend, dsn))

	a.RegisterSlice(app, migs)
	b.RegisterSlice(app, migs)

	errs := make(chan error, 2)

	go func() { errs <- a.RunPending() }()
	go func() { errs <- b.RunPending() }()

	require.NoError(t, <-errs)
	require.NoError(t, <-errs)
	assert.Equal(t, len(migs), counted(aLog)+counted(bLog))

	for i, r := range rows(t, a) {
		assert.Equal(t, sum(migs[i].Up), r.Checksum, r.Version)
	}

	require.NoError(t, a.RunPending())
	require.NoError(t, b.RunPending())
}

// On SQLite, which holds no lock across a run, a dirty row another
// instance writes after the dirty check, here while the checksums
// recorded are logged, is found when the rows are read again: ErrDirty.
func TestChecksum_SQLiteDirtyAfterRecord(t *testing.T) {
	t.Parallel()

	dsn := sqliteBackend.fresh(t)
	db := openDB(t, sqliteBackend, dsn)

	legacyTable(t, sqliteBackend, db)
	legacyInsert(t, sqliteBackend, db, "001", "app")

	other := &env{b: sqliteBackend, db: openDB(t, sqliteBackend, dsn), dsn: dsn}

	log := &recorder{onSend: func(en entry) {
		if en.msg == "checksums recorded" {
			markDirty(t, other, "002")
		}
	}}

	svc, err := migrate.New()(db, log)
	require.NoError(t, err)
	require.NoError(t, svc.Init(t.Context()))
	t.Cleanup(func() { require.NoError(t, svc.Close(context.Background())) })

	svc.RegisterSlice(app, []gas.Migration{mig("001", "a"), mig("002", "b")})

	err = svc.RunPending()
	require.ErrorIs(t, err, migrate.ErrDirty)
	require.EqualError(t, err, "gas/migrate: dirty migrations: 002")
	assert.False(t, tableExists(t, sqliteBackend, db, "b"))
}

// On SQLite, an instance whose checksum UPDATE changes no row, because
// another instance recorded a checksum first, still reads the rows again
// and verifies what the other wrote: here a different checksum, so
// ErrChecksumMismatch.
func TestChecksum_SQLiteRereadAfterNoRow(t *testing.T) {
	t.Parallel()

	f := &faults{}
	db := faultDB(t, sqliteBackend, f)

	legacyTable(t, sqliteBackend, db)
	legacyInsert(t, sqliteBackend, db, "001", "app")

	svc, _ := started(t, db)
	svc.Register(app, mig("001", "a"))

	other := sum("SELECT 2")

	f.before("exec", "BEGIN IMMEDIATE", func() {
		_, err := db.DB().ExecContext(context.Background(),
			"UPDATE __gas_migrations SET checksum = ? WHERE version = '001'", other)
		assert.NoError(t, err)
	})

	err := svc.RunPending()
	require.ErrorIs(t, err, migrate.ErrChecksumMismatch)
	assert.Contains(t, err.Error(), "001 (owner app) stored "+other)
}

// On MySQL the column check reads the current database only: a tracking
// table with the column in another database of the server does not stop
// Init adding it here.
func TestChecksum_MySQLOtherDatabase(t *testing.T) {
	t.Parallel()

	upgraded := newEnv(t, mysqlBackend)
	require.Equal(t, 1, checksumColumns(t, mysqlBackend, upgraded.db))

	db := openDB(t, mysqlBackend, mysqlDSN(t))
	legacyTable(t, mysqlBackend, db)

	_, log := started(t, db)
	assert.True(t, log.has("checksum column added"))
	assert.Equal(t, 1, checksumColumns(t, mysqlBackend, db))
}

// On PostgreSQL the column check reads the schema the unqualified table
// name resolves to: with search_path set to a schema holding a v0.5
// table, while public holds an upgraded one, Init upgrades the first.
func TestChecksum_PostgresSearchPath(t *testing.T) {
	t.Parallel()

	e := newEnv(t, postgresBackend)

	_, err := e.db.DB().ExecContext(t.Context(), "CREATE SCHEMA legacy")
	require.NoError(t, err)

	_, err = e.db.DB().ExecContext(
		t.Context(),
		strings.Replace(legacyDDL["postgres"], "__gas_migrations", "legacy.__gas_migrations", 1),
	)
	require.NoError(t, err)

	db := openDB(t, postgresBackend, e.dsn+"&search_path=legacy")

	var schema string

	require.NoError(
		t,
		db.DB().QueryRowContext(t.Context(), "SELECT current_schema()").Scan(&schema),
	)
	require.Equal(t, "legacy", schema)

	_, log := started(t, db)
	assert.True(t, log.has("checksum column added"))
	assert.Equal(t, 1, checksumColumns(t, postgresBackend, db))
}

// On PostgreSQL and MySQL the ALTER that adds the column waits for the
// table's lock no longer than the lock timeout, or 5 seconds when that is
// shorter: with another session's transaction open on the table, Init
// fails within the bound with the driver's error, which is not
// ErrLockTimeout. Either way, the connection Init used is not returned to
// the pool, so the bound it set does not reach the app.
//
//nolint:funlen // one case per dialect, failure and success.
func TestChecksum_AlterLockWait(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		setting, code string
	}{
		"postgres": {setting: "SHOW lock_timeout", code: "SQLSTATE 55P03"},
		"mysql":    {setting: "SELECT CAST(@@lock_wait_timeout AS CHAR)", code: "Error 1205"},
	}

	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()

			c, ok := cases[b.name]
			if !ok {
				t.Skip("SQLite takes the write lock in BEGIN IMMEDIATE")
			}

			dsn := b.fresh(t)
			db := openDB(t, b, dsn)
			db.DB().SetMaxOpenConns(1)

			var want string

			require.NoError(t, db.DB().QueryRowContext(t.Context(), c.setting).Scan(&want))

			legacyTable(t, b, db)

			holder, err := openDB(t, b, dsn).DB().BeginTx(t.Context(), nil)
			require.NoError(t, err)

			var n int

			require.NoError(t, holder.QueryRowContext(t.Context(),
				"SELECT COUNT(*) FROM __gas_migrations").Scan(&n))

			svc, log := newService(t, db, migrate.WithLockTimeout(time.Second))

			start := time.Now()

			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()

			err = svc.Init(ctx)

			require.Error(t, err)
			require.NotErrorIs(t, err, migrate.ErrLockTimeout)
			assert.True(t, strings.HasPrefix(err.Error(), "gas/migrate: add checksum column: "),
				err.Error())
			assert.Contains(t, err.Error(), c.code)
			assert.Less(t, time.Since(start), 5*time.Second)
			assert.False(t, log.has("checksum column added"))

			assertSetting := func() {
				t.Helper()

				var got string

				require.NoError(t, db.DB().QueryRowContext(t.Context(), c.setting).Scan(&got))
				assert.Equal(t, want, got)
				assert.Zero(t, db.DB().Stats().InUse)
			}

			assertSetting()
			require.NoError(t, holder.Rollback())

			require.NoError(t, svc.Init(t.Context()))
			t.Cleanup(func() { require.NoError(t, svc.Close(context.Background())) })
			assert.True(t, log.has("checksum column added"))
			assertSetting()
		})
	}
}

// Each step of adding the column and of recording a checksum that fails
// is named in the error: `add checksum column: ` for the column check,
// the lock-wait bound and the ALTER, and `record checksum <version>
// (owner <owner>): ` for the UPDATE.
func TestChecksum_FaultSteps(t *testing.T) {
	t.Parallel()

	check := map[string]string{
		"sqlite":   "pragma_table_info",
		"postgres": "information_schema.columns",
		"mysql":    "information_schema.columns",
	}
	set := map[string]string{
		"postgres": "set_config('lock_timeout'",
		"mysql":    "SET SESSION lock_wait_timeout",
	}

	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()

			steps := []struct{ kind, match string }{
				{"query", check[b.name]},
				{"exec", "ADD COLUMN checksum"},
			}
			if m, ok := set[b.name]; ok {
				steps = append(steps, struct{ kind, match string }{"exec", m})
			}

			for _, s := range steps {
				f := &faults{}
				db := faultDB(t, b, f)
				legacyTable(t, b, db)

				svc, _ := newService(t, db)

				f.add(s.kind, s.match, errBoom)

				err := svc.Init(t.Context())
				require.ErrorIs(t, err, errBoom, s.match)
				require.EqualError(t, err, "gas/migrate: add checksum column: boom", s.match)
			}

			f := &faults{}
			db := faultDB(t, b, f)
			legacyTable(t, b, db)
			legacyInsert(t, b, db, "001", "app")

			svc, _ := started(t, db)
			svc.Register(app, mig("001", "a"))

			f.add("exec", "SET checksum =", errBoom)

			err := svc.RunPending()
			require.ErrorIs(t, err, errBoom)
			require.EqualError(t, err, "gas/migrate: record checksum 001 (owner app): boom")
		})
	}
}
