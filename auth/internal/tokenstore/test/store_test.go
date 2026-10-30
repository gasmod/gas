package tokenstore_test

import (
	"database/sql"
	"errors"
	"io/fs"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth/internal/sqlutil"
	"github.com/gasmod/gas/auth/internal/testutil"
	"github.com/gasmod/gas/auth/internal/tokenstore"
)

// owner is the service the store's migrations are registered for.
var owner = testutil.FakeService{ServiceName: "gas/auth/token"}

// newStore returns a store on the backend with its migrations applied.
func newStore(t *testing.T, b testutil.Backend) *tokenstore.Store {
	t.Helper()

	store, err := tokenstore.New()(b.DB, testutil.NewNopLogger(), b.Migrations)
	require.NoError(t, err)
	require.NoError(t, store.Init(owner))
	require.NoError(t, b.Migrations.RunPending())

	return store
}

// insert stores a token that expires in ttl, with times to the second.
func insert(
	t *testing.T,
	store *tokenstore.Store,
	id, subject, hash, purpose string,
	ttl time.Duration,
) time.Time {
	t.Helper()

	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(
		t,
		store.InsertToken(t.Context(), id, subject, hash, purpose, now, now.Add(ttl)),
	)

	return now.Add(ttl)
}

// Consuming a token returns what it held and deletes it, so a second
// consume, like one of an unknown hash, is ErrNotFound, unwrapped.
func TestStore_Consume(t *testing.T) {
	t.Parallel()

	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			store := newStore(t, b)
			expires := insert(t, store, "id-1", "alice", "hash-1", "reset", time.Hour)

			rec, err := store.ConsumeTokenByHash(t.Context(), "hash-1")
			require.NoError(t, err)
			assert.Equal(t, "id-1", rec.ID)
			assert.Equal(t, "alice", rec.Subject)
			assert.Equal(t, "reset", rec.Purpose)
			assert.True(t, expires.Equal(rec.ExpiresAt))

			for _, hash := range []string{"hash-1", "unknown"} {
				rec, err = store.ConsumeTokenByHash(t.Context(), hash)
				require.ErrorIs(t, err, tokenstore.ErrNotFound)
				assert.Equal(
					t,
					tokenstore.ErrNotFound,
					err,
				)
				assert.Nil(t, rec)
			}
		},
	)
}

// Of many callers consuming the same token at once, exactly one gets it.
func TestStore_ConsumeOnce(t *testing.T) {
	t.Parallel()

	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			store := newStore(t, b)
			insert(t, store, "id-1", "alice", "hash-1", "login", time.Hour)

			var (
				wins atomic.Int64
				wg   sync.WaitGroup
			)

			for range 10 {
				wg.Go(func() {
					_, err := store.ConsumeTokenByHash(t.Context(), "hash-1")
					if err == nil {
						wins.Add(1)

						return
					}

					assert.ErrorIs(t, err, tokenstore.ErrNotFound)
				})
			}

			wg.Wait()
			assert.Equal(t, int64(1), wins.Load())
		},
	)
}

// DeleteTokenByHash says how many rows it deleted, and
// DeleteTokensBySubjectPurpose deletes only that subject's tokens of that
// purpose.
func TestStore_Delete(t *testing.T) {
	t.Parallel()

	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			store := newStore(t, b)
			ctx := t.Context()

			insert(t, store, "1", "alice", "h1", "reset", time.Hour)
			insert(t, store, "2", "alice", "h2", "reset", time.Hour)
			insert(t, store, "3", "alice", "h3", "verify", time.Hour)
			insert(t, store, "4", "bob", "h4", "reset", time.Hour)

			n, err := store.DeleteTokenByHash(ctx, "h1")
			require.NoError(t, err)
			assert.Equal(t, int64(1), n)

			n, err = store.DeleteTokenByHash(ctx, "h1")
			require.NoError(t, err)
			assert.Equal(t, int64(0), n)

			require.NoError(t, store.DeleteTokensBySubjectPurpose(ctx, "alice", "reset"))

			_, err = store.ConsumeTokenByHash(ctx, "h2")
			require.ErrorIs(t, err, tokenstore.ErrNotFound)

			for _, hash := range []string{"h3", "h4"} {
				_, err = store.ConsumeTokenByHash(ctx, hash)
				require.NoError(t, err, hash)
			}
		},
	)
}

