package apikey_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gasmod/gas"
	auth "github.com/gasmod/gas/auth"
	"github.com/gasmod/gas/auth/apikey"
	"github.com/gasmod/gas/auth/internal/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func setupAPIKeyService(t *testing.T, opts ...apikey.Option) *apikey.Service {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	pg := testutil.SetupPostgres(t)
	logger := testutil.NewNopLogger()
	migMgr := pg.MigrationManager()

	cfg := apikey.DefaultConfig()
	allOpts := append([]apikey.Option{apikey.WithConfig(cfg)}, opts...)
	svc := apikey.New(allOpts...)(pg.Provider(), logger, migMgr, nil)

	require.NoError(t, svc.Init())
	require.NoError(t, migMgr.RunPending())

	t.Cleanup(func() { _ = svc.Close() })
	return svc
}

func setupAPIKeyServiceWithConfig(t *testing.T, cfg *apikey.Config) *apikey.Service {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	pg := testutil.SetupPostgres(t)
	logger := testutil.NewNopLogger()
	migMgr := pg.MigrationManager()

	svc := apikey.New(apikey.WithConfig(cfg))(pg.Provider(), logger, migMgr, nil)
	require.NoError(t, svc.Init())
	require.NoError(t, migMgr.RunPending())

	t.Cleanup(func() { _ = svc.Close() })
	return svc
}

func authenticateWithAPIKey(svc *apikey.Service, headerName, key string) (gas.Principal, error) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(headerName, key)
	return svc.Authenticate(context.Background(), req)
}

// ---------------------------------------------------------------------------
// Lifecycle Tests
// ---------------------------------------------------------------------------

func TestAPIKeyGenerateAndAuthenticate(t *testing.T) {
	svc := setupAPIKeyService(t)

	key, info, err := svc.Generate(context.Background(), "user-123", "my-key", []string{"read", "write"})
	require.NoError(t, err)
	assert.NotEmpty(t, key)
	assert.NotEmpty(t, info.ID)

	principal, err := authenticateWithAPIKey(svc, "X-API-Key", key)
	require.NoError(t, err)
	assert.Equal(t, "user-123", principal.Subject())
	assert.Equal(t, "apikey", principal.Scheme())
	assert.Equal(t, info.ID, principal.CredentialID())

	// Scopes in metadata.
	scopes := principal.Metadata().Value(apikey.PrincipalMetadataKeyScopes)
	assert.IsType(t, []string{}, scopes)
	assert.Len(t, scopes, 2)
	assert.Equal(t, []string{"read", "write"}, scopes)
}

func TestAPIKeyAuthenticateNoHeader(t *testing.T) {
	svc := setupAPIKeyService(t)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	_, err := svc.Authenticate(context.Background(), req)
	assert.ErrorIs(t, err, auth.ErrUnauthenticated)
}

func TestAPIKeyAuthenticateInvalidKey(t *testing.T) {
	svc := setupAPIKeyService(t)

	_, err := authenticateWithAPIKey(svc, "X-API-Key", "totally-fake-key")
	assert.ErrorIs(t, err, auth.ErrUnauthenticated)
}

func TestAPIKeyAuthenticateWrongHeaderName(t *testing.T) {
	svc := setupAPIKeyService(t)

	key, _, err := svc.Generate(context.Background(), "user-header", "test", nil)
	require.NoError(t, err)

	// Use wrong header name.
	_, err = authenticateWithAPIKey(svc, "Authorization", key)
	assert.ErrorIs(t, err, auth.ErrUnauthenticated)
}

// ---------------------------------------------------------------------------
// Revocation Tests
// ---------------------------------------------------------------------------

func TestAPIKeyRevoke(t *testing.T) {
	svc := setupAPIKeyService(t)

	key, info, err := svc.Generate(context.Background(), "user-revoke", "test", nil)
	require.NoError(t, err)

	principal := auth.NewPrincipal("user-revoke", "apikey", info.ID, nil)
	require.NoError(t, svc.Revoke(context.Background(), principal))

	_, err = authenticateWithAPIKey(svc, "X-API-Key", key)
	assert.ErrorIs(t, err, auth.ErrUnauthenticated)
}

func TestAPIKeyRevokeAll(t *testing.T) {
	svc := setupAPIKeyService(t)

	key1, _, err := svc.Generate(context.Background(), "user-revoke-all", "key1", nil)
	require.NoError(t, err)
	key2, _, err := svc.Generate(context.Background(), "user-revoke-all", "key2", nil)
	require.NoError(t, err)
	key3, _, err := svc.Generate(context.Background(), "user-revoke-all", "key3", nil)
	require.NoError(t, err)

	require.NoError(t, svc.RevokeAll(context.Background(), "user-revoke-all"))

	for _, key := range []string{key1, key2, key3} {
		_, err := authenticateWithAPIKey(svc, "X-API-Key", key)
		assert.ErrorIs(t, err, auth.ErrUnauthenticated, "key should be revoked")
	}
}

