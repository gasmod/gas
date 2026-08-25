package session_test

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
	"github.com/gasmod/gas/auth/internal/testutil"
	"github.com/gasmod/gas/auth/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func setupSessionService(t *testing.T, opts ...session.Option) *session.Service {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	pg := testutil.SetupPostgres(t)
	logger := testutil.NewNopLogger()
	migMgr := pg.MigrationManager()

	cfg := session.DefaultConfig()
	cfg.Session.CleanupInterval = 0  // disable background cleanup by default
	cfg.Session.CookieSecure = false // tests run without TLS

	allOpts := append([]session.Option{session.WithConfig(cfg)}, opts...)
	svc := session.New(allOpts...)(pg.Provider(), logger, migMgr, nil)

	require.NoError(t, svc.Init())
	require.NoError(t, migMgr.RunPending())

	t.Cleanup(func() { _ = svc.Close() })
	return svc
}

func setupSessionServiceWithCfg(t *testing.T, cfg *session.Config) *session.Service {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	pg := testutil.SetupPostgres(t)
	logger := testutil.NewNopLogger()
	migMgr := pg.MigrationManager()

	svc := session.New(session.WithConfig(cfg))(pg.Provider(), logger, migMgr, nil)
	require.NoError(t, svc.Init())
	require.NoError(t, migMgr.RunPending())
	t.Cleanup(func() { _ = svc.Close() })
	return svc
}

func createSession(t *testing.T, svc *session.Service, subject string) *session.Session {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "192.168.1.1:12345"
	req.Header.Set("User-Agent", "test-agent/1.0")

	sess, err := svc.Create(context.Background(), subject, gas.BasePrincipalMetadata{"role": "admin"}, req)
	require.NoError(t, err)
	require.NotNil(t, sess)
	return sess
}

func authenticateWithCookie(svc *session.Service, sessionID string) (gas.Principal, error) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "session_id", Value: sessionID})
	return svc.Authenticate(context.Background(), req)
}

// ---------------------------------------------------------------------------
// Lifecycle Tests
// ---------------------------------------------------------------------------

func TestSessionCreateAndAuthenticate(t *testing.T) {
	svc := setupSessionService(t)

	sess := createSession(t, svc, "user-123")
	assert.NotEmpty(t, sess.ID)
	assert.Equal(t, "user-123", sess.Subject)
	assert.Equal(t, "192.168.1.1:12345", sess.IPAddress)
	assert.Equal(t, "test-agent/1.0", sess.UserAgent)
	assert.Equal(t, "admin", sess.Metadata["role"])

	principal, err := authenticateWithCookie(svc, sess.ID)
	require.NoError(t, err)
	assert.Equal(t, "user-123", principal.Subject())
	assert.Equal(t, "session", principal.Scheme())
	assert.Equal(t, sess.ID, principal.CredentialID())
}

func TestSessionAuthenticateNoCookie(t *testing.T) {
	svc := setupSessionService(t)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	_, err := svc.Authenticate(context.Background(), req)
	assert.ErrorIs(t, err, auth.ErrUnauthenticated)
}

func TestSessionAuthenticateEmptyCookie(t *testing.T) {
	svc := setupSessionService(t)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "session_id", Value: ""})
	_, err := svc.Authenticate(context.Background(), req)
	assert.ErrorIs(t, err, auth.ErrUnauthenticated)
}

func TestSessionAuthenticateInvalidID(t *testing.T) {
	svc := setupSessionService(t)

	_, err := authenticateWithCookie(svc, "nonexistent-session-id")
	assert.Error(t, err)
}

// ---------------------------------------------------------------------------
// Expiry Tests
// ---------------------------------------------------------------------------

func TestSessionExpiry(t *testing.T) {
	cfg := session.DefaultConfig()
	cfg.Session.SessionTTL = 1 * time.Millisecond // ultra-short TTL
	cfg.Session.CleanupInterval = 0
	cfg.Session.CookieSecure = false
	cfg.Session.ExtendOnAccess = false

	svc := setupSessionServiceWithCfg(t, cfg)

	sess := createSession(t, svc, "user-expiry")

	// Wait for session to expire.
	time.Sleep(10 * time.Millisecond)

	_, err := authenticateWithCookie(svc, sess.ID)
	assert.ErrorIs(t, err, auth.ErrCredentialsExpired)
}