// DeleteExpiredTokens deletes only the tokens whose expiry is at or
// before the given time, and says how many: a token that expires exactly
// then is deleted, and one that expires a second later is kept.
func TestStore_DeleteExpired(t *testing.T) {
	t.Parallel()

	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			store := newStore(t, b)
			ctx := t.Context()

			insert(t, store, "1", "alice", "h1", "reset", -time.Hour)

			boundary := insert(t, store, "2", "alice", "h2", "reset", time.Hour)
			insert(t, store, "3", "bob", "h3", "reset", time.Hour+time.Second)

			n, err := store.DeleteExpiredTokens(ctx, boundary)
			require.NoError(t, err)
			assert.Equal(t, int64(2), n)

			for _, hash := range []string{"h1", "h2"} {
				_, err = store.ConsumeTokenByHash(ctx, hash)
				require.ErrorIs(t, err, tokenstore.ErrNotFound, hash)
			}

			_, err = store.ConsumeTokenByHash(ctx, "h3")
			require.NoError(t, err)
		},
	)
}

// On a closed database every operation fails with the store's prefix and
// the operation's name, and a failed consume is not ErrNotFound.
func TestStore_DatabaseErrors(t *testing.T) {
	t.Parallel()

	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			store := newStore(t, b)
			ctx := t.Context()

			require.NoError(t, b.DB.DB().Close())

			_, err := store.ConsumeTokenByHash(ctx, "h")
			require.ErrorContains(t, err, "gas/auth/token-store: consume token: ")
			require.NotErrorIs(t, err, tokenstore.ErrNotFound)
			require.ErrorContains(
				t,
				store.InsertToken(ctx, "1", "s", "h", "p", time.Now(), time.Now()),
				"gas/auth/token-store: insert token: ",
			)

			_, err = store.DeleteTokenByHash(ctx, "h")
			require.ErrorContains(t, err, "gas/auth/token-store: delete token by hash: ")
			require.ErrorContains(t, store.DeleteTokensBySubjectPurpose(ctx, "s", "p"),
				"gas/auth/token-store: delete tokens by subject and purpose: ")

			_, err = store.DeleteExpiredTokens(ctx, time.Now())
			require.ErrorContains(t, err, "gas/auth/token-store: delete expired tokens: ")
		},
	)
}

// On SQLite a stored expiry that does not parse fails the consume rather
// than reading as a zero time, and the token is gone all the same.
func TestStore_SQLiteBadTime(t *testing.T) {
	t.Parallel()

	rawDB, db, migrations := testutil.SetupSQLite(t)
	store := newStore(t, testutil.Backend{DB: db, Migrations: migrations, Name: "sqlite"})
	ctx := t.Context()

	insert(t, store, "1", "alice", "h1", "reset", time.Hour)

	_, err := rawDB.ExecContext(ctx, "UPDATE __gas_auth_tokens SET expires_at = 'garbage'")
	require.NoError(t, err)

	rec, err := store.ConsumeTokenByHash(ctx, "h1")
	require.ErrorIs(t, err, sqlutil.ErrInvalidTime)
	require.EqualError(
		t,
		err,
		"gas/auth/token-store: consume token: expires_at: invalid stored time: want the layout 2006-01-02 15:04:05",
	)
	assert.Nil(t, rec)

	_, err = store.ConsumeTokenByHash(ctx, "h1")
	require.ErrorIs(t, err, tokenstore.ErrNotFound)
}

// Init fails, naming the service, for a database provider with no open
// database or with a driver no dialect supports, and every supported
// driver name is accepted.
func TestStore_Init(t *testing.T) {
	t.Parallel()

	_, _, migrations := testutil.SetupSQLite(t)

	store, err := tokenstore.New()(
		testutil.FakeDB{DriverName: "sqlite"},
		testutil.NewNopLogger(),
		migrations,
	)
	require.NoError(t, err)

	err = store.Init(owner)
	require.ErrorIs(t, err, tokenstore.ErrNoDatabase)
	require.EqualError(t, err, "gas/auth/token: database not initialized")

	db := &sql.DB{}

	store, err = tokenstore.New()(
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
		"gas/auth/token: unsupported database driver: want one of postgres, pgx, mysql, sqlite, sqlite3",
	)

	for _, driver := range []string{"postgres", "pgx", "mysql", "sqlite", "sqlite3"} {
		store, err = tokenstore.New()(
			testutil.FakeDB{SQL: db, DriverName: driver},
			testutil.NewNopLogger(),
			migrations,
		)
		require.NoError(t, err)
		require.NoError(t, store.Init(owner), driver)
	}
}

// indexCount returns how many indexes named name the backend has.
func indexCount(t *testing.T, b testutil.Backend, name string) int {
	t.Helper()

	query := "SELECT COUNT(*) FROM pg_indexes WHERE indexname = '" + name + "'"

	switch b.Name {
	case "sqlite":
		query = "SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = '" + name + "'"
	case "mysql":
		// statistics has a row per column of an index.
		query = "SELECT COUNT(DISTINCT index_name) FROM information_schema.statistics " +
			"WHERE table_schema = DATABASE() AND index_name = '" + name + "'"
	}

	var n int

	require.NoError(t, b.DB.DB().QueryRowContext(t.Context(), query).Scan(&n))

	return n
}

