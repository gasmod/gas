package token_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gasmod/gas/auth/internal/testutil"
	"github.com/gasmod/gas/auth/token"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func setupTokenService(t *testing.T, opts ...token.Option) *token.Service {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	pg := testutil.SetupPostgres(t)
	logger := testutil.NewNopLogger()
	migMgr := pg.MigrationManager()

	cfg := token.DefaultConfig()
	cfg.Token.CleanupInterval = 0 // disable background cleanup by default

	allOpts := append([]token.Option{token.WithConfig(cfg)}, opts...)
	svc := token.New(allOpts...)(pg.Provider(), logger, migMgr, nil)

	require.NoError(t, svc.Init())
	require.NoError(t, migMgr.RunPending())

	t.Cleanup(func() { _ = svc.Close() })
	return svc
}

// ---------------------------------------------------------------------------
// Lifecycle Tests
// ---------------------------------------------------------------------------

func TestTokenIssueAndVerify(t *testing.T) {
	svc := setupTokenService(t)

	rawToken, err := svc.Issue(context.Background(), "user-123", "email-verify", 15*time.Minute)
	require.NoError(t, err)
	assert.NotEmpty(t, rawToken)

	subject, err := svc.Verify(context.Background(), rawToken, "email-verify")
	require.NoError(t, err)
	assert.Equal(t, "user-123", subject)
}

func TestTokenSingleUse(t *testing.T) {
	svc := setupTokenService(t)

	rawToken, err := svc.Issue(context.Background(), "user-123", "email-verify", 15*time.Minute)
	require.NoError(t, err)

	// First verify succeeds.
	subject, err := svc.Verify(context.Background(), rawToken, "email-verify")
	require.NoError(t, err)
	assert.Equal(t, "user-123", subject)

	// Second verify fails — single-use token consumed.
	_, err = svc.Verify(context.Background(), rawToken, "email-verify")
	assert.ErrorIs(t, err, token.ErrTokenInvalid)
}

func TestTokenNonExistent(t *testing.T) {
	svc := setupTokenService(t)

	_, err := svc.Verify(context.Background(), "completely-fake-token", "email-verify")
	assert.ErrorIs(t, err, token.ErrTokenInvalid)
}

func TestTokenEmptyString(t *testing.T) {
	svc := setupTokenService(t)

	_, err := svc.Verify(context.Background(), "", "email-verify")
	assert.ErrorIs(t, err, token.ErrTokenInvalid)
}

// ---------------------------------------------------------------------------
// Expiry Tests
// ---------------------------------------------------------------------------

func TestTokenExpiry(t *testing.T) {
	svc := setupTokenService(t)

	rawToken, err := svc.Issue(context.Background(), "user-expiry", "reset", 1*time.Millisecond)
	require.NoError(t, err)

	time.Sleep(10 * time.Millisecond)

	_, err = svc.Verify(context.Background(), rawToken, "reset")
	assert.ErrorIs(t, err, token.ErrTokenExpired)

	// Token should also be consumed despite expiry — verify again.
	_, err = svc.Verify(context.Background(), rawToken, "reset")
	assert.ErrorIs(t, err, token.ErrTokenInvalid, "expired token should still be consumed")
}

func TestTokenDefaultTTLUsedWhenZero(t *testing.T) {
	svc := setupTokenService(t)

	rawToken, err := svc.Issue(context.Background(), "user-default-ttl", "verify", 0)
	require.NoError(t, err)

	// Token should be valid (default TTL is 15min).
	subject, err := svc.Verify(context.Background(), rawToken, "verify")
	require.NoError(t, err)
	assert.Equal(t, "user-default-ttl", subject)
}

// ---------------------------------------------------------------------------
// Purpose Mismatch Tests
// ---------------------------------------------------------------------------

func TestTokenWrongPurposeConsumesToken(t *testing.T) {
	svc := setupTokenService(t)

	rawToken, err := svc.Issue(context.Background(), "user-purpose", "password-reset", 15*time.Minute)
	require.NoError(t, err)

	// Verify with wrong purpose → ErrTokenInvalid, and token is CONSUMED.
	_, err = svc.Verify(context.Background(), rawToken, "email-verify")
	assert.ErrorIs(t, err, token.ErrTokenInvalid)

	// Now verify with correct purpose → also fails because token was already consumed.
	_, err = svc.Verify(context.Background(), rawToken, "password-reset")
	assert.ErrorIs(t, err, token.ErrTokenInvalid, "token consumed on wrong-purpose attempt")
}

// ---------------------------------------------------------------------------
// Revocation Tests
// ---------------------------------------------------------------------------

func TestTokenRevoke(t *testing.T) {
	svc := setupTokenService(t)

	rawToken, err := svc.Issue(context.Background(), "user-revoke", "reset", 15*time.Minute)
	require.NoError(t, err)

	require.NoError(t, svc.Revoke(context.Background(), rawToken))

	_, err = svc.Verify(context.Background(), rawToken, "reset")
	assert.ErrorIs(t, err, token.ErrTokenInvalid)
}

