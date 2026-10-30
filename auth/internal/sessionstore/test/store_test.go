package sessionstore_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth/internal/sessionstore"
	"github.com/gasmod/gas/auth/internal/sqlutil"
	"github.com/gasmod/gas/auth/internal/testutil"
)

// owner is the service the store's migrations are registered for.
var owner = testutil.FakeService{ServiceName: "gas/auth/session"}

// newStore returns a store on the backend with its migrations applied.
func newStore(t *testing.T, b testutil.Backend) *sessionstore.Store {
	t.Helper()

	store, err := sessionstore.New()(b.DB, testutil.NewNopLogger(), b.Migrations)
	require.NoError(t, err)
	require.NoError(t, store.Init(owner))
	require.NoError(t, b.Migrations.RunPending())

	return store
}

// sqliteStore returns a store on a fresh in-memory SQLite database with
// its migrations applied, and the raw database under it.
func sqliteStore(t *testing.T) (*sessionstore.Store, *sql.DB) {
	t.Helper()

	rawDB, db, migrations := testutil.SetupSQLite(t)

	return newStore(t, testutil.Backend{DB: db, Migrations: migrations, Name: "sqlite"}), rawDB
}

// session returns a session for subject that expires in ttl, with times
// to the second so that every backend stores them exactly.
func session(idHash, subject string, ttl time.Duration) *sessionstore.Session {
	now := time.Now().UTC().Truncate(time.Second)

	return &sessionstore.Session{
		IDHash:     idHash,
		Subject:    subject,
		Metadata:   gas.BasePrincipalMetadata{"role": "admin"},
		IPAddress:  "192.0.2.1",
		UserAgent:  "test-agent",
		CreatedAt:  now,
		ExpiresAt:  now.Add(ttl),
		LastActive: now,
	}
}

// assertSession checks that got holds what want held, comparing times as
// instants.
func assertSession(t *testing.T, want, got *sessionstore.Session) {
	t.Helper()

	assert.Equal(t, want.IDHash, got.IDHash)
	assert.Equal(t, want.Subject, got.Subject)
	assert.Equal(t, want.Metadata, got.Metadata)
	assert.Equal(t, want.IPAddress, got.IPAddress)
	assert.Equal(t, want.UserAgent, got.UserAgent)
	assert.True(t, want.CreatedAt.Equal(got.CreatedAt), "created_at %v != %v",
		want.CreatedAt, got.CreatedAt)
	assert.True(t, want.ExpiresAt.Equal(got.ExpiresAt), "expires_at %v != %v",
		want.ExpiresAt, got.ExpiresAt)
	assert.True(t, want.LastActive.Equal(got.LastActive), "last_active %v != %v",
		want.LastActive, got.LastActive)
}

// A stored session reads back as it was written, and an unknown ID hash
// is ErrNotFound, unwrapped.
func TestStore_InsertGet(t *testing.T) {
	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			store := newStore(t, b)
			want := session("hash-1", "alice", time.Hour)

			require.NoError(t, store.InsertSession(t.Context(), want))

			got, err := store.GetSession(t.Context(), "hash-1")
			require.NoError(t, err)
			assertSession(t, want, got)

			got, err = store.GetSession(t.Context(), "missing")
			require.ErrorIs(t, err, sessionstore.ErrNotFound)
			assert.Equal(t, sessionstore.ErrNotFound, err)
			assert.Nil(t, got)
		},
	)
}

// A nil metadata is stored as an empty object and reads back as an empty
// map, and so does a stored JSON null.
func TestStore_NilMetadata(t *testing.T) {
	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			store := newStore(t, b)
			s := session("hash-1", "alice", time.Hour)

			s.Metadata = nil

			require.NoError(t, store.InsertSession(t.Context(), s))

			got, err := store.GetSession(t.Context(), "hash-1")
			require.NoError(t, err)
			assert.NotNil(t, got.Metadata)
			assert.Empty(t, got.Metadata)

			_, err = b.DB.DB().ExecContext(t.Context(),
				"UPDATE __gas_auth_sessions SET metadata = 'null'")
			require.NoError(t, err)

			got, err = store.GetSession(t.Context(), "hash-1")
			require.NoError(t, err)
			assert.NotNil(t, got.Metadata)
			assert.Empty(t, got.Metadata)
		},
	)
}

