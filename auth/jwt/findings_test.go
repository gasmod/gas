package jwt_test

import (
	"testing"
	"time"

	"github.com/gasmod/gas"
	jwtpkg "github.com/gasmod/gas/auth/jwt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Finding #8: SignWithExpiry does not validate expiry is positive
// ---------------------------------------------------------------------------

func TestSignWithExpiry_ZeroDuration_ProducesAlreadyExpiredToken(t *testing.T) {
	svc := newHS256Service(t, "this-is-a-test-secret-32-bytes!!")

	// SignWithExpiry accepts zero — no validation.
	tokenStr, err := svc.SignWithExpiry("user-1", nil, 0)
	require.NoError(t, err, "SignWithExpiry does not reject zero expiry")
	assert.NotEmpty(t, tokenStr)

	// The token was issued with exp == iat, so it's already expired.
	_, verifyErr := svc.Verify(tokenStr)
	assert.Error(t, verifyErr, "token with zero expiry should fail verification")
}

func TestSignWithExpiry_NegativeDuration_ProducesAlreadyExpiredToken(t *testing.T) {
	svc := newHS256Service(t, "this-is-a-test-secret-32-bytes!!")

	// SignWithExpiry accepts negative — no validation.
	tokenStr, err := svc.SignWithExpiry("user-1", nil, -5*time.Minute)
	require.NoError(t, err, "SignWithExpiry does not reject negative expiry")
	assert.NotEmpty(t, tokenStr)

	// The token has exp in the past.
	_, verifyErr := svc.Verify(tokenStr)
	assert.Error(t, verifyErr, "token with negative expiry should fail verification")
}

// ---------------------------------------------------------------------------
// Finding: Sign method with explicit config uses validated expiry, but
// SignWithExpiry bypasses that validation entirely.
// ---------------------------------------------------------------------------

func TestSignUsesValidatedExpiry_SignWithExpiryBypasses(t *testing.T) {
	cfg := &jwtpkg.Config{
		JWT: jwtpkg.Settings{
			SigningKey:    "this-is-a-test-secret-32-bytes!!",
			SigningMethod: "HS256",
			Expiry:        15 * time.Minute,
		},
	}

	// Config.Validate rejects non-positive expiry.
	cfg.JWT.Expiry = -1 * time.Second
	assert.Error(t, cfg.Validate(), "Config.Validate rejects negative expiry")

	cfg.JWT.Expiry = 0
	assert.Error(t, cfg.Validate(), "Config.Validate rejects zero expiry")

	// But a running service with valid config still allows negative expiry via SignWithExpiry.
	cfg.JWT.Expiry = 15 * time.Minute
	svc := jwtpkg.New(jwtpkg.WithConfig(cfg))(&mockConfigProvider{}, gas.NewNopLogger()())
	require.NoError(t, svc.Init())

	_, err := svc.SignWithExpiry("user-1", nil, -1*time.Hour)
	assert.NoError(t, err, "SignWithExpiry does not validate its expiry parameter")
}
