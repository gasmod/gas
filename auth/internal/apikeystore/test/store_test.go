package apikeystore_test

import (
	"database/sql"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/auth/internal/apikeystore"
	"github.com/gasmod/gas/auth/internal/sqlutil"
	"github.com/gasmod/gas/auth/internal/testutil"
)

// owner is the service the store's migrations are registered for.
var owner = testutil.FakeService{ServiceName: "gas/auth/apikey"}

// newStore returns a store on the backend with its migrations applied.
func newStore(t *testing.T, b testutil.Backend) *apikeystore.Store {
	t.Helper()

	store, err := apikeystore.New()(b.DB, testutil.NewNopLogger(), b.Migrations)
	require.NoError(t, err)
	require.NoError(t, store.Init(owner))
	require.NoError(t, b.Migrations.RunPending())

	return store
}

// key returns insert parameters for a key of subject, created at created,
// with times to the second.
func key(id, subject, hash string, created time.Time) apikeystore.InsertKeyParams {
	return apikeystore.InsertKeyParams{
		ID:        id,
		Subject:   subject,
		Name:      "key " + id,
		KeyHash:   hash,
		KeyPrefix: "gas_" + id,
		Scopes:    []string{"read", " write ", ""},
		Metadata:  []byte(`{"team":"core"}`),
		CreatedAt: created.UTC().Truncate(time.Second),
	}
}

// A stored key reads back by its hash, with its scopes trimmed and the
// empty ones dropped; an unknown hash is ErrNotFound, unwrapped.
func TestStore_InsertGet(t *testing.T) {
	t.Parallel()

	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			store := newStore(t, b)
			now := time.Now()
			p := key("k1", "alice", "hash-1", now)

			p.ExpiresAt = new(now.Add(time.Hour).UTC().Truncate(time.Second))

			require.NoError(t, store.InsertKey(t.Context(), p))

			got, err := store.GetKeyByHash(t.Context(), "hash-1")
			require.NoError(t, err)
			assert.Equal(t, "k1", got.ID)
			assert.Equal(t, "alice", got.Subject)
			assert.Equal(t, "key k1", got.Name)
			assert.Equal(t, "gas_k1", got.KeyPrefix)
			assert.Equal(t, []string{"read", "write"}, got.Scopes)
			assert.Equal(t, map[string]any{"team": "core"}, got.Metadata)
			assert.True(t, p.CreatedAt.Equal(got.CreatedAt))
			require.NotNil(t, got.ExpiresAt)
			assert.True(t, p.ExpiresAt.Equal(*got.ExpiresAt))
			assert.Nil(t, got.LastUsed)
			assert.Nil(t, got.DeletedAt)

			got, err = store.GetKeyByHash(t.Context(), "unknown")
			require.ErrorIs(t, err, apikeystore.ErrNotFound)
			assert.Equal(
				t,
				apikeystore.ErrNotFound,
				err,
			)
			assert.Nil(t, got)
		},
	)
}

// UpdateLastUsed sets the last use time.
func TestStore_UpdateLastUsed(t *testing.T) {
	t.Parallel()

	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			store := newStore(t, b)
			now := time.Now().UTC().Truncate(time.Second)

			require.NoError(t, store.InsertKey(t.Context(), key("k1", "alice", "hash-1", now)))
			require.NoError(t, store.UpdateLastUsed(t.Context(), "k1", now.Add(time.Minute)))

			got, err := store.GetKeyByHash(t.Context(), "hash-1")
			require.NoError(t, err)
			require.NotNil(t, got.LastUsed)
			assert.True(t, now.Add(time.Minute).Equal(*got.LastUsed))
		},
	)
}