// Inserting an ID hash twice fails with the store's prefix.
func TestStore_InsertDuplicate(t *testing.T) {
	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			store := newStore(t, b)

			require.NoError(t,
				store.InsertSession(t.Context(), session("hash-1", "alice", time.Hour)))

			err := store.InsertSession(t.Context(), session("hash-1", "bob", time.Hour))
			require.Error(t, err)
			assert.Contains(t, err.Error(), "gas/auth/session-store: insert session: ")
		},
	)
}

// ExtendSession sets the expiry and last active time, and extending a
// missing session is not an error.
func TestStore_Extend(t *testing.T) {
	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			store := newStore(t, b)
			s := session("hash-1", "alice", time.Hour)
			require.NoError(t, store.InsertSession(t.Context(), s))

			expires := s.ExpiresAt.Add(time.Hour)
			active := s.LastActive.Add(time.Minute)
			require.NoError(t, store.ExtendSession(t.Context(), "hash-1", expires, active))
			require.NoError(t, store.ExtendSession(t.Context(), "missing", expires, active))

			got, err := store.GetSession(t.Context(), "hash-1")
			require.NoError(t, err)
			assert.True(t, expires.Equal(got.ExpiresAt))
			assert.True(t, active.Equal(got.LastActive))
		},
	)
}

// DeleteSession removes one session, DeleteSessionsBySubject all of a
// subject's, and deleting what is not there is not an error.
func TestStore_Delete(t *testing.T) {
	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			store := newStore(t, b)
			ctx := t.Context()

			for _, s := range []*sessionstore.Session{
				session("a1", "alice", time.Hour),
				session("a2", "alice", time.Hour),
				session("b1", "bob", time.Hour),
			} {
				require.NoError(t, store.InsertSession(ctx, s))
			}

			require.NoError(t, store.DeleteSession(ctx, "a1"))
			require.NoError(t, store.DeleteSession(ctx, "a1"))

			_, err := store.GetSession(ctx, "a1")
			require.ErrorIs(t, err, sessionstore.ErrNotFound)

			require.NoError(t, store.DeleteSessionsBySubject(ctx, "alice"))
			require.NoError(t, store.DeleteSessionsBySubject(ctx, "nobody"))

			_, err = store.GetSession(ctx, "a2")
			require.ErrorIs(t, err, sessionstore.ErrNotFound)

			_, err = store.GetSession(ctx, "b1")
			require.NoError(t, err)
		},
	)
}

// DeleteExpiredSessions deletes the sessions whose expiry is at or before
// the first time and those created at or before the second, and says how
// many: a session that expires exactly at the first time, or was created
// exactly at the second, is deleted, and one a second later is kept.
func TestStore_DeleteExpired(t *testing.T) {
	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			store := newStore(t, b)
			ctx := t.Context()
			now := time.Now().UTC().Truncate(time.Second)
			createdCutoff := now.Add(-24 * time.Hour)

			// at returns a session created at createdAt that expires at
			// expiresAt.
			at := func(idHash string, createdAt, expiresAt time.Time) *sessionstore.Session {
				sess := session(idHash, "alice", 0)

				sess.CreatedAt = createdAt
				sess.ExpiresAt = expiresAt

				return sess
			}

			for _, sess := range []*sessionstore.Session{
				at("expired", now.Add(-time.Hour), now.Add(-time.Minute)),
				at("expires-now", now.Add(-time.Hour), now),
				at("created-at-cutoff", createdCutoff, now.Add(time.Hour)),
				at("old", createdCutoff.Add(-time.Hour), now.Add(time.Hour)),
				at("live", createdCutoff.Add(time.Second), now.Add(time.Second)),
			} {
				require.NoError(t, store.InsertSession(ctx, sess))
			}

			n, err := store.DeleteExpiredSessions(ctx, now, createdCutoff)
			require.NoError(t, err)
			assert.Equal(t, int64(4), n)

			_, err = store.GetSession(ctx, "live")
			require.NoError(t, err)

			for _, idHash := range []string{"expired", "expires-now", "created-at-cutoff", "old"} {
				_, err = store.GetSession(ctx, idHash)
				require.ErrorIs(t, err, sessionstore.ErrNotFound, idHash)
			}
		},
	)
}

