package migrate_test

import (
	"fmt"
	"net"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/database"
	"github.com/gasmod/gas/migrate"
)

// secret is a value in migration SQL that no error may hold.
const secret = "zq-secret-value"

// A failed migration's error holds the driver's text and none of the
// migration's SQL; registration errors hold no SQL at all.
func TestErrors_NoSQL(t *testing.T) {
	t.Parallel()

	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()

			e := newEnv(t, b)

			e.svc.Register(app, gas.Migration{
				Version: "001",
				Up:      "INSERT INTO missing (v) VALUES ('" + secret + "')",
			})

			err := e.svc.RunPending()
			require.ErrorIs(t, err, migrate.ErrMigrationFailed)
			assert.NotContains(t, err.Error(), secret)
			assert.Contains(t, err.Error(), "missing", "the driver's own text stays")
			assert.Equal(t, 1, strings.Count(err.Error(), "gas/migrate: "))

			for _, w := range e.log.entries() {
				assert.NotContains(t, fmt.Sprint(w.fields), secret)
			}
		})
	}

	e := sqliteEnv(t)
	e.svc.Register(app, gas.Migration{Version: "001", Up: "-- gas:" + secret + "\nSELECT 1"})

	err := e.svc.RunPending()
	require.ErrorIs(t, err, migrate.ErrUnknownDirective)
	assert.NotContains(t, err.Error(), secret)
}

// With a provider that has SafeError, every driver error passes through
// it before it is wrapped.
func TestErrors_SafeErrorPath(t *testing.T) {
	t.Parallel()

	p := &safeProvider{rawProvider: openRaw(t, "sqlite", sqliteBackend.fresh(t), "sqlite")}
	svc, _ := started(t, p)

	svc.Register(app, gas.Migration{Version: "001", Up: "INSERT INTO missing VALUES (1)"})

	err := svc.RunPending()
	require.ErrorIs(t, err, errReplaced)
	require.EqualError(
		t,
		err,
		"gas/migrate: apply 001 (owner app): migration failed: replaced by SafeError",
	)

	p.mu.Lock()
	defer p.mu.Unlock()

	assert.Positive(t, p.calls)
}

// Through gas/database, a connection-level failure in a migration is
// replaced by gas/database's own error, which holds no address, while
// the dirty mark still lands.
func TestErrors_DatabaseSafeError(t *testing.T) {
	t.Parallel()

	f := &faults{}
	svc, _ := started(t, faultDB(t, sqliteBackend, f))

	refused := &net.OpError{
		Op:   "dial",
		Net:  "tcp",
		Addr: &net.TCPAddr{IP: net.IPv4(10, 9, 8, 7), Port: 5432},
		Err:  os.NewSyscallError("connect", syscall.ECONNREFUSED),
	}

	svc.Register(app, gas.Migration{Version: "001", Up: "CREATE TABLE a (id INT)"})
	f.add("exec", "CREATE TABLE a", refused)

	err := svc.RunPending()
	require.ErrorIs(t, err, database.ErrConnect)
	require.ErrorIs(t, err, migrate.ErrMigrationFailed)
	require.NotErrorIs(t, err, refused)
	require.EqualError(
		t,
		err,
		"gas/migrate: apply 001 (owner app): migration failed: gas/database: connection: connection refused",
	)
	assert.NotContains(t, err.Error(), "10.9.8.7")
	assert.True(t, rows(t, svc)[0].Dirty)
}

// The tracking table's read failures are wrapped after `read tracking
// table: `, for every caller.
func TestErrors_ReadTable(t *testing.T) {
	t.Parallel()

	f := &faults{}
	svc, _ := started(t, faultDB(t, sqliteBackend, f))

	f.add("query", "SELECT version, service", errBoom)

	_, appliedErr := svc.Applied(t.Context())

	for _, err := range []error{
		svc.RunPending(),
		svc.Down(1),
		svc.CheckReady(t.Context()),
		appliedErr,
	} {
		require.ErrorIs(t, err, errBoom)
		require.EqualError(t, err, "gas/migrate: read tracking table: boom")
	}

	f.clear()

	e := sqliteEnv(t)
	_, err := e.db.DB().ExecContext(t.Context(), "INSERT INTO __gas_migrations "+
		"(version, service, description, migrate_version, module_version, dirty) "+
		"VALUES ('001', 'app', '', '', '', 'maybe')")
	require.NoError(t, err)

	_, err = e.svc.Applied(t.Context())
	require.Error(t, err)
	assert.True(
		t,
		strings.HasPrefix(err.Error(), "gas/migrate: read tracking table: sql: Scan error"),
	)
}