// A soft-deleted key is still found by hash, with its DeletedAt set, and
// is listed only when revoked keys are included; listing is newest first.
func TestStore_SoftDeleteAndList(t *testing.T) {
	t.Parallel()

	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			store := newStore(t, b)
			ctx := t.Context()
			base := time.Now().Add(-time.Hour)

			require.NoError(t, store.InsertKey(ctx, key("k1", "alice", "h1", base)))
			require.NoError(
				t,
				store.InsertKey(ctx, key("k2", "alice", "h2", base.Add(time.Minute))),
			)
			require.NoError(
				t,
				store.InsertKey(ctx, key("k3", "alice", "h3", base.Add(2*time.Minute))),
			)
			require.NoError(t, store.InsertKey(ctx, key("b1", "bob", "hb", base)))

			require.NoError(t, store.SoftDeleteKeyByID(ctx, "k2", time.Now()))

			revoked, err := store.GetKeyByHash(ctx, "h2")
			require.NoError(t, err)
			assert.Equal(t, "k2", revoked.ID)
			assert.NotNil(t, revoked.DeletedAt)

			active, err := store.ListKeysBySubject(ctx, "alice", false)
			require.NoError(t, err)
			assert.Equal(t, []string{"k3", "k1"}, ids(active))

			all, err := store.ListKeysBySubject(ctx, "alice", true)
			require.NoError(t, err)
			assert.Equal(t, []string{"k3", "k2", "k1"}, ids(all))
			assert.NotNil(t, all[1].DeletedAt)

			require.NoError(t, store.SoftDeleteKeysBySubject(ctx, "alice", time.Now()))

			active, err = store.ListKeysBySubject(ctx, "alice", false)
			require.NoError(t, err)
			assert.Empty(t, active)

			_, err = store.GetKeyByHash(ctx, "hb")
			require.NoError(t, err)
		},
	)
}

// A hard delete removes the row, revoked or not.
func TestStore_HardDelete(t *testing.T) {
	t.Parallel()

	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			store := newStore(t, b)
			ctx := t.Context()
			now := time.Now()

			require.NoError(t, store.InsertKey(ctx, key("k1", "alice", "h1", now)))
			require.NoError(t, store.InsertKey(ctx, key("k2", "alice", "h2", now)))
			require.NoError(t, store.InsertKey(ctx, key("b1", "bob", "hb", now)))
			require.NoError(t, store.SoftDeleteKeyByID(ctx, "k2", now))

			require.NoError(t, store.HardDeleteKeyByID(ctx, "k1"))

			all, err := store.ListKeysBySubject(ctx, "alice", true)
			require.NoError(t, err)
			assert.Equal(t, []string{"k2"}, ids(all))

			require.NoError(t, store.HardDeleteKeysBySubject(ctx, "alice"))

			all, err = store.ListKeysBySubject(ctx, "alice", true)
			require.NoError(t, err)
			assert.Empty(t, all)

			bob, err := store.ListKeysBySubject(ctx, "bob", true)
			require.NoError(t, err)
			assert.Len(t, bob, 1)
		},
	)
}

// WithTx runs the store's queries in the transaction: a rolled back insert
// leaves nothing, a committed one stays.
func TestStore_WithTx(t *testing.T) {
	t.Parallel()

	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			store := newStore(t, b)
			ctx := t.Context()

			tx, err := b.DB.DB().BeginTx(ctx, nil)
			require.NoError(t, err)
			require.NoError(
				t,
				store.WithTx(tx).InsertKey(ctx, key("k1", "alice", "h1", time.Now())),
			)
			require.NoError(t, tx.Rollback())

			_, err = store.GetKeyByHash(ctx, "h1")
			require.ErrorIs(t, err, apikeystore.ErrNotFound)

			tx, err = b.DB.DB().BeginTx(ctx, nil)
			require.NoError(t, err)
			require.NoError(
				t,
				store.WithTx(tx).InsertKey(ctx, key("k2", "alice", "h2", time.Now())),
			)
			require.NoError(t, tx.Commit())

			_, err = store.GetKeyByHash(ctx, "h2")
			require.NoError(t, err)
		},
	)
}

// On a closed database every operation fails with the store's prefix and
// the operation's name.
func TestStore_DatabaseErrors(t *testing.T) {
	t.Parallel()

	_, db, migrations := testutil.SetupSQLite(t)
	store := newStore(t, testutil.Backend{DB: db, Migrations: migrations, Name: "sqlite"})
	ctx := t.Context()
	now := time.Now()

	require.NoError(t, db.DB().Close())

	_, err := store.GetKeyByHash(ctx, "h")
	require.ErrorContains(t, err, "gas/auth/apikey-store: get key: ")
	require.NotErrorIs(t, err, apikeystore.ErrNotFound)
	require.ErrorContains(
		t,
		store.UpdateLastUsed(ctx, "k", now),
		"gas/auth/apikey-store: update last used: ",
	)
	require.ErrorContains(
		t,
		store.InsertKey(ctx, key("k", "s", "h", now)),
		"gas/auth/apikey-store: insert key: ",
	)
	require.ErrorContains(
		t,
		store.SoftDeleteKeyByID(ctx, "k", now),
		"gas/auth/apikey-store: soft delete key: ",
	)
	require.ErrorContains(t, store.SoftDeleteKeysBySubject(ctx, "s", now),
		"gas/auth/apikey-store: soft delete keys by subject: ")
	require.ErrorContains(
		t,
		store.HardDeleteKeyByID(ctx, "k"),
		"gas/auth/apikey-store: hard delete key: ",
	)
	require.ErrorContains(t, store.HardDeleteKeysBySubject(ctx, "s"),
		"gas/auth/apikey-store: hard delete keys by subject: ")

	for _, includeRevoked := range []bool{false, true} {
		_, err = store.ListKeysBySubject(ctx, "s", includeRevoked)
		require.ErrorContains(t, err, "gas/auth/apikey-store: list keys: ")
	}

	_, err = store.DeleteExpiredKeys(ctx, now)
	require.ErrorContains(t, err, "gas/auth/apikey-store: delete expired keys: ")
}

