package jwt_test

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/auth"
	"github.com/gasmod/gas/auth/jwt"
)

// A signed token carries the custom claims plus `sub`, a random `jti`,
// `iat` now and `exp` the expiry later, in whole seconds, and no `iss`,
// `aud` or `nbf` when none is configured.
func TestSign_Claims(t *testing.T) {
	t.Parallel()

	svc := newService(t, hsConfig())

	before := time.Now().Truncate(time.Second)
	token, err := svc.SignWithExpiry("alice", m{"role": "admin", "n": 3}, time.Hour)
	require.NoError(t, err)

	after := time.Now()

	c := unverified(t, token)
	assert.Equal(t, "alice", c["sub"])
	assert.Equal(t, "admin", c["role"])
	assert.InDelta(t, 3, c["n"], 0)
	assert.NotContains(t, c, "iss")
	assert.NotContains(t, c, "aud")
	assert.NotContains(t, c, "nbf")
	assert.Len(t, c, 6)

	jti, ok := c["jti"].(string)
	require.True(t, ok)
	assert.Regexp(t, `^[A-Za-z0-9_-]{22}$`, jti)

	iat, err := c.GetIssuedAt()
	require.NoError(t, err)
	assert.False(t, iat.Before(before))
	assert.False(t, iat.After(after))
	assert.Equal(t, iat.Truncate(time.Second), iat.Time)

	exp, err := c.GetExpirationTime()
	require.NoError(t, err)
	assert.Equal(t, time.Hour, exp.Sub(iat.Time))
}

// Sign uses the configured expiry.
func TestSign_ConfiguredExpiry(t *testing.T) {
	t.Parallel()

	svc := newService(t, hsConfig(func(s *jwt.Settings) { s.Expiry = 2 * time.Hour }))

	token, err := svc.Sign("alice", nil)
	require.NoError(t, err)

	got, err := svc.Verify(token)
	require.NoError(t, err)
	assert.Equal(t, 2*time.Hour, got.ExpiresAt.Sub(got.IssuedAt))
}

// The configured issuer and audience are set as `iss` and `aud`.
func TestSign_IssuerAudience(t *testing.T) {
	t.Parallel()

	svc := newService(t, hsConfig(func(s *jwt.Settings) {
		s.Issuer = "auth.example"
		s.Audience = "api.example"
	}))

	token, err := svc.Sign("alice", nil)
	require.NoError(t, err)

	c := unverified(t, token)
	assert.Equal(t, "auth.example", c["iss"])
	assert.Equal(t, "api.example", c["aud"])
}

// Every token gets a new `jti`.
func TestSign_UniqueTokenIDs(t *testing.T) {
	t.Parallel()

	svc := newService(t, hsConfig())
	seen := map[string]bool{}

	for range 200 {
		token, err := svc.Sign("alice", nil)
		require.NoError(t, err)

		got, err := svc.Verify(token)
		require.NoError(t, err)
		assert.False(t, seen[got.TokenID])

		seen[got.TokenID] = true
	}
}

// A custom claim named after a registered claim is an error, naming the
// first such claim in sorted order, and no token is made.
func TestSign_ReservedClaims(t *testing.T) {
	t.Parallel()

	svc := newService(t, hsConfig())

	for _, name := range []string{"aud", "exp", "iat", "iss", "jti", "nbf", "sub"} {
		token, err := svc.Sign("alice", m{name: "x", "role": "admin"})
		require.ErrorIs(t, err, jwt.ErrReservedClaim, name)
		require.EqualError(t, err, `gas/auth/jwt: reserved claim "`+name+`"`)
		assert.Empty(t, token)
	}

	_, err := svc.Sign("alice", m{"sub": 1, "exp": 2, "aud": 3})
	require.EqualError(t, err, `gas/auth/jwt: reserved claim "aud"`)

	_, err = svc.Sign("alice", m{"SUB": "x", "Exp": 1})
	require.NoError(t, err, "claim names are case-sensitive")
}

// A custom claim whose value cannot be encoded as JSON is an error naming
// the claim, never its value.
func TestSign_InvalidClaim(t *testing.T) {
	t.Parallel()

	svc := newService(t, hsConfig())

	_, err := svc.Sign("alice", m{"ch": make(chan int), "z": math.NaN()})
	require.ErrorIs(t, err, jwt.ErrInvalidClaim)
	require.EqualError(t, err, `gas/auth/jwt: invalid claim "ch": value cannot be encoded as JSON`)

	_, err = svc.Sign("alice", m{"a": 1, "z": math.Inf(1)})
	require.EqualError(t, err, `gas/auth/jwt: invalid claim "z": value cannot be encoded as JSON`)
	assert.NotContains(t, err.Error(), "Inf")
}