// The migrations leave the table without the redundant index on
// token_hash, keeping the subject and purpose index and the uniqueness of
// the hash. Rolling the drop back makes the index again, as a database
// that ran only the first migration has it, and applying it again drops
// it.
func TestStore_Migrations(t *testing.T) {
	t.Parallel()

	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			const (
				hashIndex    = "idx___gas_auth_tokens_token_hash"
				subjectIndex = "idx___gas_auth_tokens_subject_purpose"
			)

			store := newStore(t, b)

			assert.Equal(t, 0, indexCount(t, b, hashIndex))
			assert.Equal(t, 1, indexCount(t, b, subjectIndex))

			insert(t, store, "1", "alice", "h1", "reset", time.Hour)
			require.Error(
				t,
				store.InsertToken(t.Context(), "2", "bob", "h1", "reset", time.Now(), time.Now()),
			)

			require.NoError(t, b.Migrations.Down(1))
			assert.Equal(t, 1, indexCount(t, b, hashIndex))

			require.NoError(t, b.Migrations.RunPending())
			assert.Equal(t, 0, indexCount(t, b, hashIndex))
			assert.Equal(t, 1, indexCount(t, b, subjectIndex))

			rec, err := store.ConsumeTokenByHash(t.Context(), "h1")
			require.NoError(t, err)
			assert.Equal(t, "alice", rec.Subject)
		},
	)
}

// failingMigrations is a gas.MigrationManager whose RegisterFS fails.
type failingMigrations struct {
	testutil.FakeService
}

var _ gas.MigrationManager = failingMigrations{}

// errRegister is what failingMigrations.RegisterFS returns.
var errRegister = errors.New("register failed")

// Register does nothing.
func (failingMigrations) Register(gas.Service, gas.Migration) {}

// RegisterSlice does nothing.
func (failingMigrations) RegisterSlice(gas.Service, []gas.Migration) {}

// RegisterFS returns errRegister.
func (failingMigrations) RegisterFS(gas.Service, fs.FS) error { return errRegister }

// RunPending does nothing.
func (failingMigrations) RunPending() error { return nil }

// Down does nothing.
func (failingMigrations) Down(int) error { return nil }

// Init fails, naming the service, when the migration manager does not
// take the migrations, and wraps its error.
func TestStore_InitRegisterFails(t *testing.T) {
	t.Parallel()

	store, err := tokenstore.New()(
		testutil.FakeDB{SQL: &sql.DB{}, DriverName: "sqlite"},
		testutil.NewNopLogger(),
		failingMigrations{},
	)
	require.NoError(t, err)

	err = store.Init(owner)
	require.ErrorIs(t, err, errRegister)
	require.EqualError(t, err, "gas/auth/token: register migrations: register failed")
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

// A stored expiry is never later than the one written. MySQL and SQLite
// keep the whole second before it, so .9s reads back as the earlier
// second, and Postgres keeps it as it is.
func TestStore_ExpiryNeverLater(t *testing.T) {
	t.Parallel()

	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			store := newStore(t, b)
			ctx := t.Context()
			second := time.Now().UTC().Truncate(time.Second).Add(time.Hour)

			for i, frac := range fractions {
				written := second.Add(frac)
				hash := "h" + strconv.Itoa(i)

				require.NoError(t, store.InsertToken(
					ctx, strconv.Itoa(i), "alice", hash, "reset", written, written))

				rec, err := store.ConsumeTokenByHash(ctx, hash)
				require.NoError(t, err)
				assertStoredTime(t, b, frac.String(), written, rec.ExpiresAt)
			}
		},
	)
}

// A cleanup cutoff is truncated like a stored time, so a cleanup a
// fraction of a second before a token's expiry leaves it, where MySQL
// would round the cutoff up to the expiry and delete it.
func TestStore_CutoffTruncated(t *testing.T) {
	t.Parallel()

	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			store := newStore(t, b)
			ctx := t.Context()
			second := time.Now().UTC().Truncate(time.Second).Add(time.Hour)
			next := second.Add(time.Second)

			require.NoError(t, store.InsertToken(ctx, "c", "alice", "hc", "reset", second, next))

			n, err := store.DeleteExpiredTokens(ctx, second.Add(600*time.Millisecond))
			require.NoError(t, err)
			assert.Zero(t, n, "a token that expires after the cutoff stays")

			n, err = store.DeleteExpiredTokens(ctx, next)
			require.NoError(t, err)
			assert.Equal(t, int64(1), n)
		},
	)
}