// On SQLite a stored time that does not parse fails the read, naming the
// column, for a lookup and for a listing.
func TestStore_SQLiteBadTime(t *testing.T) {
	t.Parallel()

	rawDB, db, migrations := testutil.SetupSQLite(t)
	store := newStore(t, testutil.Backend{DB: db, Migrations: migrations, Name: "sqlite"})
	ctx := t.Context()

	for _, column := range []string{"created_at", "last_used", "expires_at", "deleted_at"} {
		id := "k-" + column
		require.NoError(t, store.InsertKey(ctx, key(id, "s-"+column, "h-"+column, time.Now())))

		//nolint:gosec // G202: column comes from a fixed list in the test
		_, err := rawDB.ExecContext(
			ctx,
			"UPDATE __gas_auth_api_keys SET "+column+" = 'garbage' WHERE id = ?",
			id,
		)
		require.NoError(t, err)

		want := column + ": invalid stored time: want the layout 2006-01-02 15:04:05"

		if column != "deleted_at" {
			_, err = store.GetKeyByHash(ctx, "h-"+column)
			require.ErrorIs(t, err, sqlutil.ErrInvalidTime)
			require.EqualError(t, err, "gas/auth/apikey-store: get key: "+want)
		}

		_, err = store.ListKeysBySubject(ctx, "s-"+column, true)
		require.ErrorIs(t, err, sqlutil.ErrInvalidTime)
		require.EqualError(t, err, "gas/auth/apikey-store: list keys: "+want)
	}
}

// Stored metadata that is not a JSON object or null fails the read with
// ErrInvalidMetadata and a fixed reason, never quoting the stored value.
func TestStore_BadMetadata(t *testing.T) {
	t.Parallel()

	rawDB, db, migrations := testutil.SetupSQLite(t)
	store := newStore(t, testutil.Backend{DB: db, Migrations: migrations, Name: "sqlite"})
	ctx := t.Context()

	require.NoError(t, store.InsertKey(ctx, key("k1", "alice", "h1", time.Now())))

	for _, stored := range []string{`{"secret":"hunter2"`, `["hunter2"]`, `"hunter2"`} {
		_, err := rawDB.ExecContext(ctx, "UPDATE __gas_auth_api_keys SET metadata = ?", stored)
		require.NoError(t, err)

		_, err = store.GetKeyByHash(ctx, "h1")
		require.ErrorIs(t, err, apikeystore.ErrInvalidMetadata)
		require.EqualError(
			t, err, "gas/auth/apikey-store: get key: invalid stored metadata: want a JSON object",
		)

		_, err = store.ListKeysBySubject(ctx, "alice", false)
		require.ErrorIs(t, err, apikeystore.ErrInvalidMetadata)
		require.EqualError(
			t, err, "gas/auth/apikey-store: list keys: invalid stored metadata: want a JSON object",
		)
		assert.NotContains(t, err.Error(), "hunter2")
	}
}

// Keys with the same creation time are listed by ID, descending, so the
// order is the same on every read, with or without revoked keys.
func TestStore_ListSameCreatedAt(t *testing.T) {
	t.Parallel()

	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			store := newStore(t, b)
			ctx := t.Context()
			created := time.Now()

			for _, id := range []string{"b", "c", "a"} {
				require.NoError(t, store.InsertKey(ctx, key(id, "alice", "h-"+id, created)))
			}

			require.NoError(
				t,
				store.InsertKey(ctx, key("z", "alice", "h-z", created.Add(-time.Minute))),
			)

			for _, includeRevoked := range []bool{false, true} {
				keys, err := store.ListKeysBySubject(ctx, "alice", includeRevoked)
				require.NoError(t, err)
				assert.Equal(t, []string{"c", "b", "a", "z"}, ids(keys))
			}
		},
	)
}

