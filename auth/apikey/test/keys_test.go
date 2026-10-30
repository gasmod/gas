package apikey_test

import (
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/auth"
	"github.com/gasmod/gas/auth/apikey"
	"github.com/gasmod/gas/auth/internal/testutil"
)

// principalOf returns the API key principal of a key ID.
func principalOf(id string) *auth.BasePrincipal {
	return auth.NewPrincipal("alice", auth.SchemeAPIKey, id, nil)
}

// Revoke keeps the key stored, revoked, so it fails with
// ErrCredentialRevoked and is listed only with WithIncludeRevoked, with
// DeletedAt set; other keys are untouched.
func TestRevoke(t *testing.T) {
	t.Parallel()

	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			svc := newService(t, b, nil, nil)
			ctx := t.Context()

			key, info, err := svc.Generate(ctx, "alice", "revoked", nil)
			require.NoError(t, err)

			other, _, err := svc.Generate(ctx, "alice", "kept", nil)
			require.NoError(t, err)

			require.NoError(t, svc.Revoke(ctx, principalOf(info.ID)))

			_, err = authenticate(t, svc, key)
			require.ErrorIs(t, err, auth.ErrCredentialRevoked)

			_, err = authenticate(t, svc, other)
			require.NoError(t, err)

			active, err := svc.List(ctx, "alice")
			require.NoError(t, err)
			require.Len(t, active, 1)
			assert.Equal(t, "kept", active[0].Name)

			all, err := svc.List(ctx, "alice", apikey.WithIncludeRevoked())
			require.NoError(t, err)
			require.Len(t, all, 2)

			for _, k := range all {
				if k.ID == info.ID {
					assert.NotNil(t, k.DeletedAt)
				} else {
					assert.Nil(t, k.DeletedAt)
				}
			}

			require.NoError(t, svc.Revoke(ctx, principalOf("unknown")), "an unknown key is a no-op")
		},
	)
}

// Revoking a key again keeps the first revocation time.
func TestRevoke_Twice(t *testing.T) {
	t.Parallel()

	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			svc := newService(t, b, nil, nil)
			ctx := t.Context()

			_, info, err := svc.Generate(ctx, "alice", "revoked", nil)
			require.NoError(t, err)
			require.NoError(t, svc.Revoke(ctx, principalOf(info.ID)))

			revokedAt := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
			exec(
				t,
				b,
				"UPDATE __gas_auth_api_keys SET deleted_at = ? WHERE id = ?",
				revokedAt,
				info.ID,
			)
			require.NoError(t, svc.Revoke(ctx, principalOf(info.ID)))

			all, err := svc.List(ctx, "alice", apikey.WithIncludeRevoked())
			require.NoError(t, err)
			require.Len(t, all, 1)
			require.NotNil(t, all[0].DeletedAt)
			assert.True(t, revokedAt.Equal(*all[0].DeletedAt), "the first revocation time is kept")
		},
	)
}

// RevokeAll revokes every key of the subject and no other's, and
// RevokeAllByScheme does so only for the API key scheme.
func TestRevokeAll(t *testing.T) {
	t.Parallel()

	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			svc := newService(t, b, nil, nil)
			ctx := t.Context()

			a1, _, err := svc.Generate(ctx, "alice", "1", nil)
			require.NoError(t, err)

			a2, _, err := svc.Generate(ctx, "alice", "2", nil)
			require.NoError(t, err)

			bob, _, err := svc.Generate(ctx, "bob", "1", nil)
			require.NoError(t, err)

			require.NoError(t, svc.RevokeAllByScheme(ctx, "alice", auth.SchemeJWT))

			_, err = authenticate(t, svc, a1)
			require.NoError(t, err, "another scheme is a no-op")

			require.NoError(t, svc.RevokeAllByScheme(ctx, "alice", auth.SchemeAPIKey))

			for _, k := range []string{a1, a2} {
				_, err = authenticate(t, svc, k)
				require.ErrorIs(t, err, auth.ErrCredentialRevoked)
			}

			_, err = authenticate(t, svc, bob)
			require.NoError(t, err)

			require.NoError(t, svc.RevokeAll(ctx, "bob"))

			_, err = authenticate(t, svc, bob)
			require.ErrorIs(t, err, auth.ErrCredentialRevoked)
		},
	)
}

// Delete removes the key, revoked or not, which then authenticates as
// unknown; DeleteAll removes every key of the subject and no other's.
func TestDelete(t *testing.T) {
	t.Parallel()

	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			svc := newService(t, b, nil, nil)
			ctx := t.Context()

			key, info, err := svc.Generate(ctx, "alice", "deleted", nil)
			require.NoError(t, err)
			require.NoError(t, svc.Revoke(ctx, principalOf(info.ID)))
			require.NoError(t, svc.Delete(ctx, principalOf(info.ID)))

			_, err = authenticate(t, svc, key)
			require.ErrorIs(t, err, auth.ErrUnauthenticated)
			require.NotErrorIs(t, err, auth.ErrCredentialRevoked)
			assert.Equal(t, 0, countKeys(t, svc, "alice"))

			require.NoError(t, svc.Delete(ctx, principalOf("unknown")), "an unknown key is a no-op")

			_, _, err = svc.Generate(ctx, "alice", "1", nil)
			require.NoError(t, err)

			_, revoked, err := svc.Generate(ctx, "alice", "2", nil)
			require.NoError(t, err)
			require.NoError(t, svc.Revoke(ctx, principalOf(revoked.ID)))

			_, _, err = svc.Generate(ctx, "bob", "1", nil)
			require.NoError(t, err)

			require.NoError(t, svc.DeleteAll(ctx, "alice"))

			assert.Equal(t, 0, countKeys(t, svc, "alice"))
			assert.Equal(t, 1, countKeys(t, svc, "bob"))
		},
	)
}