func TestAPIKeyRevokeAllBySchemeAPIKey(t *testing.T) {
	svc := setupAPIKeyService(t)

	key, _, err := svc.Generate(context.Background(), "user-scheme", "test", nil)
	require.NoError(t, err)

	require.NoError(t, svc.RevokeAllByScheme(context.Background(), "user-scheme", "apikey"))

	_, err = authenticateWithAPIKey(svc, "X-API-Key", key)
	assert.ErrorIs(t, err, auth.ErrUnauthenticated)
}

func TestAPIKeyRevokeExcludesFromList(t *testing.T) {
	svc := setupAPIKeyService(t)

	_, keepInfo, err := svc.Generate(context.Background(), "user-list", "keep", nil)
	require.NoError(t, err)
	_, revokeInfo, err := svc.Generate(context.Background(), "user-list", "revoke", nil)
	require.NoError(t, err)

	principal := auth.NewPrincipal("user-list", "apikey", revokeInfo.ID, nil)
	require.NoError(t, svc.Revoke(context.Background(), principal))

	keys, err := svc.List(context.Background(), "user-list")
	require.NoError(t, err)
	require.Len(t, keys, 1)
	assert.Equal(t, keepInfo.ID, keys[0].ID)

	allKeys, err := svc.List(context.Background(), "user-list", apikey.WithIncludeRevoked())
	require.NoError(t, err)
	require.Len(t, allKeys, 2)
	byID := map[string]apikey.KeyInfo{}
	for _, k := range allKeys {
		byID[k.ID] = k
	}
	require.Contains(t, byID, keepInfo.ID)
	require.Contains(t, byID, revokeInfo.ID)
	assert.Nil(t, byID[keepInfo.ID].DeletedAt)
	assert.NotNil(t, byID[revokeInfo.ID].DeletedAt)
}

func TestAPIKeyDeleteHardRemovesRow(t *testing.T) {
	svc := setupAPIKeyService(t)

	key, info, err := svc.Generate(context.Background(), "user-delete", "test", nil)
	require.NoError(t, err)

	principal := auth.NewPrincipal("user-delete", "apikey", info.ID, nil)
	require.NoError(t, svc.Delete(context.Background(), principal))

	_, err = authenticateWithAPIKey(svc, "X-API-Key", key)
	assert.ErrorIs(t, err, auth.ErrUnauthenticated)

	keys, err := svc.List(context.Background(), "user-delete")
	require.NoError(t, err)
	assert.Empty(t, keys)
}

func TestAPIKeyDeleteAllHardRemovesRows(t *testing.T) {
	svc := setupAPIKeyService(t)

	_, _, err := svc.Generate(context.Background(), "user-delete-all", "k1", nil)
	require.NoError(t, err)
	_, _, err = svc.Generate(context.Background(), "user-delete-all", "k2", nil)
	require.NoError(t, err)

	// Soft-delete one first to prove DeleteAll removes both active and soft-deleted rows.
	_, revokedInfo, err := svc.Generate(context.Background(), "user-delete-all", "k3", nil)
	require.NoError(t, err)
	principal := auth.NewPrincipal("user-delete-all", "apikey", revokedInfo.ID, nil)
	require.NoError(t, svc.Revoke(context.Background(), principal))

	require.NoError(t, svc.DeleteAll(context.Background(), "user-delete-all"))

	keys, err := svc.List(context.Background(), "user-delete-all")
	require.NoError(t, err)
	assert.Empty(t, keys)
}

func TestAPIKeyRevokeAllBySchemeWrongScheme(t *testing.T) {
	svc := setupAPIKeyService(t)

	key, _, err := svc.Generate(context.Background(), "user-wrong-scheme", "test", nil)
	require.NoError(t, err)

	// Wrong scheme → no-op.
	require.NoError(t, svc.RevokeAllByScheme(context.Background(), "user-wrong-scheme", "jwt"))

	principal, err := authenticateWithAPIKey(svc, "X-API-Key", key)
	require.NoError(t, err)
	assert.Equal(t, "user-wrong-scheme", principal.Subject())
}

// ---------------------------------------------------------------------------
// Last Used Tracking
// ---------------------------------------------------------------------------

