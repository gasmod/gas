package session_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	auth "github.com/gasmod/gas/auth"
	"github.com/gasmod/gas/auth/internal/testutil"
	"github.com/gasmod/gas/auth/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
)

// ---------------------------------------------------------------------------
// Finding #2: SQLite time parsing silently returns zero time on error
// ---------------------------------------------------------------------------

// TestSQLiteTimeParsing_MalformedTimestamp demonstrates that a session stored
// with a non-standard timestamp format in SQLite results in a zero time, which
// causes the session to appear expired.
func TestSQLiteTimeParsing_MalformedTimestamp(t *testing.T) {
	rawDB, provider, migMgr := testutil.SetupSQLite(t)
	logger := testutil.NewNopLogger()

	cfg := session.DefaultConfig()
	cfg.Session.CleanupInterval = 0
	cfg.Session.CookieSecure = false

	svc := session.New(session.WithConfig(cfg))(provider, logger, migMgr, nil)
	require.NoError(t, svc.Init())
	require.NoError(t, migMgr.RunPending())
	t.Cleanup(func() { _ = svc.Close() })

	// Insert a session with a valid timestamp format.
	validExpiry := time.Now().Add(24 * time.Hour).UTC().Format("2006-01-02 15:04:05")
	validCreated := time.Now().UTC().Format("2006-01-02 15:04:05")
	_, err := rawDB.Exec(`
		INSERT INTO __gas_auth_sessions (id, subject, metadata, ip_address, user_agent, created_at, expires_at, last_active)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"valid-sess", "user-1", "{}", "127.0.0.1", "test", validCreated, validExpiry, validCreated)
	require.NoError(t, err)

	// This session should authenticate successfully.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "session_id", Value: "valid-sess"})
	principal, err := svc.Authenticate(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, "user-1", principal.Subject())

	// Now insert a session with a MALFORMED timestamp (e.g., RFC3339 with timezone).
	// This format doesn't match the expected "2006-01-02 15:04:05" layout.
	malformedExpiry := time.Now().Add(24 * time.Hour).Format(time.RFC3339)
	_, err = rawDB.Exec(`
		INSERT INTO __gas_auth_sessions (id, subject, metadata, ip_address, user_agent, created_at, expires_at, last_active)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"bad-sess", "user-2", "{}", "127.0.0.1", "test", validCreated, malformedExpiry, validCreated)
	require.NoError(t, err)

	// This session has a future expiry but parseSQLiteTime will return zero time
	// because RFC3339 doesn't match "2006-01-02 15:04:05".
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.AddCookie(&http.Cookie{Name: "session_id", Value: "bad-sess"})
	_, err = svc.Authenticate(context.Background(), req2)

	// The session should be valid (expires 24h from now), but because parseSQLiteTime
	// silently returns zero time, the expiry is 0001-01-01 00:00:00, so
	// time.Now().After(zeroTime) == true and the session appears expired.
	assert.ErrorIs(t, err, auth.ErrCredentialsExpired,
		"session with malformed timestamp is incorrectly treated as expired due to silent zero-time parse")
}