// Revoke and Delete fail with ErrSchemeMismatch for a principal of
// another scheme, even one whose CredentialID names a stored key, which
// stays valid; they fail with ErrNilPrincipal for a nil principal or a
// nil pointer. Both sentinels are the auth package's.
func TestRevokeDelete_Principal(t *testing.T) {
	t.Parallel()

	svc := newService(t, sqliteBackend(t), nil, nil)
	ctx := t.Context()

	key, info, err := svc.Generate(ctx, "alice", "k", nil)
	require.NoError(t, err)

	session := auth.NewPrincipal("alice", auth.SchemeSession, info.ID, nil)

	for _, err := range []error{svc.Revoke(ctx, session), svc.Delete(ctx, session)} {
		require.ErrorIs(t, err, apikey.ErrSchemeMismatch)
		require.ErrorIs(t, err, auth.ErrSchemeMismatch)
		require.EqualError(t, err, "gas/auth/apikey: principal scheme mismatch")
	}

	_, err = authenticate(t, svc, key)
	require.NoError(t, err, "a principal of another scheme revokes nothing")

	var typedNil *auth.BasePrincipal

	for _, op := range []func() error{
		func() error { return svc.Revoke(ctx, nil) },
		func() error { return svc.Revoke(ctx, typedNil) },
		func() error { return svc.Delete(ctx, nil) },
		func() error { return svc.Delete(ctx, typedNil) },
	} {
		err = op()
		require.ErrorIs(t, err, apikey.ErrNilPrincipal)
		require.ErrorIs(t, err, auth.ErrNilPrincipal)
		require.EqualError(t, err, "gas/auth/apikey: nil principal")
	}
}

// List returns the subject's keys newest first, keys with the same
// creation time by ID, descending, and an empty slice, not nil, for a
// subject without keys. Expired keys are listed.
func TestList(t *testing.T) {
	t.Parallel()

	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			svc := newService(t, b, nil, nil)
			ctx := t.Context()

			base := time.Now().Add(-time.Hour)

			for i := range 3 {
				_, info, err := svc.Generate(ctx, "alice", fmt.Sprintf("key-%d", i), nil)
				require.NoError(t, err)

				exec(t, b, "UPDATE __gas_auth_api_keys SET created_at = ? WHERE id = ?",
					base.Add(time.Duration(i)*time.Minute), info.ID)
			}

			_, expired, err := svc.Generate(ctx, "alice", "expired", nil)
			require.NoError(t, err)
			setExpiry(t, b, expired.ID, time.Now().Add(-time.Hour))

			keys, err := svc.List(ctx, "alice")
			require.NoError(t, err)
			require.Len(t, keys, 4)
			assert.Equal(t, "expired", keys[0].Name)
			assert.Equal(t, []string{"key-2", "key-1", "key-0"},
				[]string{keys[1].Name, keys[2].Name, keys[3].Name})

			exec(t, b, "UPDATE __gas_auth_api_keys SET created_at = ? WHERE subject = ?",
				base, "alice")

			keys, err = svc.List(ctx, "alice")
			require.NoError(t, err)

			got := make([]string, len(keys))
			for i := range keys {
				got[i] = keys[i].ID
			}

			want := slices.Clone(got)
			slices.Sort(want)
			slices.Reverse(want)
			assert.Equal(
				t,
				want,
				got,
				"keys created at the same time are ordered by ID, descending",
			)

			none, err := svc.List(ctx, "nobody")
			require.NoError(t, err)
			assert.NotNil(t, none)
			assert.Empty(t, none)
		},
	)
}

// Database failures fail the management methods, naming the operation.
func TestKeys_DatabaseFailure(t *testing.T) {
	t.Parallel()

	b := sqliteBackend(t)
	svc := newService(t, b, nil, nil)
	ctx := t.Context()

	require.NoError(t, b.DB.DB().Close())

	_, err := svc.List(ctx, "alice")
	require.ErrorContains(t, err, "gas/auth/apikey: list keys: ")
	require.ErrorContains(t, svc.Revoke(ctx, principalOf("k")), "gas/auth/apikey: revoke: ")
	require.ErrorContains(t, svc.RevokeAll(ctx, "alice"), "gas/auth/apikey: revoke all: ")
	require.ErrorContains(t, svc.Delete(ctx, principalOf("k")), "gas/auth/apikey: delete: ")
	require.ErrorContains(t, svc.DeleteAll(ctx, "alice"), "gas/auth/apikey: delete all: ")
}