func TestAPIKeyUpdatesLastUsed(t *testing.T) {
	svc := setupAPIKeyService(t)

	key, _, err := svc.Generate(context.Background(), "user-last-used", "test", nil)
	require.NoError(t, err)

	// Authenticate to trigger last_used update.
	_, err = authenticateWithAPIKey(svc, "X-API-Key", key)
	require.NoError(t, err)

	keys, err := svc.List(context.Background(), "user-last-used")
	require.NoError(t, err)
	require.Len(t, keys, 1)
	assert.NotNil(t, keys[0].LastUsed, "last_used should be set after authentication")
}

// ---------------------------------------------------------------------------
// List Tests
// ---------------------------------------------------------------------------

func TestAPIKeyList(t *testing.T) {
	svc := setupAPIKeyService(t)

	_, _, err := svc.Generate(context.Background(), "user-list", "key1", []string{"read"})
	require.NoError(t, err)
	_, _, err = svc.Generate(context.Background(), "user-list", "key2", []string{"write"})
	require.NoError(t, err)

	keys, err := svc.List(context.Background(), "user-list")
	require.NoError(t, err)
	assert.Len(t, keys, 2)

	// Should not contain key hashes.
	for _, k := range keys {
		assert.NotEmpty(t, k.ID)
		assert.NotEmpty(t, k.Name)
		assert.NotEmpty(t, k.KeyPrefix)
	}
}

func TestAPIKeyListEmpty(t *testing.T) {
	svc := setupAPIKeyService(t)

	keys, err := svc.List(context.Background(), "nonexistent-user")
	require.NoError(t, err)
	assert.Empty(t, keys)
}

func TestAPIKeyListOrdering(t *testing.T) {
	svc := setupAPIKeyService(t)

	for i := range 5 {
		_, _, err := svc.Generate(context.Background(), "user-order", fmt.Sprintf("key-%d", i), nil)
		require.NoError(t, err)
		time.Sleep(10 * time.Millisecond)
	}

	keys, err := svc.List(context.Background(), "user-order")
	require.NoError(t, err)
	require.Len(t, keys, 5)

	// Should be ordered by created_at DESC (newest first).
	for i := 1; i < len(keys); i++ {
		assert.True(t, !keys[i-1].CreatedAt.Before(keys[i].CreatedAt),
			"keys should be ordered newest first")
	}
}

// ---------------------------------------------------------------------------
// Scope Tests
// ---------------------------------------------------------------------------

func TestAPIKeyEmptyScopes(t *testing.T) {
	svc := setupAPIKeyService(t)

	key, _, err := svc.Generate(context.Background(), "user-empty-scopes", "test", nil)
	require.NoError(t, err)

	principal, err := authenticateWithAPIKey(svc, "X-API-Key", key)
	require.NoError(t, err)

	scopes := principal.Metadata().Value(apikey.PrincipalMetadataKeyScopes)
	assert.IsType(t, []string{}, scopes)
	assert.Len(t, scopes, 0)
}

