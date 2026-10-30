package token_test

import (
	"encoding/base64"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/auth"
	"github.com/gasmod/gas/auth/internal/cryptoutil"
	"github.com/gasmod/gas/auth/internal/testutil"
	"github.com/gasmod/gas/auth/token"
)

// Issue returns TokenLength random bytes as unpadded URL-safe base64, a
// different token each time, and Verify returns the subject of a token
// presented for its purpose. An empty purpose, and subjects and purposes
// of MaxValueLength bytes, round-trip as given.
func TestService_IssueAndVerify(t *testing.T) {
	t.Parallel()

	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			svc := newService(t, b, testConfig(), testutil.NewNopLogger())
			ctx := t.Context()

			raw, err := svc.Issue(ctx, "user-1", "email-verify", time.Hour)
			require.NoError(t, err)
			assert.Len(t, raw, 43)

			decoded, err := base64.RawURLEncoding.DecodeString(raw)
			require.NoError(t, err)
			assert.Len(t, decoded, token.DefaultTokenLength)

			other, err := svc.Issue(ctx, "user-1", "email-verify", time.Hour)
			require.NoError(t, err)
			assert.NotEqual(t, raw, other)

			subject, err := svc.Verify(ctx, raw, "email-verify")
			require.NoError(t, err)
			assert.Equal(t, "user-1", subject)

			longest := strings.Repeat("p", auth.MaxValueLength)

			for _, tc := range []struct{ subject, purpose string }{
				{"user-2", ""},
				{longest, longest},
				{"üser-ß", "reset ✓"},
			} {
				raw, err = svc.Issue(ctx, tc.subject, tc.purpose, time.Hour)
				require.NoError(t, err)

				subject, err = svc.Verify(ctx, raw, tc.purpose)
				require.NoError(t, err)
				assert.Equal(t, tc.subject, subject)
			}
		},
	)
}

// A token verifies once: a second Verify, an unknown token and the empty
// string are all ErrTokenInvalid, after the service's name.
func TestService_VerifyOnce(t *testing.T) {
	t.Parallel()

	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			svc := newService(t, b, testConfig(), testutil.NewNopLogger())
			ctx := t.Context()

			raw, err := svc.Issue(ctx, "user-1", "reset", time.Hour)
			require.NoError(t, err)

			_, err = svc.Verify(ctx, raw, "reset")
			require.NoError(t, err)

			for _, presented := range []string{raw, "no-such-token", ""} {
				subject, err := svc.Verify(ctx, presented, "reset")
				require.ErrorIs(t, err, token.ErrTokenInvalid)
				require.ErrorIs(t, err, auth.ErrUnauthenticated)
				require.EqualError(t, err, "gas/auth/token: unauthenticated: invalid token")
				assert.Empty(t, subject)
			}
		},
	)
}

// A token presented for another purpose is ErrTokenInvalid and is
// consumed all the same, so the right purpose fails afterwards too. The
// purpose is compared exactly.
func TestService_VerifyWrongPurposeBurns(t *testing.T) {
	t.Parallel()

	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			svc := newService(t, b, testConfig(), testutil.NewNopLogger())
			ctx := t.Context()

			for _, wrong := range []string{"email-verify", "Password-Reset", "password-reset ", ""} {
				raw, err := svc.Issue(ctx, "user-1", "password-reset", time.Hour)
				require.NoError(t, err)

				_, err = svc.Verify(ctx, raw, wrong)
				require.ErrorIs(t, err, token.ErrTokenInvalid, wrong)

				_, err = svc.Verify(ctx, raw, "password-reset")
				require.ErrorIs(t, err, token.ErrTokenInvalid, wrong)
			}
		},
	)
}