// Each step of applying a migration that fails is named in its error,
// and the failure is rolled back and marked dirty when the connection
// can still say so.
//
//nolint:funlen // one case per step and dialect.
func TestErrors_ApplySteps(t *testing.T) {
	t.Parallel()

	type step struct {
		kind, match string
		up          string
		want        string
		dirty       bool
	}

	perDialect := map[string][]step{
		"sqlite": {
			{
				kind: "exec", match: "COMMIT", up: "SELECT 1",
				want: "commit: boom; mark dirty: commit: boom",
			},
			{kind: "exec", match: "'', 0)", up: "SELECT 1", want: "record: boom", dirty: true},
			{
				kind: "exec", match: "ROLLBACK", up: "SELECT * FROM missing",
				want: "SQL logic error: no such table: missing (1)\nroll back: boom",
			},
		},
		"postgres": {
			{kind: "begin", up: "SELECT 1", want: "begin: boom", dirty: true},
			{kind: "commit", up: "SELECT 1", want: "commit: boom", dirty: true},
			{
				kind:  "rollback",
				up:    "SELECT * FROM missing",
				want:  "ERROR: relation \"missing\" does not exist (SQLSTATE 42P01)\nroll back: boom",
				dirty: true,
			},
		},
		"mysql": {
			{kind: "begin", up: "SELECT 1", want: "begin: boom", dirty: true},
			{kind: "commit", up: "SELECT 1", want: "commit: boom", dirty: true},
			{
				kind: "rollback", up: "SELECT * FROM missing",
				want:  "Error 1146 (42S02): Table '%s.missing' doesn't exist\nroll back: boom",
				dirty: true,
			},
		},
	}

	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()

			for _, s := range perDialect[b.name] {
				f := &faults{}
				db := faultDB(t, b, f)
				svc, _ := started(t, db)

				svc.Register(app, gas.Migration{Version: "001", Up: s.up})
				f.add(s.kind, s.match, errBoom)

				err := svc.RunPending()

				f.clear()

				want := s.want
				if strings.Contains(want, "%s") {
					var name string

					require.NoError(
						t,
						db.DB().QueryRowContext(t.Context(), "SELECT DATABASE()").Scan(&name),
					)

					want = fmt.Sprintf(want, name)
				}

				require.ErrorIs(t, err, migrate.ErrMigrationFailed, s.match)
				require.EqualError(
					t,
					err,
					"gas/migrate: apply 001 (owner app): migration failed: "+want,
				)

				got := rows(t, svc)
				if s.dirty {
					require.Len(t, got, 1, want)
					assert.True(t, got[0].Dirty)
				} else {
					assert.Empty(t, got, want)
				}
			}
		})
	}
}

// On SQLite each migration is its own transaction, begun with BEGIN
// IMMEDIATE, which takes the write lock: a failure to begin it, or to read
// the table again inside it, fails the call without marking anything
// dirty, and a failed commit loses only that migration, so those before
// it stay applied.
func TestErrors_SQLiteWrites(t *testing.T) {
	t.Parallel()

	f := &faults{}
	svc, log := started(t, faultDB(t, sqliteBackend, f))

	svc.Register(app, mig("001", "a"))
	require.NoError(t, svc.RunPending())

	svc.Register(app, mig("002", "b"))

	f.add("exec", "BEGIN IMMEDIATE", errBoom)

	err := svc.RunPending()
	require.ErrorIs(t, err, errBoom)
	require.NotErrorIs(t, err, migrate.ErrMigrationFailed)
	require.EqualError(t, err, "gas/migrate: apply 002 (owner app): acquire lock: boom")

	f.clear()
	f.add("query", "OR dirty", errBoom)

	err = svc.RunPending()
	require.EqualError(t, err, "gas/migrate: apply 002 (owner app): read tracking table: boom")

	f.clear()
	f.add("exec", "COMMIT", errBoom)

	err = svc.RunPending()
	require.ErrorIs(t, err, migrate.ErrMigrationFailed)
	require.EqualError(
		t,
		err,
		"gas/migrate: apply 002 (owner app): migration failed: commit: boom; mark dirty: commit: boom",
	)

	f.clear()
	assert.Equal(t, []string{"001"}, versions(rows(t, svc)))
	assert.False(t, log.has("migration marked dirty"))

	require.NoError(t, svc.RunPending())
	assert.Equal(t, []string{"001", "002"}, versions(rows(t, svc)))
}

// On PostgreSQL and MySQL, a lock that is gone when the call releases it,
// here released by the migration itself, is logged, the connection is
// closed instead of returned to the pool, and the call's result stands.
func TestErrors_ReleaseFails(t *testing.T) {
	t.Parallel()

	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()

			var unlock string

			switch b.name {
			case "sqlite":
				t.Skip("SQLite holds no lock for the call")
			case "postgres":
				unlock = fmt.Sprintf("SELECT pg_advisory_unlock(%d)", migrate.PostgresLockKey)
			default:
				unlock = "DO RELEASE_LOCK(CONCAT('" + migrate.MySQLLockPrefix + "', MD5(DATABASE())))"
			}

			e := newEnv(t, b)
			e.svc.Register(
				app,
				gas.Migration{Version: "001", Up: "-- gas:no-transaction\n" + unlock},
			)

			require.NoError(t, e.svc.RunPending())

			warns := e.log.messages("warn")
			require.Len(t, warns, 1)
			assert.Equal(t, "migration lock release failed, closing its connection", warns[0].msg)
			assert.Equal(t, []string{"001"}, versions(rows(t, e.svc)))
			assertUnlocked(t, e)
		})
	}
}

// ClearDirty's driver errors are wrapped after `clear <version>: `.
func TestErrors_ClearDirty(t *testing.T) {
	t.Parallel()

	f := &faults{}
	svc, _ := started(t, faultDB(t, sqliteBackend, f))

	f.add("exec", "DELETE FROM __gas_migrations", errBoom)

	err := svc.ClearDirty(t.Context(), "001")
	require.ErrorIs(t, err, errBoom)
	require.EqualError(t, err, "gas/migrate: clear 001: boom")
}

// Init's driver errors are wrapped after `create tracking table: `.
func TestErrors_CreateTable(t *testing.T) {
	t.Parallel()

	f := &faults{}
	db := faultDB(t, sqliteBackend, f)
	svc, _ := newService(t, db)

	f.add("exec", "CREATE TABLE IF NOT EXISTS __gas_migrations", errBoom)

	err := svc.Init(t.Context())
	require.ErrorIs(t, err, errBoom)
	require.EqualError(t, err, "gas/migrate: create tracking table: boom")
}