// indexCount returns how many indexes named name the backend has.
func indexCount(t *testing.T, b testutil.Backend, name string) int {
	t.Helper()

	query := "SELECT COUNT(*) FROM pg_indexes WHERE indexname = $1"

	switch b.Name {
	case "sqlite":
		query = "SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?"
	case "mysql":
		// statistics has a row per column of an index.
		query = `SELECT COUNT(DISTINCT index_name) FROM information_schema.statistics
			WHERE table_schema = DATABASE() AND index_name = ?`
	}

	var n int

	require.NoError(t, b.DB.DB().QueryRowContext(t.Context(), query, name).Scan(&n))

	return n
}

// The migrations leave the table without the redundant index on
// key_hash, keeping the subject index and the uniqueness of the hash.
// Rolling the drop back makes the index again, as a database that ran
// only the first migrations has it, and applying it again drops it.
func TestStore_Migrations(t *testing.T) {
	t.Parallel()

	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			const (
				hashIndex    = "idx___gas_auth_api_keys_key_hash"
				subjectIndex = "idx___gas_auth_api_keys_subject"
			)

			store := newStore(t, b)
			ctx := t.Context()

			assert.Equal(t, 0, indexCount(t, b, hashIndex))
			assert.Equal(t, 1, indexCount(t, b, subjectIndex))

			require.NoError(t, store.InsertKey(ctx, key("k1", "alice", "h1", time.Now())))
			require.Error(
				t,
				store.InsertKey(ctx, key("k2", "bob", "h1", time.Now())),
				"the hash stays unique",
			)

			require.NoError(t, b.Migrations.Down(1))
			assert.Equal(t, 1, indexCount(t, b, hashIndex))

			require.NoError(t, b.Migrations.RunPending())
			assert.Equal(t, 0, indexCount(t, b, hashIndex))
			assert.Equal(t, 1, indexCount(t, b, subjectIndex))

			got, err := store.GetKeyByHash(ctx, "h1")
			require.NoError(t, err)
			assert.Equal(t, "alice", got.Subject)
		},
	)
}

// Init fails, naming the service, for a database provider with no open
// database or with a driver no dialect supports, and every supported
// driver name is accepted.
func TestStore_Init(t *testing.T) {
	t.Parallel()

	_, _, migrations := testutil.SetupSQLite(t)

	store, err := apikeystore.New()(
		testutil.FakeDB{DriverName: "sqlite"},
		testutil.NewNopLogger(),
		migrations,
	)
	require.NoError(t, err)

	err = store.Init(owner)
	require.ErrorIs(t, err, apikeystore.ErrNoDatabase)
	require.EqualError(t, err, "gas/auth/apikey: database not initialized")

	db := &sql.DB{}

	store, err = apikeystore.New()(
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
		"gas/auth/apikey: unsupported database driver: want one of postgres, pgx, mysql, sqlite, sqlite3",
	)

	for _, driver := range []string{"postgres", "pgx", "mysql", "sqlite", "sqlite3"} {
		store, err = apikeystore.New()(
			testutil.FakeDB{SQL: db, DriverName: driver},
			testutil.NewNopLogger(),
			migrations,
		)
		require.NoError(t, err)
		require.NoError(t, store.Init(owner), driver)
	}
}

// DeleteExpiredKeys removes the keys whose expiry is at or before the
// given time, soft-deleted or not, returns how many, and leaves keys
// without an expiry and keys expiring later: a key that expires exactly
// then is deleted.
func TestStore_DeleteExpiredKeys(t *testing.T) {
	t.Parallel()

	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			store := newStore(t, b)
			ctx := t.Context()
			now := time.Now().UTC().Truncate(time.Second)

			for id, expiry := range map[string]*time.Time{
				"past":    new(now.Add(-time.Hour)),
				"revoked": new(now.Add(-time.Minute)),
				"at":      new(now),
				"future":  new(now.Add(time.Hour)),
				"never":   nil,
			} {
				p := key(id, "alice", "h-"+id, now.Add(-2*time.Hour))

				p.ExpiresAt = expiry
				require.NoError(t, store.InsertKey(ctx, p))
			}

			require.NoError(t, store.SoftDeleteKeyByID(ctx, "revoked", now))

			n, err := store.DeleteExpiredKeys(ctx, now)
			require.NoError(t, err)
			assert.Equal(t, int64(3), n)

			left, err := store.ListKeysBySubject(ctx, "alice", true)
			require.NoError(t, err)

			got := ids(left)
			slices.Sort(got)
			assert.Equal(t, []string{"future", "never"}, got)

			n, err = store.DeleteExpiredKeys(ctx, now)
			require.NoError(t, err)
			assert.Equal(t, int64(0), n)
		},
	)
}