// An empty subject is an error matching auth.ErrEmptySubject.
func TestSign_EmptySubject(t *testing.T) {
	t.Parallel()

	_, err := newService(t, hsConfig()).Sign("", nil)
	require.ErrorIs(t, err, jwt.ErrEmptySubject)
	require.ErrorIs(t, err, auth.ErrEmptySubject)
	require.EqualError(t, err, "gas/auth/jwt: empty subject")
}

// A subject of more than auth.MaxValueLength bytes is an error matching
// auth.ErrValueTooLong that names the subject, never its value; one of
// exactly that length signs.
func TestSign_SubjectTooLong(t *testing.T) {
	t.Parallel()

	svc := newService(t, hsConfig())
	long := strings.Repeat("s", auth.MaxValueLength+1)

	token, err := svc.Sign(long, nil)
	require.ErrorIs(t, err, jwt.ErrValueTooLong)
	require.ErrorIs(t, err, auth.ErrValueTooLong)
	require.EqualError(t, err, "gas/auth/jwt: value too long: subject is longer than 255 bytes")
	assert.Empty(t, token)

	_, err = svc.SignWithExpiry(long, nil, time.Hour)
	require.ErrorIs(t, err, jwt.ErrValueTooLong)

	token, err = svc.Sign(strings.Repeat("s", auth.MaxValueLength), nil)
	require.NoError(t, err)

	got, err := svc.Verify(token)
	require.NoError(t, err)
	assert.Len(t, got.Subject, auth.MaxValueLength)
}

// An expiry that is not positive is ErrInvalidTTL.
func TestSignWithExpiry_NotPositive(t *testing.T) {
	t.Parallel()

	svc := newService(t, hsConfig())

	for _, expiry := range []time.Duration{0, -time.Nanosecond, -time.Hour} {
		token, err := svc.SignWithExpiry("alice", nil, expiry)
		require.ErrorIs(t, err, jwt.ErrInvalidTTL)
		require.EqualError(t, err, "gas/auth/jwt: invalid ttl: must be positive")
		assert.Empty(t, token)
	}
}

// Sign's checks run in the documented order.
func TestSign_CheckOrder(t *testing.T) {
	t.Parallel()

	pub, _ := rsaFiles(t, rsaKey2048())
	verifyOnly := newService(t, rsConfig(pub, ""))
	svc := newService(t, hsConfig())
	reserved := m{"sub": "x"}

	_, err := build(t, hsConfig()).SignWithExpiry("", reserved, 0)
	require.ErrorIs(t, err, jwt.ErrNotInitialized)

	_, err = verifyOnly.SignWithExpiry("", reserved, 0)
	require.ErrorIs(t, err, jwt.ErrSigningKeyNotConfigured)
	require.EqualError(t, err, "gas/auth/jwt: signing key not configured")

	_, err = svc.SignWithExpiry("", reserved, 0)
	require.ErrorIs(t, err, jwt.ErrEmptySubject)

	_, err = svc.SignWithExpiry(strings.Repeat("s", auth.MaxValueLength+1), reserved, 0)
	require.ErrorIs(t, err, jwt.ErrValueTooLong)

	_, err = svc.SignWithExpiry("alice", reserved, 0)
	require.ErrorIs(t, err, jwt.ErrInvalidTTL)

	_, err = svc.SignWithExpiry("alice", reserved, time.Hour)
	require.ErrorIs(t, err, jwt.ErrReservedClaim)
}

// Sign does not change the claims map, and a nil map means no claims.
func TestSign_ClaimsMap(t *testing.T) {
	t.Parallel()

	svc := newService(t, hsConfig())

	given := m{"role": "admin"}
	_, err := svc.Sign("alice", given)
	require.NoError(t, err)
	assert.Equal(t, m{"role": "admin"}, given)

	token, err := svc.Sign("alice", nil)
	require.NoError(t, err)

	got, err := svc.Verify(token)
	require.NoError(t, err)
	assert.Empty(t, got.CustomClaims)
	assert.NotNil(t, got.CustomClaims)
}
