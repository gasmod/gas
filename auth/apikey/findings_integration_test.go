package apikey_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gasmod/gas/auth/apikey"
	"github.com/gasmod/gas/auth/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Finding #6: API key Authenticate fails hard on last_used update failure
// ---------------------------------------------------------------------------

// TestAPIKeyLastUsedUpdateIsCoupledToAuth demonstrates that the last_used
// update is on the critical path of authentication. If the UPDATE fails,
// the entire Authenticate call fails — even though the key was already
// validated.
func TestAPIKeyLastUsedUpdateIsCoupledToAuth(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	svc := setupAPIKeyService(t)

	key, _, err := svc.Generate(context.Background(), "user-1", "test-key", nil)
	require.NoError(t, err)

	// First auth succeeds — this also updates last_used.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-API-Key", key)
	principal, err := svc.Authenticate(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, "user-1", principal.Subject())

	// Verify that last_used was set by listing keys.
	keys, err := svc.List(context.Background(), "user-1")
	require.NoError(t, err)
	require.Len(t, keys, 1)
	assert.NotNil(t, keys[0].LastUsed, "last_used is set after authentication")

	// We can't easily simulate a DB write failure here without mocking,
	// but this test documents that UpdateLastUsed is in the critical auth path.
	// If it were best-effort (log and continue), a transient DB write failure
	// would not reject an otherwise valid request.
}

// ---------------------------------------------------------------------------
// Finding #7: No expiry cleanup for API keys
// ---------------------------------------------------------------------------

// TestAPIKeyExpiredKeysAccumulate demonstrates that expired API keys remain
// in the database indefinitely — there is no cleanup goroutine like in
// session and token services.
func TestAPIKeyExpiredKeysAccumulate(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	pg := testutil.SetupPostgres(t)
	logger := testutil.NewNopLogger()
	migMgr := pg.MigrationManager()

	cfg := apikey.DefaultConfig()
	svc := apikey.New(apikey.WithConfig(cfg))(pg.Provider(), logger, migMgr, nil)
	require.NoError(t, svc.Init())
	require.NoError(t, migMgr.RunPending())
	t.Cleanup(func() { _ = svc.Close() })

	db := pg.Provider().DB()

	// Generate a key.
	_, info, err := svc.Generate(context.Background(), "user-exp", "expiring-key", nil)
	require.NoError(t, err)

	// Manually set its expiry to the past (simulating an expired key).
	pastExpiry := time.Now().Add(-1 * time.Hour)
	_, err = db.Exec("UPDATE __gas_auth_api_keys SET expires_at = $1 WHERE id = $2", pastExpiry, info.ID)
	require.NoError(t, err)

	// The key is expired and cannot be used for auth.
	// But it still exists in the database.
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM __gas_auth_api_keys WHERE id = $1", info.ID).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count, "expired key remains in database")

	// It also still appears in List.
	keys, err := svc.List(context.Background(), "user-exp")
	require.NoError(t, err)
	assert.Len(t, keys, 1, "expired key still shows up in List")

	// Wait to confirm no cleanup happens (unlike session/token services).
	time.Sleep(200 * time.Millisecond)

	err = db.QueryRow("SELECT COUNT(*) FROM __gas_auth_api_keys WHERE id = $1", info.ID).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count,
		"expired key persists indefinitely — no cleanup goroutine exists for API keys "+
			"(session and token services have background cleanup)")
}

// ---------------------------------------------------------------------------
// Finding #11: Scopes stored as comma-separated strings (MySQL/SQLite)
// ---------------------------------------------------------------------------

// TestAPIKeyScopeQueryingLimitation demonstrates that scopes stored as
// comma-separated TEXT cannot be queried at the database level.
func TestAPIKeyScopeQueryingLimitation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	pg := testutil.SetupPostgres(t)
	logger := testutil.NewNopLogger()
	migMgr := pg.MigrationManager()

	cfg := apikey.DefaultConfig()
	svc := apikey.New(apikey.WithConfig(cfg))(pg.Provider(), logger, migMgr, nil)
	require.NoError(t, svc.Init())
	require.NoError(t, migMgr.RunPending())
	t.Cleanup(func() { _ = svc.Close() })

	// Generate keys with different scopes.
	_, _, err := svc.Generate(context.Background(), "user-scope", "read-key", []string{"read"})
	require.NoError(t, err)
	_, _, err = svc.Generate(context.Background(), "user-scope", "write-key", []string{"read", "write"})
	require.NoError(t, err)
	_, _, err = svc.Generate(context.Background(), "user-scope", "admin-key", []string{"read", "write", "admin"})
	require.NoError(t, err)

	// PostgreSQL can query scopes using array operators.
	db := pg.Provider().DB()
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM __gas_auth_api_keys WHERE 'write' = ANY(scopes) AND subject = $1",
		"user-scope").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 2, count,
		"PostgreSQL can query by individual scope using ANY() on TEXT[] column")

	// MySQL/SQLite store scopes as comma-separated TEXT ('read,write').
	// They would need LIKE '%write%' which is imprecise (matches 'readwrite' too)
	// or application-level filtering after loading all keys.
	// This test documents that PostgreSQL's native array is superior for this use case.
}

// ---------------------------------------------------------------------------
// Finding: API key service has no Close cleanup (stateless)
// Contrast with session and token which stop goroutines on Close.
// ---------------------------------------------------------------------------

func TestAPIKeyCloseIsNoOp(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	svc := setupAPIKeyService(t)

	// Close is a no-op — no goroutines to stop, no resources to release.
	err := svc.Close()
	assert.NoError(t, err)

	// Service can still be used after Close (it's truly stateless).
	// This is correct behavior but means expired keys are never cleaned up.
	_, _, err = svc.Generate(context.Background(), "user-after-close", "key", nil)
	assert.NoError(t, err, "service still works after Close — no cleanup goroutine to stop")
}