// TestSQLiteTimeParsing_TruncatedTimestamp shows the issue with truncated timestamps.
func TestSQLiteTimeParsing_TruncatedTimestamp(t *testing.T) {
	rawDB, provider, migMgr := testutil.SetupSQLite(t)
	logger := testutil.NewNopLogger()

	cfg := session.DefaultConfig()
	cfg.Session.CleanupInterval = 0
	cfg.Session.CookieSecure = false

	svc := session.New(session.WithConfig(cfg))(provider, logger, migMgr, nil)
	require.NoError(t, svc.Init())
	require.NoError(t, migMgr.RunPending())
	t.Cleanup(func() { _ = svc.Close() })

	validCreated := time.Now().UTC().Format("2006-01-02 15:04:05")

	// Insert with a truncated timestamp (just a date, no time).
	_, err := rawDB.Exec(`
		INSERT INTO __gas_auth_sessions (id, subject, metadata, ip_address, user_agent, created_at, expires_at, last_active)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"trunc-sess", "user-3", "{}", "127.0.0.1", "test", validCreated, "2099-01-01", validCreated)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "session_id", Value: "trunc-sess"})
	_, err = svc.Authenticate(context.Background(), req)

	// "2099-01-01" doesn't parse with "2006-01-02 15:04:05" layout, so parseSQLiteTime
	// returns zero time and the session appears expired even though 2099 is in the future.
	assert.ErrorIs(t, err, auth.ErrCredentialsExpired,
		"truncated timestamp silently becomes zero time, making session appear expired")
}

// ---------------------------------------------------------------------------
// Finding #3: MySQL migration inconsistency (tested via PostgreSQL defaults)
// ---------------------------------------------------------------------------

// TestSessionPostgresHasMetadataDefault verifies that PostgreSQL sessions can
// be inserted with column defaults. The equivalent MySQL migration is MISSING
// the DEFAULT clause for metadata and user_agent.
func TestSessionPostgresHasMetadataDefault(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	pg := testutil.SetupPostgres(t)
	logger := testutil.NewNopLogger()
	migMgr := pg.MigrationManager()

	cfg := session.DefaultConfig()
	cfg.Session.CleanupInterval = 0
	cfg.Session.CookieSecure = false

	svc := session.New(session.WithConfig(cfg))(pg.Provider(), logger, migMgr, nil)
	require.NoError(t, svc.Init())
	require.NoError(t, migMgr.RunPending())
	t.Cleanup(func() { _ = svc.Close() })

	db := pg.Provider().DB()

	// PostgreSQL migration has DEFAULT '{}' for metadata and DEFAULT '' for user_agent.
	// This INSERT omits metadata, ip_address, and user_agent — should succeed.
	_, err := db.Exec(`
		INSERT INTO __gas_auth_sessions (id, subject, expires_at)
		VALUES ($1, $2, $3)`,
		"default-test", "user-1", time.Now().Add(24*time.Hour))
	assert.NoError(t, err,
		"PostgreSQL allows insert without metadata/user_agent (has DEFAULTs). "+
			"The MySQL migration is MISSING these defaults and would fail here.")

	// Verify defaults were applied.
	var metadata, userAgent string
	err = db.QueryRow("SELECT metadata::text, user_agent FROM __gas_auth_sessions WHERE id = $1", "default-test").
		Scan(&metadata, &userAgent)
	require.NoError(t, err)
	assert.Equal(t, "{}", metadata, "metadata defaults to empty JSON object")
	assert.Equal(t, "", userAgent, "user_agent defaults to empty string")
}

// ---------------------------------------------------------------------------
// Finding #12: User-Agent stored without length limit
// ---------------------------------------------------------------------------

func TestSessionStoresArbitrarilyLongUserAgent(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	svc := setupSessionService(t)

	// Create a session with an absurdly long User-Agent.
	longUA := make([]byte, 100_000)
	for i := range longUA {
		longUA[i] = 'A'
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("User-Agent", string(longUA))

	sess, err := svc.Create(context.Background(), "user-ua", gas.BasePrincipalMetadata{}, req)
	require.NoError(t, err, "no length validation on User-Agent")
	assert.Len(t, sess.UserAgent, 100_000,
		"100KB User-Agent stored without truncation — potential storage abuse vector")
}

// ---------------------------------------------------------------------------
// Finding #4: Cleanup goroutine context handling
// ---------------------------------------------------------------------------

func TestSessionCleanupGracefulShutdown(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	pg := testutil.SetupPostgres(t)
	logger := testutil.NewNopLogger()
	migMgr := pg.MigrationManager()

	cfg := session.DefaultConfig()
	cfg.Session.CleanupInterval = 100 * time.Millisecond
	cfg.Session.CleanupTimeout = 5 * time.Second

	svc := session.New(session.WithConfig(cfg))(pg.Provider(), logger, migMgr, nil)
	require.NoError(t, svc.Init())
	require.NoError(t, migMgr.RunPending())

	// Let cleanup run a few cycles.
	time.Sleep(350 * time.Millisecond)

	// Verify Close shuts down cleanly (doesn't hang).
	done := make(chan error, 1)
	go func() { done <- svc.Close() }()

	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Close() did not return within 5 seconds — cleanup goroutine may be stuck")
	}
}

// ---------------------------------------------------------------------------
// Finding #9: No session rotation API
// ---------------------------------------------------------------------------

func TestSessionNoRotationAPI(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	svc := setupSessionService(t)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	sess, err := svc.Create(context.Background(), "user-rotate", gas.BasePrincipalMetadata{"role": "user"}, req)
	require.NoError(t, err)

	// Simulate privilege escalation: user logs in and becomes admin.
	// Ideally we'd call svc.Rotate(ctx, sess.ID) to get a new session ID.
	// Instead, we must manually revoke the old and create a new one.
	principal := auth.NewPrincipal("user-rotate", auth.SchemeSession, sess.ID, nil)
	require.NoError(t, svc.Revoke(context.Background(), principal))

	newSess, err := svc.Create(context.Background(), "user-rotate", gas.BasePrincipalMetadata{"role": "admin"}, req)
	require.NoError(t, err)

	// The old session should be invalid.
	authReq := httptest.NewRequest(http.MethodGet, "/", nil)
	authReq.AddCookie(&http.Cookie{Name: "session_id", Value: sess.ID})
	_, err = svc.Authenticate(context.Background(), authReq)
	assert.Error(t, err, "old session should be revoked")

	// The new session should work.
	authReq2 := httptest.NewRequest(http.MethodGet, "/", nil)
	authReq2.AddCookie(&http.Cookie{Name: "session_id", Value: newSess.ID})
	p, err := svc.Authenticate(context.Background(), authReq2)
	require.NoError(t, err)
	assert.Equal(t, "admin", p.Metadata().Value("role"))

	// This works but is NOT atomic — there's a window between Revoke and Create
	// where the user has no valid session. A Rotate() method would do both atomically.
}