// An expired token is ErrTokenExpired and is consumed all the same. An
// expired token presented for the wrong purpose is ErrTokenInvalid, so it
// says nothing about its expiry.
func TestService_VerifyExpired(t *testing.T) {
	t.Parallel()

	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			svc := newService(t, b, testConfig(), testutil.NewNopLogger())
			ctx := t.Context()

			expired, err := svc.Issue(ctx, "user-1", "reset", time.Hour)
			require.NoError(t, err)

			wrongPurpose, err := svc.Issue(ctx, "user-1", "reset", time.Hour)
			require.NoError(t, err)

			expireAll(t, b)

			_, err = svc.Verify(ctx, expired, "reset")
			require.ErrorIs(t, err, token.ErrTokenExpired)
			require.ErrorIs(t, err, auth.ErrCredentialsExpired)
			require.EqualError(
				t,
				err,
				"gas/auth/token: unauthenticated: credentials expired: token expired",
			)

			_, err = svc.Verify(ctx, expired, "reset")
			require.ErrorIs(t, err, token.ErrTokenInvalid)

			_, err = svc.Verify(ctx, wrongPurpose, "invite")
			require.ErrorIs(t, err, token.ErrTokenInvalid)
			require.NotErrorIs(t, err, auth.ErrCredentialsExpired)
		},
	)
}

// A short ttl expires: a token issued for a millisecond is expired soon
// after, on every backend.
func TestService_IssueShortTTL(t *testing.T) {
	t.Parallel()

	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			svc := newService(t, b, testConfig(), testutil.NewNopLogger())

			raw, err := svc.Issue(t.Context(), "user-1", "reset", time.Millisecond)
			require.NoError(t, err)

			time.Sleep(20 * time.Millisecond)

			_, err = svc.Verify(t.Context(), raw, "reset")
			require.ErrorIs(t, err, token.ErrTokenExpired)
		},
	)
}

// Of many calls presenting the same token at once, exactly one succeeds
// and every other one is ErrTokenInvalid.
func TestService_VerifyConcurrent(t *testing.T) {
	t.Parallel()

	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			svc := newService(t, b, testConfig(), testutil.NewNopLogger())

			for range 5 {
				raw, err := svc.Issue(t.Context(), "user-1", "verify", time.Hour)
				require.NoError(t, err)

				// The losers send their errors, and the test checks them
				// after every caller is done.
				var (
					wins   atomic.Int64
					wg     sync.WaitGroup
					denied = make(chan error, 20)
				)

				for range 20 {
					wg.Go(func() {
						subject, err := svc.Verify(t.Context(), raw, "verify")
						if err != nil {
							denied <- err

							return
						}

						assert.Equal(t, "user-1", subject)
						wins.Add(1)
					})
				}

				wg.Wait()
				close(denied)
				assert.Equal(t, int64(1), wins.Load())

				for err := range denied {
					require.ErrorIs(t, err, token.ErrTokenInvalid)
					require.EqualError(t, err, "gas/auth/token: unauthenticated: invalid token")
				}
			}
		},
	)
}

// A ttl of 0 uses DefaultTTL; a negative ttl is ErrInvalidTTL and stores
// nothing.
func TestService_IssueTTL(t *testing.T) {
	t.Parallel()

	b := sqliteBackend(t)
	cfg := testConfig()

	cfg.Token.DefaultTTL = time.Millisecond

	svc := newService(t, b, cfg, testutil.NewNopLogger())

	raw, err := svc.Issue(t.Context(), "user-1", "reset", 0)
	require.NoError(t, err)

	time.Sleep(20 * time.Millisecond)

	_, err = svc.Verify(t.Context(), raw, "reset")
	require.ErrorIs(t, err, token.ErrTokenExpired)

	raw, err = svc.Issue(t.Context(), "user-1", "reset", -time.Second)
	require.ErrorIs(t, err, token.ErrInvalidTTL)
	require.EqualError(t, err, "gas/auth/token: invalid ttl: must not be negative")
	assert.Empty(t, raw)
	assert.Equal(t, 0, countTokens(t, b))
}

// The default TTL is long enough that a token issued with ttl 0 verifies.
func TestService_IssueDefaultTTL(t *testing.T) {
	t.Parallel()

	svc := newService(t, sqliteBackend(t), testConfig(), testutil.NewNopLogger())

	raw, err := svc.Issue(t.Context(), "user-1", "reset", 0)
	require.NoError(t, err)

	subject, err := svc.Verify(t.Context(), raw, "reset")
	require.NoError(t, err)
	assert.Equal(t, "user-1", subject)
}