// Metadata stored as JSON null reads as an empty map, never nil, and
// stored scopes that clean to nothing read as an empty slice, never nil.
func TestStore_EmptyValuesNotNil(t *testing.T) {
	t.Parallel()

	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			store := newStore(t, b)
			ctx := t.Context()

			p := key("k1", "alice", "h1", time.Now())

			p.Scopes = nil
			p.Metadata = []byte("null")
			require.NoError(t, store.InsertKey(ctx, p))

			got, err := store.GetKeyByHash(ctx, "h1")
			require.NoError(t, err)
			assert.Equal(t, map[string]any{}, got.Metadata)
			assert.Equal(t, []string{}, got.Scopes)

			list, err := store.ListKeysBySubject(ctx, "alice", false)
			require.NoError(t, err)
			assert.NotNil(t, list[0].Metadata)
			assert.NotNil(t, list[0].Scopes)
		},
	)
}

// ids returns the IDs of keys, in order.
func ids(keys []apikeystore.KeyInfo) []string {
	out := make([]string, len(keys))
	for i := range keys {
		out[i] = keys[i].ID
	}

	return out
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

// A stored time is never later than the one written, whichever write
// stores it. MySQL and SQLite keep the whole second before it, so .9s
// reads back as the earlier second, and Postgres keeps it as it is.
func TestStore_ExpiryNeverLater(t *testing.T) {
	t.Parallel()

	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			store := newStore(t, b)
			ctx := t.Context()
			second := time.Now().UTC().Truncate(time.Second).Add(time.Hour)

			for i, frac := range fractions {
				id := strconv.Itoa(i)
				subject := "s" + id
				p := key(id, subject, "h"+id, second)

				p.CreatedAt = second.Add(frac)
				p.ExpiresAt = new(second.Add(time.Hour + frac))
				require.NoError(t, store.InsertKey(ctx, p))

				lastUsed := second.Add(time.Minute + frac)
				require.NoError(t, store.UpdateLastUsed(ctx, id, lastUsed))

				deleted := second.Add(2*time.Minute + frac)
				if i%2 == 0 {
					require.NoError(t, store.SoftDeleteKeyByID(ctx, id, deleted))
				} else {
					require.NoError(t, store.SoftDeleteKeysBySubject(ctx, subject, deleted))
				}

				got, err := store.GetKeyByHash(ctx, "h"+id)
				require.NoError(t, err)
				require.NotNil(t, got.ExpiresAt)
				require.NotNil(t, got.LastUsed)
				require.NotNil(t, got.DeletedAt)
				assertStoredTime(t, b, id+" created_at", p.CreatedAt, got.CreatedAt)
				assertStoredTime(t, b, id+" expires_at", *p.ExpiresAt, *got.ExpiresAt)
				assertStoredTime(t, b, id+" last_used", lastUsed, *got.LastUsed)
				assertStoredTime(t, b, id+" deleted_at", deleted, *got.DeletedAt)
			}
		},
	)
}

// The cleanup cutoff is truncated like a stored time, so a cleanup a
// fraction of a second before a key's expiry leaves it, where MySQL would
// round the cutoff up to the expiry and delete it.
func TestStore_CutoffTruncated(t *testing.T) {
	t.Parallel()

	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			store := newStore(t, b)
			ctx := t.Context()
			second := time.Now().UTC().Truncate(time.Second).Add(time.Hour)
			p := key("cutoff", "alice", "h-cutoff", second)

			p.ExpiresAt = new(second.Add(time.Second))
			require.NoError(t, store.InsertKey(ctx, p))

			n, err := store.DeleteExpiredKeys(ctx, second.Add(600*time.Millisecond))
			require.NoError(t, err)
			assert.Zero(t, n, "a key that expires after the cutoff stays")
		},
	)
}
