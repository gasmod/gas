package modulesauth_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gasmod/gas/auth/token"
)

// Docs: modules/auth.mdx#single-use-tokens
// "`Issue(ctx, subject, purpose, ttl)` stores the SHA-256 hash of a random
// token (`token.tokenlength` bytes, URL-safe base64) with its purpose, subject
// and expiry, and returns the raw token. A `ttl` of 0 uses `token.defaultttl`."
func TestTokenIssue(t *testing.T) {
	t.Parallel()
	db := sqliteDB(t)
	cfg := token.DefaultConfig()
	cfg.Token.TokenLength = 20
	cfg.Token.DefaultTTL = 100 * time.Millisecond
	cfg.Token.CleanupInterval = 0
	svc := mustToken(t, db, cfg)
	ctx := t.Context()
	raw, err := svc.Issue(ctx, "u1", "verify-email", time.Hour)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(b) != 20 {
		t.Errorf("token %q decodes to %d bytes (%v), want 20", raw, len(b), err)
	}
	sum := sha256.Sum256([]byte(raw))
	var hash, purpose, subject string
	if err := db.DB().QueryRowContext(ctx, "SELECT token_hash, purpose, subject FROM __gas_auth_tokens").Scan(&hash, &purpose, &subject); err != nil {
		t.Fatalf("read token row: %v", err)
	}
	if hash != hex.EncodeToString(sum[:]) || purpose != "verify-email" || subject != "u1" || strings.Contains(hash, raw) {
		t.Errorf("stored row = %q %q %q, want the SHA-256 hash, purpose and subject", hash, purpose, subject)
	}

	short, _ := svc.Issue(ctx, "u1", "p", 0)
	long, _ := svc.Issue(ctx, "u1", "p", time.Hour)
	time.Sleep(200 * time.Millisecond)
	if _, err := svc.Verify(ctx, short, "p"); !errors.Is(err, token.ErrTokenExpired) {
		t.Errorf("ttl 0 with defaultttl 100ms, after 200ms: %v, want ErrTokenExpired", err)
	}
	if sub, err := svc.Verify(ctx, long, "p"); err != nil || sub != "u1" {
		t.Errorf("ttl 1h: %q, %v", sub, err)
	}
}

// Docs: modules/auth.mdx#single-use-tokens
// "`Verify(ctx, raw, purpose)` fetches and deletes the token in one atomic store
// operation, then checks it, and returns the subject. A token is consumed the
// first time it is presented, whatever the outcome: an unknown or already used
// token fails with `token.ErrTokenInvalid`, a wrong purpose also fails with
// `token.ErrTokenInvalid` and burns the token, and an expired one fails with
// `token.ErrTokenExpired`."
func TestTokenVerify(t *testing.T) {
	t.Parallel()
	db := sqliteDB(t)
	svc := mustToken(t, db, nil)
	ctx := t.Context()

	raw, _ := svc.Issue(ctx, "u1", "reset", time.Hour)
	if sub, err := svc.Verify(ctx, raw, "reset"); err != nil || sub != "u1" {
		t.Fatalf("Verify = %q, %v", sub, err)
	}
	if _, err := svc.Verify(ctx, raw, "reset"); err != token.ErrTokenInvalid { //nolint:errorlint // the sentinel itself
		t.Errorf("used token: %v, want ErrTokenInvalid", err)
	}
	if _, err := svc.Verify(ctx, "never-issued", "reset"); err != token.ErrTokenInvalid { //nolint:errorlint // the sentinel itself
		t.Errorf("unknown token: %v, want ErrTokenInvalid", err)
	}
	wrong, _ := svc.Issue(ctx, "u1", "reset", time.Hour)
	if _, err := svc.Verify(ctx, wrong, "login"); err != token.ErrTokenInvalid { //nolint:errorlint // the sentinel itself
		t.Errorf("wrong purpose: %v, want ErrTokenInvalid", err)
	}
	if _, err := svc.Verify(ctx, wrong, "reset"); err != token.ErrTokenInvalid { //nolint:errorlint // the sentinel itself
		t.Errorf("token after a wrong-purpose attempt: %v, want it burned", err)
	}
	expired, _ := svc.Issue(ctx, "u1", "reset", -time.Minute)
	if _, err := svc.Verify(ctx, expired, "reset"); err != token.ErrTokenExpired { //nolint:errorlint // the sentinel itself
		t.Errorf("expired: %v, want ErrTokenExpired", err)
	}
	if _, err := svc.Verify(ctx, expired, "reset"); err != token.ErrTokenInvalid { //nolint:errorlint // the sentinel itself
		t.Errorf("expired token presented again: %v, want it consumed", err)
	}
	if n := rowCount(t, db.DB(), "__gas_auth_tokens"); n != 0 {
		t.Errorf("rows left = %d, want every presented token deleted", n)
	}
}