// RotateSession moves the session to the new ID hash, sets its address,
// User-Agent, expiry and last active time, and keeps its subject,
// metadata and creation time; with the old hash gone it changes nothing
// and returns ErrNotFound, unwrapped.
func TestStore_Rotate(t *testing.T) {
	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			store := newStore(t, b)
			ctx := t.Context()
			old := session("old", "alice", time.Hour)

			old.CreatedAt = old.CreatedAt.Add(-time.Hour)

			require.NoError(t, store.InsertSession(ctx, old))

			repl := session("new", "ignored", 2*time.Hour)

			repl.Metadata = gas.BasePrincipalMetadata{"ignored": true}
			repl.IPAddress = "198.51.100.7"
			repl.UserAgent = "other-agent"
			repl.LastActive = repl.LastActive.Add(time.Minute)

			require.NoError(t, store.RotateSession(ctx, "old", repl))

			_, err := store.GetSession(ctx, "old")
			require.ErrorIs(t, err, sessionstore.ErrNotFound)

			got, err := store.GetSession(ctx, "new")
			require.NoError(t, err)
			assertSession(t, &sessionstore.Session{
				IDHash:     "new",
				Subject:    "alice",
				Metadata:   old.Metadata,
				IPAddress:  "198.51.100.7",
				UserAgent:  "other-agent",
				CreatedAt:  old.CreatedAt,
				ExpiresAt:  repl.ExpiresAt,
				LastActive: repl.LastActive,
			}, got)

			err = store.RotateSession(ctx, "old", session("other", "alice", time.Hour))
			assert.Equal(t, sessionstore.ErrNotFound, err)

			_, err = store.GetSession(ctx, "other")
			require.ErrorIs(t, err, sessionstore.ErrNotFound)
		},
	)
}

// Rotating to an ID hash another session has fails with the store's
// prefix, and the session keeps its old hash.
func TestStore_RotateTaken(t *testing.T) {
	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			store := newStore(t, b)
			ctx := t.Context()

			require.NoError(t, store.InsertSession(ctx, session("old", "alice", time.Hour)))
			require.NoError(t, store.InsertSession(ctx, session("taken", "bob", time.Hour)))

			err := store.RotateSession(ctx, "old", session("taken", "alice", time.Hour))
			require.ErrorContains(t, err, "gas/auth/session-store: rotate session: ")
			require.NotErrorIs(t, err, sessionstore.ErrNotFound)

			_, err = store.GetSession(ctx, "old")
			require.NoError(t, err)
		},
	)
}

// Of several concurrent rotations of one session, exactly one succeeds
// and the rest find it gone.
func TestStore_RotateConcurrent(t *testing.T) {
	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			store := newStore(t, b)
			ctx := t.Context()

			require.NoError(t, store.InsertSession(ctx, session("old", "alice", time.Hour)))

			const n = 8

			var (
				wg   sync.WaitGroup
				mu   sync.Mutex
				oks  int
				errs []error
			)

			for i := range n {
				wg.Go(func() {
					err := store.RotateSession(ctx, "old",
						session("new-"+strconv.Itoa(i), "alice", time.Hour))

					mu.Lock()
					defer mu.Unlock()

					if err == nil {
						oks++
					} else {
						errs = append(errs, err)
					}
				})
			}

			wg.Wait()

			assert.Equal(t, 1, oks)

			for _, err := range errs {
				assert.Equal(t, sessionstore.ErrNotFound, err)
			}
		},
	)
}