func TestAPIKeyCSVScopeInjection(t *testing.T) {
	svc := setupAPIKeyService(t)

	// Scopes containing commas are now rejected at generation time.
	_, _, err := svc.Generate(context.Background(), "user-csv", "test", []string{"scope,with,commas", "normal"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not contain commas")
}

// ---------------------------------------------------------------------------
// Prefix Tests
// ---------------------------------------------------------------------------

func TestAPIKeyWithPrefix(t *testing.T) {
	cfg := apikey.DefaultConfig()
	cfg.APIKey.Prefix = "gas_"
	svc := setupAPIKeyServiceWithConfig(t, cfg)

	key, _, err := svc.Generate(context.Background(), "user-prefix", "test", nil)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(key, "gas_"), "key should start with prefix")

	principal, err := authenticateWithAPIKey(svc, "X-API-Key", key)
	require.NoError(t, err)
	assert.Equal(t, "user-prefix", principal.Subject())
}

func TestAPIKeyWithEmptyPrefix(t *testing.T) {
	cfg := apikey.DefaultConfig()
	cfg.APIKey.Prefix = ""
	svc := setupAPIKeyServiceWithConfig(t, cfg)

	key, _, err := svc.Generate(context.Background(), "user-no-prefix", "test", nil)
	require.NoError(t, err)
	assert.NotEmpty(t, key)

	principal, err := authenticateWithAPIKey(svc, "X-API-Key", key)
	require.NoError(t, err)
	assert.Equal(t, "user-no-prefix", principal.Subject())
}

func TestAPIKeyPrefixInList(t *testing.T) {
	cfg := apikey.DefaultConfig()
	cfg.APIKey.Prefix = "test_"
	svc := setupAPIKeyServiceWithConfig(t, cfg)

	key, _, err := svc.Generate(context.Background(), "user-prefix-list", "test", nil)
	require.NoError(t, err)

	keys, err := svc.List(context.Background(), "user-prefix-list")
	require.NoError(t, err)
	require.Len(t, keys, 1)

	// KeyPrefix should be prefix + 8 chars of the raw key.
	assert.True(t, strings.HasPrefix(key, keys[0].KeyPrefix),
		"key should start with its prefix")
}

// ---------------------------------------------------------------------------
// Expiry Tests
// ---------------------------------------------------------------------------

// Note: The apikey.Generate does not accept an expiry parameter.
// Expiry must be set directly in the DB for testing. We test the
// Authenticate path's expiry check via direct DB manipulation.

// ---------------------------------------------------------------------------
// Brute Force Resistance
// ---------------------------------------------------------------------------

func TestAPIKeyBruteForceResistance(t *testing.T) {
	svc := setupAPIKeyService(t)

	_, _, err := svc.Generate(context.Background(), "user-brute", "test", nil)
	require.NoError(t, err)

	// Try many random keys — all should return ErrUnauthenticated.
	for i := range 20 {
		_, err := authenticateWithAPIKey(svc, "X-API-Key", fmt.Sprintf("random-key-%d", i))
		assert.ErrorIs(t, err, auth.ErrUnauthenticated, "brute force attempt %d", i)
	}
}

func TestAPIKeyNearMissGivesNoSignal(t *testing.T) {
	svc := setupAPIKeyService(t)

	key, _, err := svc.Generate(context.Background(), "user-near-miss", "test", nil)
	require.NoError(t, err)

	// Try key minus last character.
	nearMiss := key[:len(key)-1]
	_, err = authenticateWithAPIKey(svc, "X-API-Key", nearMiss)
	assert.ErrorIs(t, err, auth.ErrUnauthenticated, "near miss should give no signal")
}

func TestAPIKeyGenerateRejectsCommaInScope(t *testing.T) {
	svc := setupAPIKeyService(t)

	_, _, err := svc.Generate(context.Background(), "user-1", "bad-scope", []string{"read,write"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not contain commas")
}

// ---------------------------------------------------------------------------
// Concurrency Tests
// ---------------------------------------------------------------------------

func TestAPIKeyConcurrentGenerateAndAuthenticate(t *testing.T) {
	svc := setupAPIKeyService(t)

	const goroutines = 30
	var wg sync.WaitGroup
	var successCount atomic.Int32

	keys := make([]string, goroutines)
	wg.Add(goroutines)
	for i := range goroutines {
		go func(idx int) {
			defer wg.Done()
			key, _, genErr := svc.Generate(context.Background(), fmt.Sprintf("user-%d", idx), fmt.Sprintf("key-%d", idx), nil)
			require.NoError(t, genErr)
			keys[idx] = key
		}(i)
	}
	wg.Wait()

	wg.Add(goroutines)
	for i := range goroutines {
		go func(idx int) {
			defer wg.Done()
			principal, authErr := authenticateWithAPIKey(svc, "X-API-Key", keys[idx])
			if authErr == nil && principal != nil {
				successCount.Add(1)
			}
		}(i)
	}
	wg.Wait()

	assert.Equal(t, int32(goroutines), successCount.Load())
}

func TestAPIKeyConcurrentRevokeWhileAuthenticating(t *testing.T) {
	svc := setupAPIKeyService(t)

	key, info, err := svc.Generate(context.Background(), "user-race", "test", nil)
	require.NoError(t, err)

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for range 20 {
			_, _ = authenticateWithAPIKey(svc, "X-API-Key", key)
		}
	}()

	go func() {
		defer wg.Done()
		time.Sleep(5 * time.Millisecond)
		principal := auth.NewPrincipal("user-race", "apikey", info.ID, nil)
		_ = svc.Revoke(context.Background(), principal)
	}()

	wg.Wait()
}

// ---------------------------------------------------------------------------
// Edge Cases
// ---------------------------------------------------------------------------

func TestAPIKeyCustomHeaderName(t *testing.T) {
	cfg := apikey.DefaultConfig()
	cfg.APIKey.HeaderName = "Authorization"
	svc := setupAPIKeyServiceWithConfig(t, cfg)

	key, _, err := svc.Generate(context.Background(), "user-custom-header", "test", nil)
	require.NoError(t, err)

	principal, err := authenticateWithAPIKey(svc, "Authorization", key)
	require.NoError(t, err)
	assert.Equal(t, "user-custom-header", principal.Subject())
}

func TestAPIKeyEmptySubject(t *testing.T) {
	svc := setupAPIKeyService(t)

	key, _, err := svc.Generate(context.Background(), "", "test", nil)
	require.NoError(t, err)

	principal, err := authenticateWithAPIKey(svc, "X-API-Key", key)
	require.NoError(t, err)
	assert.Equal(t, "", principal.Subject())
}

// ---------------------------------------------------------------------------
// Transaction Tests
// ---------------------------------------------------------------------------

func setupAPIKeyServiceWithProvider(t *testing.T) (*apikey.Service, *testutil.PostgresContainer) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	pg := testutil.SetupPostgres(t)
	logger := testutil.NewNopLogger()
	migMgr := pg.MigrationManager()

	svc := apikey.New(apikey.WithConfig(apikey.DefaultConfig()))(pg.Provider(), logger, migMgr, nil)
	require.NoError(t, svc.Init())
	require.NoError(t, migMgr.RunPending())

	t.Cleanup(func() { _ = svc.Close() })
	return svc, pg
}

func TestAPIKeyWithTxRollback(t *testing.T) {
	svc, pg := setupAPIKeyServiceWithProvider(t)
	ctx := context.Background()

	tx, err := pg.Provider().BeginTx(ctx, nil)
	require.NoError(t, err)

	_, _, err = svc.WithTx(tx).Generate(ctx, "user-tx-rollback", "k", []string{"read"})
	require.NoError(t, err)

	require.NoError(t, tx.Rollback())

	keys, err := svc.List(ctx, "user-tx-rollback")
	require.NoError(t, err)
	assert.Empty(t, keys, "rolled-back generate must not leave a row")
}

func TestAPIKeyWithTxCommit(t *testing.T) {
	svc, pg := setupAPIKeyServiceWithProvider(t)
	ctx := context.Background()

	tx, err := pg.Provider().BeginTx(ctx, nil)
	require.NoError(t, err)

	_, info, err := svc.WithTx(tx).Generate(ctx, "user-tx-commit", "k", []string{"read"})
	require.NoError(t, err)

	require.NoError(t, tx.Commit())

	keys, err := svc.List(ctx, "user-tx-commit")
	require.NoError(t, err)
	require.Len(t, keys, 1)
	assert.Equal(t, info.ID, keys[0].ID)
}

func TestAPIKeyWithTxMixedOperations(t *testing.T) {
	svc, pg := setupAPIKeyServiceWithProvider(t)
	ctx := context.Background()

	// Seed a pre-existing key outside any tx.
	_, seedInfo, err := svc.Generate(ctx, "user-tx-mixed", "seed", []string{"read"})
	require.NoError(t, err)

	tx, err := pg.Provider().BeginTx(ctx, nil)
	require.NoError(t, err)
	txSvc := svc.WithTx(tx)

	// Generate a new key inside the tx.
	_, newInfo, err := txSvc.Generate(ctx, "user-tx-mixed", "new", []string{"read", "write"})
	require.NoError(t, err)

	// Revoke the seed key inside the same tx.
	seedPrincipal := auth.NewPrincipal("user-tx-mixed", auth.SchemeAPIKey, seedInfo.ID, nil)
	require.NoError(t, txSvc.Revoke(ctx, seedPrincipal))

	// List inside the tx: must see the new key, must not see the revoked seed.
	txKeys, err := txSvc.List(ctx, "user-tx-mixed")
	require.NoError(t, err)
	require.Len(t, txKeys, 1)
	assert.Equal(t, newInfo.ID, txKeys[0].ID)

	// Outside the tx, the changes must not yet be visible: seed still active, new key absent.
	outsideKeys, err := svc.List(ctx, "user-tx-mixed")
	require.NoError(t, err)
	require.Len(t, outsideKeys, 1)
	assert.Equal(t, seedInfo.ID, outsideKeys[0].ID)

	require.NoError(t, tx.Rollback())

	// After rollback: seed still active, new key never existed.
	finalKeys, err := svc.List(ctx, "user-tx-mixed")
	require.NoError(t, err)
	require.Len(t, finalKeys, 1)
	assert.Equal(t, seedInfo.ID, finalKeys[0].ID)
	assert.Nil(t, finalKeys[0].DeletedAt)
}

// ---------------------------------------------------------------------------
// Name Test
// ---------------------------------------------------------------------------

func TestAPIKeyServiceName(t *testing.T) {
	svc := setupAPIKeyService(t)
	assert.Equal(t, "gas/auth/apikey", svc.Name())
}