// Docs: modules/auth.mdx#single-use-tokens
// "Of two concurrent `Verify` calls with the same token, only one can
// succeed."
// Run on every backend; with Docker that includes PostgreSQL, where the
// calls really overlap.
func TestTokenVerifyConcurrent(t *testing.T) {
	t.Parallel()
	for i, mk := range allBackends(t) {
		t.Run(backendNames[i], func(t *testing.T) {
			t.Parallel()
			svc := mustToken(t, mk(t).open(t), nil)
			for range 10 {
				raw, err := svc.Issue(t.Context(), "u1", "p", time.Hour)
				if err != nil {
					t.Fatalf("Issue: %v", err)
				}
				var wg sync.WaitGroup
				var mu sync.Mutex
				wins := 0
				for range 8 {
					wg.Go(func() {
						if _, err := svc.Verify(t.Context(), raw, "p"); err == nil {
							mu.Lock()
							wins++
							mu.Unlock()
						}
					})
				}
				wg.Wait()
				if wins != 1 {
					t.Fatalf("concurrent Verify successes = %d, want 1", wins)
				}
			}
		})
	}
}

// Docs: modules/auth.mdx#single-use-tokens
// "`Revoke(ctx, raw)` deletes one token and returns `token.ErrTokenInvalid`
// when there is none; `RevokeAllByPurpose(ctx, subject, purpose)` deletes a
// subject's tokens for one purpose."
func TestTokenRevoke(t *testing.T) {
	t.Parallel()
	svc := mustToken(t, sqliteDB(t), nil)
	ctx := t.Context()
	raw, _ := svc.Issue(ctx, "u1", "p", time.Hour)
	if err := svc.Revoke(ctx, raw); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, err := svc.Verify(ctx, raw, "p"); !errors.Is(err, token.ErrTokenInvalid) {
		t.Errorf("revoked token verified: %v", err)
	}
	if err := svc.Revoke(ctx, raw); err != token.ErrTokenInvalid { //nolint:errorlint // the sentinel itself
		t.Errorf("Revoke of a missing token = %v, want ErrTokenInvalid", err)
	}
	a1, _ := svc.Issue(ctx, "u1", "reset", time.Hour)
	a2, _ := svc.Issue(ctx, "u1", "reset", time.Hour)
	keepPurpose, _ := svc.Issue(ctx, "u1", "verify", time.Hour)
	keepSubject, _ := svc.Issue(ctx, "u2", "reset", time.Hour)
	if err := svc.RevokeAllByPurpose(ctx, "u1", "reset"); err != nil {
		t.Fatalf("RevokeAllByPurpose: %v", err)
	}
	for name, raw := range map[string]string{"a1": a1, "a2": a2} {
		if _, err := svc.Verify(ctx, raw, "reset"); err == nil {
			t.Errorf("%s survived RevokeAllByPurpose", name)
		}
	}
	if _, err := svc.Verify(ctx, keepPurpose, "verify"); err != nil {
		t.Errorf("other purpose revoked: %v", err)
	}
	if _, err := svc.Verify(ctx, keepSubject, "reset"); err != nil {
		t.Errorf("other subject revoked: %v", err)
	}
}