// The hash_session_ids migration deletes the sessions stored under raw
// IDs, which signs everyone out, and renames the key column to id_hash.
func TestStore_HashMigrationClearsSessions(t *testing.T) {
	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			ctx := t.Context()
			dir := filepath.Join("..", b.Name)

			//nolint:gosec // G304: a fixed migration file of the store
			up, err := os.ReadFile(filepath.Join(dir, "20250323001_create_sessions.up.sql"))
			require.NoError(t, err)

			//nolint:gosec // G304: a fixed migration file of the store
			down, err := os.ReadFile(filepath.Join(dir, "20250323001_create_sessions.down.sql"))
			require.NoError(t, err)

			// The schema before this version: only the first migration.
			b.Migrations.Register(owner, gas.Migration{
				Version:     "20250323001",
				Description: "create sessions",
				Up:          string(up),
				Down:        string(down),
			})
			require.NoError(t, b.Migrations.RunPending())

			_, err = b.DB.DB().ExecContext(ctx,
				// MySQL's first migration gives metadata and user_agent
				// no default, so the row sets them on every backend.
				`INSERT INTO __gas_auth_sessions (id, subject, metadata, user_agent, expires_at)
				 VALUES ('raw-session-id', 'alice', '{}', '', CURRENT_TIMESTAMP)`)
			require.NoError(t, err)

			store := newStore(t, b)

			var count int

			err = b.DB.DB().QueryRowContext(ctx,
				"SELECT COUNT(*) FROM __gas_auth_sessions").Scan(&count)
			require.NoError(t, err)
			assert.Zero(t, count)

			require.NoError(t, store.InsertSession(ctx, session("hash-1", "alice", time.Hour)))

			_, err = store.GetSession(ctx, "hash-1")
			require.NoError(t, err)
		},
	)
}

// The down migration deletes every session and restores the id column.
func TestStore_HashMigrationDown(t *testing.T) {
	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			ctx := t.Context()
			store := newStore(t, b)

			require.NoError(t, store.InsertSession(ctx, session("hash-1", "alice", time.Hour)))
			require.NoError(t, b.Migrations.Down(1))

			var count int

			err := b.DB.DB().QueryRowContext(ctx,
				"SELECT COUNT(*) FROM __gas_auth_sessions WHERE id IS NOT NULL").Scan(&count)
			require.NoError(t, err)
			assert.Zero(t, count)
		},
	)
}

// On a closed database every operation fails with the store's prefix and
// the operation's name.
func TestStore_DatabaseErrors(t *testing.T) {
	t.Parallel()

	store, rawDB := sqliteStore(t)
	ctx := t.Context()

	require.NoError(t, rawDB.Close())

	_, err := store.GetSession(ctx, "x")
	require.ErrorContains(t, err, "gas/auth/session-store: get session: ")
	require.NotErrorIs(t, err, sessionstore.ErrNotFound)
	require.ErrorContains(t, store.InsertSession(ctx, session("x", "y", time.Hour)),
		"gas/auth/session-store: insert session: ")
	require.ErrorContains(t, store.ExtendSession(ctx, "x", time.Now(), time.Now()),
		"gas/auth/session-store: extend session: ")
	require.ErrorContains(t, store.DeleteSession(ctx, "x"),
		"gas/auth/session-store: delete session: ")
	require.ErrorContains(t, store.DeleteSessionsBySubject(ctx, "y"),
		"gas/auth/session-store: delete sessions by subject: ")
	require.ErrorContains(t, store.RotateSession(ctx, "x", session("z", "y", time.Hour)),
		"gas/auth/session-store: rotate session: ")

	_, err = store.DeleteExpiredSessions(ctx, time.Now(), time.Now())
	require.ErrorContains(t, err, "gas/auth/session-store: delete expired sessions: ")
}