// Revoke deletes one token, whatever its purpose and expiry, and is not
// an error for a token that is not stored, including one revoked or
// consumed already.
func TestService_Revoke(t *testing.T) {
	t.Parallel()

	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			svc := newService(t, b, testConfig(), testutil.NewNopLogger())
			ctx := t.Context()

			raw, err := svc.Issue(ctx, "user-1", "reset", time.Hour)
			require.NoError(t, err)

			kept, err := svc.Issue(ctx, "user-1", "reset", time.Hour)
			require.NoError(t, err)

			expired, err := svc.Issue(ctx, "user-1", "invite", time.Hour)
			require.NoError(t, err)

			require.NoError(t, svc.Revoke(ctx, raw))

			_, err = svc.Verify(ctx, raw, "reset")
			require.ErrorIs(t, err, token.ErrTokenInvalid)

			consumed, err := svc.Issue(ctx, "user-1", "reset", time.Hour)
			require.NoError(t, err)

			_, err = svc.Verify(ctx, consumed, "reset")
			require.NoError(t, err)

			for _, missing := range []string{raw, consumed, "no-such-token", ""} {
				require.NoError(t, svc.Revoke(ctx, missing))
			}

			expireAll(t, b)
			require.NoError(t, svc.Revoke(ctx, expired))

			_, err = svc.Verify(ctx, kept, "reset")
			require.ErrorIs(t, err, token.ErrTokenExpired)
		},
	)
}

// RevokeAllByPurpose deletes every token of the subject with the purpose,
// expired ones included, leaves the rest, and is not an error when there
// are none.
func TestService_RevokeAllByPurpose(t *testing.T) {
	t.Parallel()

	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			svc := newService(t, b, testConfig(), testutil.NewNopLogger())
			ctx := t.Context()

			issue := func(subject, purpose string) string {
				raw, err := svc.Issue(ctx, subject, purpose, time.Hour)
				require.NoError(t, err)

				return raw
			}

			expiredReset := issue("alice", "reset")

			expireAll(t, b)

			reset1, reset2 := issue("alice", "reset"), issue("alice", "reset")
			invite, bobReset := issue("alice", "invite"), issue("bob", "reset")

			require.NoError(t, svc.RevokeAllByPurpose(ctx, "alice", "reset"))
			require.NoError(t, svc.RevokeAllByPurpose(ctx, "alice", "reset"))
			require.NoError(t, svc.RevokeAllByPurpose(ctx, "nobody", "reset"))

			for _, raw := range []string{expiredReset, reset1, reset2} {
				_, err := svc.Verify(ctx, raw, "reset")
				require.ErrorIs(t, err, token.ErrTokenInvalid)
			}

			subject, err := svc.Verify(ctx, invite, "invite")
			require.NoError(t, err)
			assert.Equal(t, "alice", subject)

			subject, err = svc.Verify(ctx, bobReset, "reset")
			require.NoError(t, err)
			assert.Equal(t, "bob", subject)
		},
	)
}

// Issue rejects an empty subject with auth.ErrEmptySubject, and a subject
// or purpose longer than MaxValueLength bytes with auth.ErrValueTooLong,
// naming the field and never quoting the value. It checks them after the
// ttl, and stores nothing.
func TestService_IssueArguments(t *testing.T) {
	t.Parallel()

	b := sqliteBackend(t)
	svc := newService(t, b, testConfig(), testutil.NewNopLogger())
	tooLong := strings.Repeat("x", auth.MaxValueLength+1)

	tests := []struct {
		target           error
		subject, purpose string
		want             string
		ttl              time.Duration
	}{
		{
			subject: "", purpose: "reset", target: auth.ErrEmptySubject,
			want: "gas/auth/token: empty subject",
		},
		{
			subject: tooLong, purpose: "reset", target: auth.ErrValueTooLong,
			want: "gas/auth/token: value too long: subject is longer than 255 bytes",
		},
		{
			subject: "user-1", purpose: tooLong, target: auth.ErrValueTooLong,
			want: "gas/auth/token: value too long: purpose is longer than 255 bytes",
		},
		{
			subject: "", purpose: tooLong, ttl: -time.Second, target: token.ErrInvalidTTL,
			want: "gas/auth/token: invalid ttl: must not be negative",
		},
	}

	for _, tt := range tests {
		raw, err := svc.Issue(t.Context(), tt.subject, tt.purpose, tt.ttl)
		require.ErrorIs(t, err, tt.target)
		require.EqualError(t, err, tt.want)
		require.NotErrorIs(t, err, auth.ErrUnauthenticated)
		assert.Empty(t, raw)
	}

	assert.Equal(t, 0, countTokens(t, b))
}

