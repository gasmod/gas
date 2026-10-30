package migrate_test

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/migrate"
)

// Two instances on one database, each with its own *sql.DB as two
// processes would have, start at once with the same pending migration.
// The first takes the lock and stops inside the migration; the second
// waits for the lock, which it logs; once the first finishes, the second
// takes the lock, reads the table again and finds the migration applied.
// On SQLite the lock is the write lock of the migration's transaction,
// and the second reads the table again inside its own. So exactly one
// applies it (a second CREATE TABLE would fail), both return nil, and
// the row is clean.
func TestConcurrency_TwoRunnersOneApplies(t *testing.T) {
	t.Parallel()

	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()

			e := newEnv(t, b)
			peer, peerLog := e.peer(t)
			g := newGate(t, e)

			m := gas.Migration{
				Version: "001",
				Up:      g.sql + " CREATE TABLE raced (id INT);",
				Down:    "DROP TABLE raced",
			}
			e.svc.Register(app, m)
			peer.Register(app, m)

			first := make(chan error, 1)

			go func() { first <- e.svc.RunPending() }()

			g.blocked()

			second := make(chan error, 1)

			go func() { second <- peer.RunPending() }()

			waitLogged(t, peerLog, "waiting for the migration lock")
			g.release()

			require.NoError(t, <-first)
			require.NoError(t, <-second)

			assert.Equal(
				t,
				[]migrate.AppliedMigration{{Version: "001", Service: "app"}},
				rows(t, e.svc),
			)
			assert.True(t, e.log.has("migration applied"))
			assert.False(t, peerLog.has("migration applied"))

			// On SQLite the second found the migration pending, waited for
			// the write lock to apply it, and then found its row.
			assert.Equal(t, b.name != "sqlite", peerLog.has("no pending migrations"))
			require.NoError(t, e.svc.CheckReady(t.Context()))
			require.NoError(t, peer.CheckReady(t.Context()))
		})
	}
}

// Eight instances initialize at once on a database without the tracking
// table. Each creates it under the lock, so all succeed; without the
// lock, PostgreSQL's CREATE TABLE IF NOT EXISTS races and fails all but
// one.
func TestConcurrency_InitTogether(t *testing.T) {
	t.Parallel()

	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()

			dsn := b.fresh(t)

			svcs := make([]*migrate.Service, 8)
			for i := range svcs {
				svcs[i], _ = newService(t, openDB(t, b, dsn))
			}

			var wg sync.WaitGroup

			errs := make([]error, len(svcs))

			for i, svc := range svcs {
				wg.Go(func() { errs[i] = svc.Init(t.Context()) })
			}

			wg.Wait()

			for i, err := range errs {
				require.NoError(t, err, "instance %d", i)
			}

			for _, svc := range svcs {
				require.NoError(t, svc.Close(t.Context()))
			}
		})
	}
}

// A run waits for the lock no longer than the lock timeout, and then
// fails with ErrLockTimeout having changed nothing; the lock it could not
// take is still the other holder's.
func TestConcurrency_LockTimeout(t *testing.T) {
	t.Parallel()

	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()

			e := newEnv(t, b, migrate.WithLockTimeout(300*time.Millisecond))

			e.svc.Register(app, mig("001", "a"))
			insertRow(t, e, "000", "app", false)
			e.svc.Register(app, gas.Migration{Version: "000", Up: "SELECT 1", Down: "SELECT 1"})

			holdLock(t, e)

			start := time.Now()
			err := e.svc.RunPending()

			want := "gas/migrate: acquire lock: lock timed out"
			if b.name == "sqlite" {
				want = "gas/migrate: apply 001 (owner app): acquire lock: lock timed out"
			}

			require.ErrorIs(t, err, migrate.ErrLockTimeout)
			require.EqualError(t, err, want)
			assert.Less(t, time.Since(start), 5*time.Second)
			assert.True(t, e.log.has("waiting for the migration lock"))
			assert.False(t, e.exists(t, "a"))
			assert.False(t, e.log.has("migration marked dirty"))

			require.ErrorIs(t, e.svc.Down(1), migrate.ErrLockTimeout)
			require.ErrorIs(t, e.svc.ClearDirty(t.Context(), "001"), migrate.ErrLockTimeout)
			assert.Equal(t, []string{"000"}, versions(rows(t, e.svc)))
		})
	}
}

// MySQL waits in GET_LOCK a second at a time, so a lock timeout of more
// than a second still ends at the timeout, and a lock released during
// the wait is taken.
func TestConcurrency_MySQLLongWait(t *testing.T) {
	t.Parallel()

	e := newEnv(t, mysqlBackend, migrate.WithLockTimeout(1500*time.Millisecond))
	release := holdLock(t, e)

	start := time.Now()

	require.ErrorIs(t, e.svc.RunPending(), migrate.ErrLockTimeout)
	assert.GreaterOrEqual(t, time.Since(start), 1400*time.Millisecond)

	e2 := newEnv(t, mysqlBackend, migrate.WithLockTimeout(10*time.Second))
	release2 := holdLock(t, e2)

	time.AfterFunc(1500*time.Millisecond, release2)
	e2.svc.Register(app, mig("001", "a"))

	require.NoError(t, e2.svc.RunPending())
	assert.True(t, e2.exists(t, "a"))

	release()
}