// Docs: modules/auth.mdx#single-use-tokens
// "Expired tokens are cleaned up in the background exactly as sessions are,
// with the `token.cleanup*` keys."
func TestTokenCleanup(t *testing.T) {
	t.Parallel()
	cfgWith := func(interval time.Duration) *token.Config {
		cfg := token.DefaultConfig()
		cfg.Token.CleanupInterval = interval
		return cfg
	}
	db := sqliteDB(t)
	svc := mustToken(t, db, cfgWith(50*time.Millisecond))
	if _, err := svc.Issue(t.Context(), "u", "p", time.Millisecond); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	live, _ := svc.Issue(t.Context(), "u", "p", time.Hour)
	if !eventually(func() bool { return rowCount(t, db.DB(), "__gas_auth_tokens") == 1 }) {
		t.Error("expired token not cleaned up")
	}
	if err := svc.Close(t.Context()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_, _ = svc.Issue(t.Context(), "u", "p", time.Millisecond)
	time.Sleep(300 * time.Millisecond)
	if n := rowCount(t, db.DB(), "__gas_auth_tokens"); n != 2 {
		t.Errorf("rows after Close = %d, want 2: cleanup still running", n)
	}
	_ = live

	db2 := sqliteDB(t)
	off := mustToken(t, db2, cfgWith(0))
	_, _ = off.Issue(t.Context(), "u", "p", time.Millisecond)
	time.Sleep(300 * time.Millisecond)
	if n := rowCount(t, db2.DB(), "__gas_auth_tokens"); n != 1 {
		t.Errorf("interval 0: rows = %d, want 1", n)
	}
}

// Docs: modules/auth.mdx#configuration
// "| `token.defaultttl` | duration | `15m` | Lifetime when `Issue` gets a `ttl` of 0. Must be greater than zero. |"
// and "| `token.tokenlength` | int | `32` | Random bytes per token. At least 16. |"
// and "| `token.cleanupinterval` | duration | `1h` |" and
// "| `token.cleanuptimeout` | duration | `30s` | Bound on each cleanup run. Must be positive while cleanup is enabled. |"
// and "The HS256 key, RS256 public key path and cleanup-timeout rules are
// checked for every config, including one given with `WithConfig`."
func TestTokenConfigTable(t *testing.T) {
	t.Parallel()
	d := token.DefaultConfig().Token
	if d.DefaultTTL != 15*time.Minute || d.TokenLength != 32 || d.CleanupInterval != time.Hour || d.CleanupTimeout != 30*time.Second {
		t.Errorf("defaults = %+v", d)
	}
	svc, err := startStore(t, "token", sqliteDB(t), boundConfig(t, map[string]any{"token": map[string]any{
		"tokenlength": 16, "defaultttl": "1h",
	}}), nil, nil)
	if err != nil {
		t.Fatalf("bound Init: %v", err)
	}
	raw, _ := svc.(*token.Service).Issue(t.Context(), "u", "p", 0)
	if b, _ := base64.RawURLEncoding.DecodeString(raw); len(b) != 16 {
		t.Errorf("bound tokenlength 16 gave %d bytes", len(b))
	}
	for name, doc := range map[string]map[string]any{
		"tokenlength 15":     {"tokenlength": 15},
		"zero defaultttl":    {"defaultttl": "0s"},
		"zero cleanup bound": {"cleanupinterval": "1h", "cleanuptimeout": "0s"},
	} {
		if _, err := startStore(t, "token", sqliteDB(t), boundConfig(t, map[string]any{"token": doc}), nil, nil); err == nil {
			t.Errorf("bound %s: Init succeeded", name)
		}
	}
	given := token.DefaultConfig()
	given.Token.TokenLength = 8
	given.Token.DefaultTTL = 0
	if _, err := startStore(t, "token", sqliteDB(t), nil, nil, token.WithConfig(given)); err != nil {
		t.Errorf("WithConfig tokenlength 8, defaultttl 0: %v, want the tags skipped", err)
	}
	timeout := token.DefaultConfig()
	timeout.Token.CleanupTimeout = 0
	if _, err := startStore(t, "token", sqliteDB(t), nil, nil, token.WithConfig(timeout)); err == nil || !strings.HasPrefix(err.Error(), "gas/auth/token: ") {
		t.Errorf("WithConfig zero cleanup timeout with cleanup on: %v, want a gas/auth/token error", err)
	}
	timeout.Token.CleanupInterval = 0
	if _, err := startStore(t, "token", sqliteDB(t), nil, nil, token.WithConfig(timeout)); err != nil {
		t.Errorf("zero cleanup timeout with cleanup off: %v", err)
	}
}