func TestTokenRevokeNonExistent(t *testing.T) {
	svc := setupTokenService(t)

	err := svc.Revoke(context.Background(), "nonexistent-token")
	assert.ErrorIs(t, err, token.ErrTokenInvalid)
}

func TestTokenRevokeAllByPurpose(t *testing.T) {
	svc := setupTokenService(t)

	token1, err := svc.Issue(context.Background(), "user-bulk", "reset", 15*time.Minute)
	require.NoError(t, err)
	token2, err := svc.Issue(context.Background(), "user-bulk", "reset", 15*time.Minute)
	require.NoError(t, err)
	token3, err := svc.Issue(context.Background(), "user-bulk", "invite", 15*time.Minute)
	require.NoError(t, err)

	require.NoError(t, svc.RevokeAllByPurpose(context.Background(), "user-bulk", "reset"))

	// "reset" tokens should be gone.
	_, err = svc.Verify(context.Background(), token1, "reset")
	assert.ErrorIs(t, err, token.ErrTokenInvalid)
	_, err = svc.Verify(context.Background(), token2, "reset")
	assert.ErrorIs(t, err, token.ErrTokenInvalid)

	// "invite" token should still be valid.
	subject, err := svc.Verify(context.Background(), token3, "invite")
	require.NoError(t, err)
	assert.Equal(t, "user-bulk", subject)
}

// ---------------------------------------------------------------------------
// Cleanup Goroutine Tests
// ---------------------------------------------------------------------------

func TestTokenCleanupGoroutine(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	pg := testutil.SetupPostgres(t)
	logger := testutil.NewNopLogger()
	migMgr := pg.MigrationManager()

	cfg := token.DefaultConfig()
	cfg.Token.CleanupInterval = 100 * time.Millisecond

	svc := token.New(token.WithConfig(cfg))(pg.Provider(), logger, migMgr, nil)
	require.NoError(t, svc.Init())
	require.NoError(t, migMgr.RunPending())

	rawToken, err := svc.Issue(context.Background(), "user-cleanup", "reset", 1*time.Millisecond)
	require.NoError(t, err)

	// Wait for token to expire and cleanup to run.
	time.Sleep(500 * time.Millisecond)

	_, err = svc.Verify(context.Background(), rawToken, "reset")
	assert.Error(t, err, "expired token should have been cleaned up")

	require.NoError(t, svc.Close())
}

// ---------------------------------------------------------------------------
// Concurrency Tests
// ---------------------------------------------------------------------------

func TestTokenConcurrentReplayPrevention(t *testing.T) {
	svc := setupTokenService(t)

	rawToken, err := svc.Issue(context.Background(), "user-replay", "verify", 15*time.Minute)
	require.NoError(t, err)

	const goroutines = 50
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

	// Exactly 1 goroutine should succeed.
	assert.Equal(t, int32(1), successCount.Load(), "only one verify should succeed for a single-use token")
}

func TestTokenConcurrentIssueAndVerify(t *testing.T) {
	svc := setupTokenService(t)

	const goroutines = 50
	var wg sync.WaitGroup
	var successCount atomic.Int32

	tokens := make([]string, goroutines)
	wg.Add(goroutines)
	for i := range goroutines {
		go func(idx int) {
			defer wg.Done()
			tok, issueErr := svc.Issue(context.Background(), fmt.Sprintf("user-%d", idx), "verify", 15*time.Minute)
			require.NoError(t, issueErr)
			tokens[idx] = tok
		}(i)
	}
	wg.Wait()

	wg.Add(goroutines)
	for i := range goroutines {
		go func(idx int) {
			defer wg.Done()
			subject, verifyErr := svc.Verify(context.Background(), tokens[idx], "verify")
			if verifyErr == nil && subject == fmt.Sprintf("user-%d", idx) {
				successCount.Add(1)
			}
		}(i)
	}
	wg.Wait()

	assert.Equal(t, int32(goroutines), successCount.Load())
}

// ---------------------------------------------------------------------------
// Edge Cases
// ---------------------------------------------------------------------------

func TestTokenLongPurpose(t *testing.T) {
	svc := setupTokenService(t)

	longPurpose := ""
	for range 1000 {
		longPurpose += "a"
	}

	rawToken, err := svc.Issue(context.Background(), "user-long", longPurpose, 15*time.Minute)
	require.NoError(t, err)

	subject, err := svc.Verify(context.Background(), rawToken, longPurpose)
	require.NoError(t, err)
	assert.Equal(t, "user-long", subject)
}

func TestTokenEmptySubjectAndPurpose(t *testing.T) {
	svc := setupTokenService(t)

	rawToken, err := svc.Issue(context.Background(), "", "", 15*time.Minute)
	require.NoError(t, err)

	subject, err := svc.Verify(context.Background(), rawToken, "")
	require.NoError(t, err)
	assert.Equal(t, "", subject)
}

// ---------------------------------------------------------------------------
// Name Test
// ---------------------------------------------------------------------------

func TestTokenServiceName(t *testing.T) {
	svc := setupTokenService(t)
	assert.Equal(t, "gas/auth/token", svc.Name())
}