// The wait for the lock ends when the call's context ends, with the
// context's error, not ErrLockTimeout.
func TestConcurrency_ContextEndsWait(t *testing.T) {
	t.Parallel()

	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()

			e := newEnv(t, b)
			e.svc.Register(app, mig("001", "a"))
			holdLock(t, e)

			ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
			defer cancel()

			want := "gas/migrate: acquire lock: context deadline exceeded"
			if b.name == "sqlite" {
				want = "gas/migrate: apply 001 (owner app): acquire lock: context deadline exceeded"
			}

			err := e.svc.RunPendingContext(ctx)
			require.ErrorIs(t, err, context.DeadlineExceeded)
			require.NotErrorIs(t, err, migrate.ErrLockTimeout)
			require.EqualError(t, err, want)
		})
	}
}

// A run releases the lock when it ends, failed or not, so another
// instance takes it at once; on PostgreSQL and MySQL nothing still holds
// it afterwards.
func TestConcurrency_LockReleased(t *testing.T) {
	t.Parallel()

	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()

			e := newEnv(t, b, migrate.WithLockTimeout(time.Second))
			peer, peerLog := e.peer(t, migrate.WithLockTimeout(time.Second))

			e.svc.Register(app, gas.Migration{Version: "001", Up: "SELECT * FROM missing"})
			require.ErrorIs(t, e.svc.RunPending(), migrate.ErrMigrationFailed)

			require.NoError(t, peer.ClearDirty(t.Context(), "001"))
			assert.False(t, peerLog.has("waiting for the migration lock"))
			assertUnlocked(t, e)
		})
	}
}

// holdLock holds the migration lock of e's database on another connection
// until the returned function, or the end of the test, releases it.
func holdLock(t *testing.T, e *env) func() {
	t.Helper()

	other := openDB(t, e.b, e.dsn)

	conn, err := other.DB().Conn(t.Context())
	require.NoError(t, err)

	var lock, unlock string

	switch e.b.name {
	case "sqlite":
		lock, unlock = "BEGIN IMMEDIATE", "ROLLBACK"
	case "postgres":
		lock = fmt.Sprintf("SELECT pg_advisory_lock(%d)", migrate.PostgresLockKey)
		unlock = fmt.Sprintf("SELECT pg_advisory_unlock(%d)", migrate.PostgresLockKey)
	default:
		lock = "DO GET_LOCK(CONCAT('" + migrate.MySQLLockPrefix + "', MD5(DATABASE())), 10)"
		unlock = "DO RELEASE_LOCK(CONCAT('" + migrate.MySQLLockPrefix + "', MD5(DATABASE())))"
	}

	_, err = conn.ExecContext(t.Context(), lock)
	require.NoError(t, err)

	var once sync.Once

	release := func() {
		once.Do(func() {
			_, err := conn.ExecContext(context.Background(), unlock)
			assert.NoError(t, err)
			assert.NoError(t, conn.Close())
		})
	}

	t.Cleanup(release)

	return release
}

// assertUnlocked checks that nothing holds the migration lock of e's
// database: another connection takes it at once.
func assertUnlocked(t *testing.T, e *env) {
	t.Helper()

	var q string

	switch e.b.name {
	case "sqlite":
	case "postgres":
		q = fmt.Sprintf("SELECT pg_try_advisory_lock(%d) AND pg_advisory_unlock(%d)",
			migrate.PostgresLockKey, migrate.PostgresLockKey)
	default:
		q = "SELECT GET_LOCK(CONCAT('" + migrate.MySQLLockPrefix + "', MD5(DATABASE())), 0) = 1 " +
			"AND RELEASE_LOCK(CONCAT('" + migrate.MySQLLockPrefix + "', MD5(DATABASE()))) = 1"
	}

	conn, err := e.db.DB().Conn(t.Context())
	require.NoError(t, err)

	defer func() { require.NoError(t, conn.Close()) }()

	if e.b.name == "sqlite" {
		// No connection holds SQLite's write lock.
		_, err = conn.ExecContext(t.Context(), "BEGIN IMMEDIATE")
		require.NoError(t, err)

		_, err = conn.ExecContext(t.Context(), "ROLLBACK")
		require.NoError(t, err)

		return
	}

	var free sql.NullBool

	require.NoError(t, conn.QueryRowContext(t.Context(), q).Scan(&free))
	assert.True(t, free.Bool)
}