// WithTx runs the operations in the transaction: a rolled back Generate
// leaves nothing, and a committed one stays.
func TestWithTx(t *testing.T) {
	t.Parallel()

	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			svc := newService(t, b, nil, nil)
			ctx := t.Context()

			tx, err := b.DB.BeginTx(ctx, nil)
			require.NoError(t, err)

			key, _, err := svc.WithTx(tx).Generate(ctx, "alice", "rolled back", nil)
			require.NoError(t, err)
			require.NoError(t, tx.Rollback())

			_, err = authenticate(t, svc, key)
			require.ErrorIs(t, err, auth.ErrUnauthenticated)
			assert.Equal(t, 0, countKeys(t, svc, "alice"))

			tx, err = b.DB.BeginTx(ctx, nil)
			require.NoError(t, err)

			key, _, err = svc.WithTx(tx).Generate(ctx, "alice", "committed", nil)
			require.NoError(t, err)
			require.NoError(t, tx.Commit())

			_, err = authenticate(t, svc, key)
			require.NoError(t, err)
		},
	)
}

// Inside a transaction, writes are seen by the transaction's reads and
// not by others until it commits; a rollback undoes them all.
//
// Postgres only: it reads outside an open transaction, which needs a
// second connection, and the SQLite test database has one.
func TestWithTx_Isolation(t *testing.T) {
	t.Parallel()

	b := testutil.PostgresBackend(t)
	svc := newService(t, b, nil, nil)
	ctx := t.Context()

	_, seed, err := svc.Generate(ctx, "alice", "seed", nil)
	require.NoError(t, err)

	tx, err := b.DB.BeginTx(ctx, nil)
	require.NoError(t, err)

	txSvc := svc.WithTx(tx)

	_, created, err := txSvc.Generate(ctx, "alice", "new", nil)
	require.NoError(t, err)
	require.NoError(t, txSvc.Revoke(ctx, principalOf(seed.ID)))

	inside, err := txSvc.List(ctx, "alice")
	require.NoError(t, err)
	require.Len(t, inside, 1)
	assert.Equal(t, created.ID, inside[0].ID)

	outside, err := svc.List(ctx, "alice")
	require.NoError(t, err)
	require.Len(t, outside, 1)
	assert.Equal(t, seed.ID, outside[0].ID)

	require.NoError(t, tx.Rollback())

	after, err := svc.List(ctx, "alice")
	require.NoError(t, err)
	require.Len(t, after, 1)
	assert.Equal(t, seed.ID, after[0].ID)
	assert.Nil(t, after[0].DeletedAt)
}

// Generate, Authenticate and Revoke may run concurrently.
func TestService_Concurrent(t *testing.T) {
	t.Parallel()

	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			svc := newService(t, b, nil, nil)
			ctx := t.Context()

			const n = 20

			keys := make([]string, n)
			ids := make([]string, n)

			var wg sync.WaitGroup

			for i := range n {
				wg.Go(func() {
					key, info, err := svc.Generate(ctx, fmt.Sprintf("user-%d", i), "k", nil)
					assert.NoError(t, err)

					keys[i], ids[i] = key, info.ID
				})
			}

			wg.Wait()

			var ok atomic.Int32

			for i := range n {
				wg.Go(func() {
					_, err := authenticate(t, svc, keys[i])
					if err == nil {
						ok.Add(1)
					}
				})
				wg.Go(func() {
					assert.NoError(t, svc.Revoke(ctx, principalOf(ids[i%2])))
				})
			}

			wg.Wait()
			assert.GreaterOrEqual(t, ok.Load(), int32(n-2))

			_, err := authenticate(t, svc, keys[0])
			require.ErrorIs(t, err, auth.ErrCredentialRevoked)
		},
	)
}

// List never presents a revoked key as live: without WithIncludeRevoked it
// is left out, whether revoked by Revoke or RevokeAll, and with it the key
// carries DeletedAt.
func TestList_RevokedNotLive(t *testing.T) {
	t.Parallel()

	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			svc := newService(t, b, nil, nil)
			ctx := t.Context()

			_, one, err := svc.Generate(ctx, "alice", "one", nil)
			require.NoError(t, err)

			_, _, err = svc.Generate(ctx, "alice", "live", nil)
			require.NoError(t, err)
			require.NoError(t, svc.Revoke(ctx, principalOf(one.ID)))

			live, err := svc.List(ctx, "alice")
			require.NoError(t, err)
			require.Len(t, live, 1)
			assert.Equal(t, "live", live[0].Name)
			assert.Nil(t, live[0].DeletedAt)

			require.NoError(t, svc.RevokeAll(ctx, "alice"))

			live, err = svc.List(ctx, "alice")
			require.NoError(t, err)
			assert.Empty(t, live)

			all, err := svc.List(ctx, "alice", apikey.WithIncludeRevoked())
			require.NoError(t, err)
			require.Len(t, all, 2)

			for _, k := range all {
				assert.NotNil(t, k.DeletedAt, k.Name)
			}
		},
	)
}