// The database keeps the token's SHA-256 hash, never the token.
func TestService_StoresHashOnly(t *testing.T) {
	t.Parallel()

	testutil.ForEachBackend(
		t,
		func(t *testing.T, b testutil.Backend) { //nolint:thelper // a subtest body, not a helper
			svc := newService(t, b, testConfig(), testutil.NewNopLogger())

			raw, err := svc.Issue(t.Context(), "user-1", "reset", time.Hour)
			require.NoError(t, err)

			rows, err := b.DB.DB().QueryContext(
				t.Context(),
				"SELECT id, subject, token_hash, purpose FROM __gas_auth_tokens",
			)
			require.NoError(t, err)

			defer func() { require.NoError(t, rows.Close()) }()

			require.True(t, rows.Next())

			var id, subject, hash, purpose string

			require.NoError(t, rows.Scan(&id, &subject, &hash, &purpose))
			require.NoError(t, rows.Err())

			assert.Equal(t, cryptoutil.SHA256Hex(raw), hash)

			for _, col := range []string{id, subject, hash, purpose} {
				assert.NotContains(t, col, raw)
			}
		},
	)
}

// FuzzService_Verify presents arbitrary tokens for arbitrary purposes,
// and issues tokens with arbitrary subjects and purposes. It checks that
// nothing panics; that a token that was never issued, like one already
// consumed, is denied with ErrTokenInvalid and the fixed message
// `gas/auth/token: unauthenticated: invalid token`, which cannot hold
// the token; that Revoke of such a token is not an error; that Issue
// rejects exactly the subjects and purposes the documentation says it
// does; and that an issued token verifies once, returning its subject,
// exactly when the purpose matches. The inputs share one service and
// database, which holds no token between inputs.
func FuzzService_Verify(f *testing.F) {
	for _, seed := range []struct{ raw, purpose, issuedPurpose string }{
		{"", "", ""},
		{"abc", "reset", "reset"},
		{"hunter2", "reset", "Reset"},
		{"x' OR '1'='1", "'; DROP TABLE __gas_auth_tokens; --", "invite"},
		{"\x00\xff\xfe", "\x00", "\x00"},
		{strings.Repeat("A", 4096), "purpose ✓", "purpose ✓"},
	} {
		f.Add(seed.raw, seed.purpose, seed.issuedPurpose)
	}

	svc := newService(f, sqliteBackend(f), testConfig(), testutil.NewNopLogger())

	const invalid = "gas/auth/token: unauthenticated: invalid token"

	f.Fuzz(func(t *testing.T, raw, purpose, issuedPurpose string) {
		ctx := t.Context()

		subject, err := svc.Verify(ctx, raw, purpose)
		require.ErrorIs(t, err, token.ErrTokenInvalid)
		require.EqualError(t, err, invalid)
		require.Empty(t, subject)
		require.NoError(t, svc.Revoke(ctx, raw))

		issued, err := svc.Issue(ctx, raw, issuedPurpose, time.Hour)

		switch {
		case raw == "":
			require.ErrorIs(t, err, auth.ErrEmptySubject)

			return
		case len(raw) > auth.MaxValueLength || len(issuedPurpose) > auth.MaxValueLength:
			require.ErrorIs(t, err, auth.ErrValueTooLong)

			return
		}

		require.NoError(t, err)

		subject, err = svc.Verify(ctx, issued, purpose)
		if purpose == issuedPurpose {
			require.NoError(t, err)
			require.Equal(t, raw, subject)
		} else {
			require.EqualError(t, err, invalid)
		}

		_, err = svc.Verify(ctx, issued, issuedPurpose)
		require.EqualError(t, err, invalid)
	})
}
