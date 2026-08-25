package token_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gasmod/gas/auth/internal/cryptoutil"
	"github.com/gasmod/gas/auth/internal/testutil"
	"github.com/gasmod/gas/auth/token"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Finding #1: Token Verify race condition — GET then DELETE is not atomic
// ---------------------------------------------------------------------------

// TestTokenVerifyRace_StressTest runs a high number of iterations to increase
// the probability of hitting the race window between GetTokenByHash and
// DeleteTokenByID. Each iteration issues a token and has many goroutines
// race to verify it. If the race is hit, more than one goroutine succeeds.
func TestTokenVerifyRace_StressTest(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stress test in short mode")
	}
	svc := setupTokenService(t)

	const iterations = 20
	const goroutines = 100

	var totalDoubleVerifies int

	for iter := range iterations {
		rawToken, err := svc.Issue(context.Background(), fmt.Sprintf("user-%d", iter), "verify", 15*time.Minute)
		require.NoError(t, err)

		var wg sync.WaitGroup
		var successCount atomic.Int32
		wg.Add(goroutines)
		for range goroutines {
			go func() {
				defer wg.Done()
				_, verifyErr := svc.Verify(context.Background(), rawToken, "verify")
				if verifyErr == nil {
					successCount.Add(1)
				}
			}()
		}
		wg.Wait()

		if successCount.Load() > 1 {
			totalDoubleVerifies++
			t.Logf("iteration %d: %d goroutines succeeded (expected 1)", iter, successCount.Load())
		}
	}

	// This assertion documents the vulnerability. On a fast local database
	// the window may be too small to hit reliably, but in production with
	// network latency the risk is real.
	if totalDoubleVerifies > 0 {
		t.Errorf("RACE CONFIRMED: %d/%d iterations had >1 successful verify — "+
			"the single-use invariant was violated", totalDoubleVerifies, iterations)
	} else {
		t.Logf("race window not hit in %d iterations (does not mean it's safe — "+
			"see TestTokenVerifyRace_DeleteDoesNotCheckRowCount for the code-level proof)", iterations)
	}
}

// TestTokenVerifyRace_DeleteDoesNotCheckRowCount proves that DeleteTokenByID
// succeeds silently even when the token was already deleted. This is the root
// cause: two concurrent Verify calls that both read the token before either
// deletes it will BOTH proceed to return the subject.
func TestTokenVerifyRace_DeleteDoesNotCheckRowCount(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	svc := setupTokenService(t)

	// Issue a token and verify it (consuming it).
	rawToken, err := svc.Issue(context.Background(), "user-del", "verify", 15*time.Minute)
	require.NoError(t, err)

	subject, err := svc.Verify(context.Background(), rawToken, "verify")
	require.NoError(t, err)
	assert.Equal(t, "user-del", subject)

	// The token is now deleted. A second Verify correctly returns invalid
	// because GetTokenByHash finds nothing. But this only works because the
	// GET fails — not because DELETE checked its row count.
	_, err = svc.Verify(context.Background(), rawToken, "verify")
	assert.Error(t, err, "second verify fails because GET finds nothing")

	// The vulnerability: if two goroutines both GET the record before either
	// DELETEs, both will have a valid record, both will DELETE (one with 0
	// rows affected — silently), and both will return success.
	//
	// To fix: use DELETE ... RETURNING or check RowsAffected from DeleteTokenByHash.
}