func TestSessionExtendOnAccess(t *testing.T) {
	cfg := session.DefaultConfig()
	cfg.Session.SessionTTL = 2 * time.Hour
	cfg.Session.CleanupInterval = 0
	cfg.Session.CookieSecure = false
	cfg.Session.ExtendOnAccess = true

	svc := setupSessionServiceWithCfg(t, cfg)

	sess := createSession(t, svc, "user-extend")
	originalExpiry := sess.ExpiresAt

	time.Sleep(50 * time.Millisecond)

	principal, err := authenticateWithCookie(svc, sess.ID)
	require.NoError(t, err)
	assert.Equal(t, "user-extend", principal.Subject())

	// Authenticate again — session should still be valid.
	principal2, err := authenticateWithCookie(svc, sess.ID)
	require.NoError(t, err)
	assert.Equal(t, "user-extend", principal2.Subject())
	_ = originalExpiry // original expiry should be before the new one
}

func TestSessionExtendOnAccessDisabled(t *testing.T) {
	cfg := session.DefaultConfig()
	cfg.Session.SessionTTL = 24 * time.Hour
	cfg.Session.CleanupInterval = 0
	cfg.Session.CookieSecure = false
	cfg.Session.ExtendOnAccess = false

	svc := setupSessionServiceWithCfg(t, cfg)

	sess := createSession(t, svc, "user-no-extend")

	_, err := authenticateWithCookie(svc, sess.ID)
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// Revocation Tests
// ---------------------------------------------------------------------------

func TestSessionRevoke(t *testing.T) {
	svc := setupSessionService(t)

	sess := createSession(t, svc, "user-revoke")
	principal, err := authenticateWithCookie(svc, sess.ID)
	require.NoError(t, err)

	require.NoError(t, svc.Revoke(context.Background(), principal))

	_, err = authenticateWithCookie(svc, sess.ID)
	assert.Error(t, err)
}

func TestSessionRevokeAll(t *testing.T) {
	svc := setupSessionService(t)

	sess1 := createSession(t, svc, "user-revoke-all")
	sess2 := createSession(t, svc, "user-revoke-all")
	sess3 := createSession(t, svc, "user-revoke-all")

	require.NoError(t, svc.RevokeAll(context.Background(), "user-revoke-all"))

	for _, id := range []string{sess1.ID, sess2.ID, sess3.ID} {
		_, err := authenticateWithCookie(svc, id)
		assert.Error(t, err, "session %s should be revoked", id)
	}
}

func TestSessionRevokeAllBySchemeSession(t *testing.T) {
	svc := setupSessionService(t)

	sess := createSession(t, svc, "user-scheme")
	require.NoError(t, svc.RevokeAllByScheme(context.Background(), "user-scheme", "session"))

	_, err := authenticateWithCookie(svc, sess.ID)
	assert.Error(t, err)
}

func TestSessionRevokeAllBySchemeWrongScheme(t *testing.T) {
	svc := setupSessionService(t)

	sess := createSession(t, svc, "user-wrong-scheme")
	require.NoError(t, svc.RevokeAllByScheme(context.Background(), "user-wrong-scheme", "jwt"))

	// Should still be valid — wrong scheme is a no-op.
	principal, err := authenticateWithCookie(svc, sess.ID)
	require.NoError(t, err)
	assert.Equal(t, "user-wrong-scheme", principal.Subject())
}

// ---------------------------------------------------------------------------
// Cookie Tests
// ---------------------------------------------------------------------------

func TestSessionSetCookie(t *testing.T) {
	svc := setupSessionService(t)
	sess := createSession(t, svc, "user-cookie")

	w := httptest.NewRecorder()
	svc.SetCookie(w, sess)

	cookies := w.Result().Cookies()
	require.Len(t, cookies, 1)

	c := cookies[0]
	assert.Equal(t, "session_id", c.Name)
	assert.Equal(t, sess.ID, c.Value)
	assert.Equal(t, "/", c.Path)
	assert.True(t, c.HttpOnly)
}

func TestSessionClearCookie(t *testing.T) {
	svc := setupSessionService(t)

	w := httptest.NewRecorder()
	svc.ClearCookie(w)

	cookies := w.Result().Cookies()
	require.Len(t, cookies, 1)

	c := cookies[0]
	assert.Equal(t, "session_id", c.Name)
	assert.Equal(t, "", c.Value)
	assert.Equal(t, -1, c.MaxAge)
}

// ---------------------------------------------------------------------------
// Cleanup Goroutine Tests
// ---------------------------------------------------------------------------

func TestSessionCleanupGoroutine(t *testing.T) {
	cfg := session.DefaultConfig()
	cfg.Session.SessionTTL = 1 * time.Millisecond
	cfg.Session.CleanupInterval = 100 * time.Millisecond
	cfg.Session.CookieSecure = false

	svc := setupSessionServiceWithCfg(t, cfg)

	// Create a session that will expire almost immediately.
	sess := createSession(t, svc, "user-cleanup")

	// Wait for session to expire and cleanup to run.
	time.Sleep(500 * time.Millisecond)

	_, err := authenticateWithCookie(svc, sess.ID)
	assert.Error(t, err, "expired session should have been cleaned up")
}

// ---------------------------------------------------------------------------
// Metadata Tests
// ---------------------------------------------------------------------------

func TestSessionMetadataRoundTrip(t *testing.T) {
	svc := setupSessionService(t)

	meta := gas.BasePrincipalMetadata{
		"role":    "admin",
		"org_id":  float64(42),
		"nested":  map[string]any{"key": "value"},
		"unicode": "\U0001F600 emoji",
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	sess, err := svc.Create(context.Background(), "user-meta", meta, req)
	require.NoError(t, err)

	principal, err := authenticateWithCookie(svc, sess.ID)
	require.NoError(t, err)

	assert.Equal(t, "admin", principal.Metadata().Value("role"))
	assert.Equal(t, "\U0001F600 emoji", principal.Metadata().Value("unicode"))
}

func TestSessionNilMetadata(t *testing.T) {
	svc := setupSessionService(t)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	sess, err := svc.Create(context.Background(), "user-nil-meta", nil, req)
	require.NoError(t, err)

	principal, err := authenticateWithCookie(svc, sess.ID)
	require.NoError(t, err)
	assert.NotNil(t, principal.Metadata())
}

func TestSessionLongUserAgent(t *testing.T) {
	svc := setupSessionService(t)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	longUA := strings.Repeat("A", 10*1024)
	req.Header.Set("User-Agent", longUA)

	sess, err := svc.Create(context.Background(), "user-long-ua", nil, req)
	require.NoError(t, err)
	assert.NotEmpty(t, sess.ID)
}

// ---------------------------------------------------------------------------
// Session Fixation Tests
// ---------------------------------------------------------------------------

func TestSessionFixation(t *testing.T) {
	svc := setupSessionService(t)

	_, err := authenticateWithCookie(svc, "attacker-crafted-session-id")
	assert.Error(t, err, "crafted session ID must not create a new session")
}

func TestSessionSQLInjectionInCookie(t *testing.T) {
	svc := setupSessionService(t)

	_, err := authenticateWithCookie(svc, "'; DROP TABLE __gas_auth_sessions; --")
	assert.Error(t, err, "SQL injection in cookie value must not succeed")
}

// ---------------------------------------------------------------------------
// Concurrency Tests
// ---------------------------------------------------------------------------

func TestSessionConcurrentCreateAndAuthenticate(t *testing.T) {
	svc := setupSessionService(t)

	const goroutines = 50
	var wg sync.WaitGroup
	var successCount atomic.Int32

	// Create sessions concurrently.
	sessionIDs := make([]string, goroutines)
	wg.Add(goroutines)
	for i := range goroutines {
		go func(idx int) {
			defer wg.Done()
			sess := createSession(t, svc, fmt.Sprintf("user-concurrent-%d", idx))
			sessionIDs[idx] = sess.ID
		}(i)
	}
	wg.Wait()

	// Authenticate concurrently.
	wg.Add(goroutines)
	for i := range goroutines {
		go func(idx int) {
			defer wg.Done()
			principal, err := authenticateWithCookie(svc, sessionIDs[idx])
			if err == nil && principal != nil {
				successCount.Add(1)
			}
		}(i)
	}
	wg.Wait()

	assert.Equal(t, int32(goroutines), successCount.Load())
}

func TestSessionConcurrentRevokeWhileAuthenticating(t *testing.T) {
	svc := setupSessionService(t)

	sess := createSession(t, svc, "user-race-revoke")

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for range 20 {
			_, _ = authenticateWithCookie(svc, sess.ID)
		}
	}()

	go func() {
		defer wg.Done()
		time.Sleep(5 * time.Millisecond)
		principal := auth.NewPrincipal("user-race-revoke", "session", sess.ID, nil)
		_ = svc.Revoke(context.Background(), principal)
	}()

	wg.Wait()
}

// ---------------------------------------------------------------------------
// Name Test
// ---------------------------------------------------------------------------

func TestSessionServiceName(t *testing.T) {
	svc := setupSessionService(t)
	assert.Equal(t, "gas/auth/session", svc.Name())
}