// On SQLite a stored time that does not parse fails the read, naming the
// column, rather than reading as a zero time, which would look long
// expired.
func TestStore_SQLiteBadTime(t *testing.T) {
	t.Parallel()

	store, rawDB := sqliteStore(t)
	ctx := t.Context()

	for _, column := range []string{"created_at", "expires_at", "last_active"} {
		s := session("hash-"+column, "alice", time.Hour)
		require.NoError(t, store.InsertSession(ctx, s))

		//nolint:gosec // G202: column comes from a fixed list in the test
		_, err := rawDB.ExecContext(ctx,
			"UPDATE __gas_auth_sessions SET "+column+" = 'garbage' WHERE id_hash = ?", s.IDHash)
		require.NoError(t, err)

		got, err := store.GetSession(ctx, s.IDHash)
		require.ErrorIs(t, err, sqlutil.ErrInvalidTime)
		require.EqualError(t, err, "gas/auth/session-store: get session: "+column+
			": invalid stored time: want the layout 2006-01-02 15:04:05")
		assert.Nil(t, got)
	}
}

// Stored metadata that is not a JSON object fails the read, without
// quoting it.
func TestStore_BadMetadata(t *testing.T) {
	t.Parallel()

	store, rawDB := sqliteStore(t)
	ctx := t.Context()

	require.NoError(t, store.InsertSession(ctx, session("hash-1", "alice", time.Hour)))

	_, err := rawDB.ExecContext(ctx,
		"UPDATE __gas_auth_sessions SET metadata = 'hunter2' WHERE id_hash = 'hash-1'")
	require.NoError(t, err)

	_, err = store.GetSession(ctx, "hash-1")
	require.EqualError(t, err, "gas/auth/session-store: get session: metadata: not a JSON object")
}

// Metadata that cannot be JSON fails the write without quoting it.
func TestStore_UnmarshalableMetadata(t *testing.T) {
	t.Parallel()

	store, _ := sqliteStore(t)
	s := session("hash-1", "alice", time.Hour)

	s.Metadata = gas.BasePrincipalMetadata{"ch": make(chan int)}

	require.EqualError(t, store.InsertSession(t.Context(), s),
		"gas/auth/session-store: insert session: metadata: cannot be encoded as JSON")
}

// Init fails, naming the service, for a database provider with no open
// database or with a driver no dialect supports, and every supported
// driver name is accepted.
func TestStore_Init(t *testing.T) {
	t.Parallel()

	_, _, migrations := testutil.SetupSQLite(t)

	store, err := sessionstore.New()(
		testutil.FakeDB{DriverName: "sqlite"},
		testutil.NewNopLogger(),
		migrations,
	)
	require.NoError(t, err)

	err = store.Init(owner)
	require.ErrorIs(t, err, sessionstore.ErrNoDatabase)
	require.EqualError(t, err, "gas/auth/session: database not initialized")

	db := &sql.DB{}

	store, err = sessionstore.New()(
		testutil.FakeDB{SQL: db, DriverName: "oracle"},
		testutil.NewNopLogger(),
		migrations,
	)
	require.NoError(t, err)

	err = store.Init(owner)
	require.ErrorIs(t, err, sqlutil.ErrUnsupportedDriver)
	require.EqualError(
		t,
		err,
		"gas/auth/session: unsupported database driver: want one of postgres, pgx, mysql, sqlite, sqlite3",
	)

	for _, driver := range []string{"postgres", "pgx", "mysql", "sqlite", "sqlite3"} {
		// Each dialect registers its own versions, with one owner, so a
		// fresh manager per dialect keeps them apart.
		_, _, perDriver := testutil.SetupSQLite(t)

		store, err = sessionstore.New()(
			testutil.FakeDB{SQL: db, DriverName: driver},
			testutil.NewNopLogger(),
			perDriver,
		)
		require.NoError(t, err)
		require.NoError(t, store.Init(owner), driver)
	}
}