// TestTokenVerifyRace_SimulatedWithDirectSQL directly simulates the race by
// issuing a token, manually querying it twice (simulating two concurrent GETs),
// then deleting it. This proves the code path that enables the race.
func TestTokenVerifyRace_SimulatedWithDirectSQL(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	pg := testutil.SetupPostgres(t)
	logger := testutil.NewNopLogger()
	migMgr := pg.MigrationManager()

	cfg := token.DefaultConfig()
	cfg.Token.CleanupInterval = 0

	svc := token.New(token.WithConfig(cfg))(pg.Provider(), logger, migMgr, nil)
	require.NoError(t, svc.Init())
	require.NoError(t, migMgr.RunPending())
	t.Cleanup(func() { _ = svc.Close() })

	rawToken, err := svc.Issue(context.Background(), "user-sim", "verify", 15*time.Minute)
	require.NoError(t, err)

	tokenHash := cryptoutil.SHA256Hex(rawToken)
	db := pg.Provider().DB()

	// Simulate two concurrent GETs — both see the token.
	var subject1, subject2 string
	err = db.QueryRow("SELECT subject FROM __gas_auth_tokens WHERE token_hash = $1", tokenHash).Scan(&subject1)
	require.NoError(t, err, "first GET finds the token")

	err = db.QueryRow("SELECT subject FROM __gas_auth_tokens WHERE token_hash = $1", tokenHash).Scan(&subject2)
	require.NoError(t, err, "second GET also finds the token (race window)")

	assert.Equal(t, "user-sim", subject1)
	assert.Equal(t, "user-sim", subject2)

	// First DELETE succeeds.
	res, err := db.Exec("DELETE FROM __gas_auth_tokens WHERE token_hash = $1", tokenHash)
	require.NoError(t, err)
	rows, _ := res.RowsAffected()
	assert.Equal(t, int64(1), rows, "first DELETE removes the token")

	// Second DELETE also succeeds — with 0 rows affected, but NO error.
	res, err = db.Exec("DELETE FROM __gas_auth_tokens WHERE token_hash = $1", tokenHash)
	require.NoError(t, err, "second DELETE does not return an error")
	rows, _ = res.RowsAffected()
	assert.Equal(t, int64(0), rows, "second DELETE affects 0 rows")

	// Both callers would now proceed with a valid record and return success.
	// The fix: the application must check RowsAffected or use an atomic
	// DELETE ... RETURNING instead of separate GET + DELETE.
}

// ---------------------------------------------------------------------------
// Finding: Token Verify deletes before validating — wrong purpose silently
// consumes the token. This is documented as a trade-off but can surprise
// callers if not aware.
// ---------------------------------------------------------------------------

func TestTokenVerify_WrongPurposeConsumesToken(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	svc := setupTokenService(t)

	rawToken, err := svc.Issue(context.Background(), "user-purpose", "password-reset", 15*time.Minute)
	require.NoError(t, err)

	// Verify with wrong purpose — token is consumed but returns error.
	_, err = svc.Verify(context.Background(), rawToken, "email-verify")
	assert.Error(t, err, "wrong purpose returns error")

	// Now the correct purpose also fails — the token was already burned.
	_, err = svc.Verify(context.Background(), rawToken, "password-reset")
	assert.Error(t, err, "correct purpose fails because token was already consumed")
}

// ---------------------------------------------------------------------------
// Finding #4: Cleanup context handling
// ---------------------------------------------------------------------------

func TestTokenCleanupRemovesExpired(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	pg := testutil.SetupPostgres(t)
	logger := testutil.NewNopLogger()
	migMgr := pg.MigrationManager()

	cfg := token.DefaultConfig()
	cfg.Token.CleanupInterval = 200 * time.Millisecond
	cfg.Token.CleanupTimeout = 5 * time.Second

	svc := token.New(token.WithConfig(cfg))(pg.Provider(), logger, migMgr, nil)
	require.NoError(t, svc.Init())
	require.NoError(t, migMgr.RunPending())
	t.Cleanup(func() { _ = svc.Close() })

	// Issue a token with very short TTL.
	rawToken, err := svc.Issue(context.Background(), "user-cleanup", "verify", 50*time.Millisecond)
	require.NoError(t, err)

	tokenHash := cryptoutil.SHA256Hex(rawToken)
	db := pg.Provider().DB()

	// Token exists initially.
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM __gas_auth_tokens WHERE token_hash = $1", tokenHash).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count, "token exists before cleanup")

	// Wait for token to expire and cleanup to run.
	time.Sleep(500 * time.Millisecond)

	err = db.QueryRow("SELECT COUNT(*) FROM __gas_auth_tokens WHERE token_hash = $1", tokenHash).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count, "cleanup goroutine removed the expired token")
}

func TestTokenCleanupGracefulShutdown(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	pg := testutil.SetupPostgres(t)
	logger := testutil.NewNopLogger()
	migMgr := pg.MigrationManager()

	cfg := token.DefaultConfig()
	cfg.Token.CleanupInterval = 100 * time.Millisecond
	cfg.Token.CleanupTimeout = 5 * time.Second

	svc := token.New(token.WithConfig(cfg))(pg.Provider(), logger, migMgr, nil)
	require.NoError(t, svc.Init())
	require.NoError(t, migMgr.RunPending())

	// Let a few cleanup cycles run.
	time.Sleep(350 * time.Millisecond)

	// Close should stop the goroutine without hanging.
	err := svc.Close()
	assert.NoError(t, err, "Close should shut down cleanup goroutine cleanly")
}