// fractions are the parts of a second the time tests write. They are
// whole microseconds, so Postgres keeps each one as it is.
var fractions = []time.Duration{
	0, 400 * time.Millisecond, 500 * time.Millisecond,
	600 * time.Millisecond, 900 * time.Millisecond, time.Second - time.Microsecond,
}

// assertStoredTime checks a time read back from b against the one
// written: MySQL and SQLite keep the whole second before it, Postgres
// keeps it as it is, and none keeps a later time.
func assertStoredTime(t *testing.T, b testutil.Backend, what string, written, stored time.Time) {
	t.Helper()

	want := written
	if b.Name != "postgres" {
		want = written.Truncate(time.Second)
	}

	assert.True(t, want.Equal(stored), "%s: %v is not %v", what, stored, want)
	assert.False(t, stored.After(written), "%s: stored later than written", what)
}

// assertSessionTimes reads idHash back and checks each of its times
// against the one written.
func assertSessionTimes(
	t *testing.T,
	b testutil.Backend,
	store *sessionstore.Store,
	idHash string,
	created, expires, active time.Time,
) {
	t.Helper()

	got, err := store.GetSession(t.Context(), idHash)
	require.NoError(t, err)
	assertStoredTime(t, b, idHash+" created_at", created, got.CreatedAt)
	assertStoredTime(t, b, idHash+" expires_at", expires, got.ExpiresAt)
	assertStoredTime(t, b, idHash+" last_active", active, got.LastActive)
}

// A stored time is never later than the one written, whichever write
// stores it. MySQL and SQLite keep the whole second before it, so .9s
// reads back as the earlier second, and Postgres keeps it as it is.
func TestStore_ExpiryNeverLater(t *testing.T) {
	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			store := newStore(t, b)
			ctx := t.Context()
			second := time.Now().UTC().Truncate(time.Second).Add(time.Hour)

			for i, frac := range fractions {
				idHash := "h" + strconv.Itoa(i)
				s := session(idHash, "alice", 0)

				s.CreatedAt = second.Add(frac)
				s.ExpiresAt = second.Add(time.Hour + frac)
				s.LastActive = second.Add(frac)

				require.NoError(t, store.InsertSession(ctx, s))
				assertSessionTimes(t, b, store, idHash, s.CreatedAt, s.ExpiresAt, s.LastActive)

				expires := s.ExpiresAt.Add(time.Hour)
				active := s.LastActive.Add(time.Minute)
				require.NoError(t, store.ExtendSession(ctx, idHash, expires, active))
				assertSessionTimes(t, b, store, idHash, s.CreatedAt, expires, active)

				repl := session("r"+idHash, "alice", 0)

				repl.ExpiresAt = expires.Add(time.Hour)
				repl.LastActive = active.Add(time.Minute)
				require.NoError(t, store.RotateSession(ctx, idHash, repl))
				assertSessionTimes(
					t, b, store, "r"+idHash, s.CreatedAt, repl.ExpiresAt, repl.LastActive)
			}
		},
	)
}

// Both cleanup cutoffs are truncated like a stored time, so a cleanup a
// fraction of a second before a session's expiry, and after its creation,
// leaves it, where MySQL would round the cutoffs up and delete it.
func TestStore_CutoffTruncated(t *testing.T) {
	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			store := newStore(t, b)
			ctx := t.Context()
			second := time.Now().UTC().Truncate(time.Second).Add(time.Hour)
			created := second.Add(-time.Hour)
			s := session("cutoff", "alice", 0)

			s.CreatedAt = created.Add(time.Second)
			s.ExpiresAt = second.Add(time.Second)
			require.NoError(t, store.InsertSession(ctx, s))

			n, err := store.DeleteExpiredSessions(
				ctx, second.Add(600*time.Millisecond), created.Add(600*time.Millisecond))
			require.NoError(t, err)
			assert.Zero(t, n, "a session that expires after the cutoffs stays")

			_, err = store.GetSession(ctx, "cutoff")
			require.NoError(t, err)
		},
	)
}
