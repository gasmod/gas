package jwt_test

import (
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	gojwt "github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth"
	"github.com/gasmod/gas/auth/internal/testutil"
	"github.com/gasmod/gas/auth/jwt"
)

// The example in the package documentation's Usage section.
func TestDoc_Usage(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	key := testKey
	logger := &gas.NopLogger{}
	r := request()

	svc, err := jwt.New(jwt.WithConfig(&jwt.Config{JWT: jwt.Settings{
		SigningMethod: jwt.MethodHS256,
		SigningKey:    key, // at least 32 bytes
		Expiry:        15 * time.Minute,
	}}))(nil, logger)
	require.NoError(t, err)
	require.NoError(t, svc.Init(ctx))

	token, err := svc.Sign("user-42", map[string]any{"role": "admin"})
	require.NoError(t, err)

	r.Header.Set("Authorization", "Bearer "+token)

	p, err := svc.Authenticate(ctx, r)
	require.NoError(t, err)

	assert.Equal(t, "user-42", p.Subject())
	assert.Equal(t, auth.SchemeJWT, p.Scheme())
	assert.Equal(t, unverified(t, token)["jti"], p.CredentialID())
	assert.Equal(t, gas.BasePrincipalMetadata{"role": "admin"}, p.Metadata())
}

// The error messages the package documentation lists are the ones the
// service returns, and it lists every message this test makes.
//
//nolint:funlen // one case per documented message.
func TestDoc_Errors(t *testing.T) {
	t.Parallel()

	svc := newService(t, hsConfig())
	checked := newService(t, hsConfig(func(s *jwt.Settings) {
		s.Issuer = "auth.example"
		s.Audience = "api.example"
	}))

	small := writePKIX(t, &newRSAKey(t, 1024).PublicKey)
	pub, _ := rsaFiles(t, rsaKey2048())
	_, otherPriv := rsaFiles(t, newRSAKey(t, 2048))
	verifyOnly := newService(t, rsConfig(pub, ""))

	// verify returns the error of Verify for a token with claims changed
	// by edit, at svc.
	verify := func(svc *jwt.Service, edit func(gojwt.MapClaims)) error {
		_, err := svc.Verify(mintHS(t, claims(edit)))

		return err
	}

	// sign returns the error of SignWithExpiry at svc.
	sign := func(svc *jwt.Service, subject string, c map[string]any, expiry time.Duration) error {
		_, err := svc.SignWithExpiry(subject, c, expiry)

		return err
	}

	// authenticateErr returns the error of Authenticate for a request with
	// the Authorization headers given.
	authenticateErr := func(authorization ...string) error {
		_, err := svc.Authenticate(t.Context(), request(authorization...))

		return err
	}

	future := time.Now().Add(time.Hour).Unix()
	cases := map[string]func() error{
		"gas/auth/jwt: unauthenticated: credentials expired: token expired": func() error {
			return verify(
				svc,
				func(c gojwt.MapClaims) { c["exp"] = time.Now().Add(-time.Minute).Unix() },
			)
		},
		"gas/auth/jwt: unauthenticated: invalid token: malformed": func() error {
			_, err := svc.Verify("garbage")

			return err
		},
		"gas/auth/jwt: unauthenticated: invalid token: unknown or missing algorithm": func() error {
			body := strings.SplitN(mintHS(t, claims(nil)), ".", 2)[1]
			_, err := svc.Verify(b64(`{"typ":"JWT"}`) + "." + body)

			return err
		},
		"gas/auth/jwt: unauthenticated: invalid token: bad signature or unexpected algorithm": func() error {
			_, err := svc.Verify(mint(t, gojwt.SigningMethodHS256, []byte(otherKey), claims(nil)))

			return err
		},
		"gas/auth/jwt: unauthenticated: invalid token: missing required claim": func() error {
			return verify(svc, func(c gojwt.MapClaims) { delete(c, "sub") })
		},
		"gas/auth/jwt: unauthenticated: invalid token: claim of the wrong type": func() error {
			return verify(svc, func(c gojwt.MapClaims) { c["sub"] = 7 })
		},
		"gas/auth/jwt: unauthenticated: invalid token: issued in the future": func() error {
			return verify(svc, func(c gojwt.MapClaims) { c["iat"] = future })
		},
		"gas/auth/jwt: unauthenticated: invalid token: not valid yet": func() error {
			return verify(svc, func(c gojwt.MapClaims) { c["nbf"] = future })
		},
		"gas/auth/jwt: unauthenticated: invalid token: issuer not accepted": func() error {
			return verify(checked, issAud(m{"iss": "evil"}))
		},
		"gas/auth/jwt: unauthenticated: invalid token: audience not accepted": func() error {
			return verify(checked, issAud(m{"aud": "evil"}))
		},
		"gas/auth/jwt: unauthenticated: invalid token: more than one bearer token": func() error {
			return authenticateErr("Bearer a", "Bearer b")
		},
		"gas/auth/jwt: no credentials": func() error {
			return authenticateErr()
		},
		"gas/auth/jwt: service not initialized": func() error {
			_, err := build(t, hsConfig()).Verify("token")

			return err
		},
		"gas/auth/jwt: logger is nil": func() error {
			_, err := jwt.New()(nil, nil)

			return err
		},
		"gas/auth/jwt: config is nil": func() error {
			return initErr(t, nil)
		},
		"gas/auth/jwt: invalid config: HS256 requires JWT.SigningKey of at least 32 bytes": func() error {
			return initErr(t, hsConfig(func(s *jwt.Settings) { s.SigningKey = "short" }))
		},
		"gas/auth/jwt: failed to load key JWT.PublicKeyPath: open: no such file or directory": func() error {
			return initErr(t, rsConfig(filepath.Join(t.TempDir(), "missing.pem"), ""))
		},
		"gas/auth/jwt: failed to load key JWT.PublicKeyPath: key too small: " +
			"RSA key has 1024 bits, fewer than 2048": func() error {
			return initErr(t, rsConfig(small, ""))
		},
		"gas/auth/jwt: failed to load key JWT.PrivateKeyPath: key does not match JWT.PublicKeyPath": func() error {
			return initErr(t, rsConfig(pub, otherPriv))
		},
		"gas/auth/jwt: signing key not configured": func() error {
			_, err := verifyOnly.Sign("alice", nil)

			return err
		},
		"gas/auth/jwt: empty subject": func() error {
			return sign(svc, "", nil, time.Minute)
		},
		"gas/auth/jwt: value too long: subject is longer than 255 bytes": func() error {
			return sign(svc, strings.Repeat("s", 256), nil, time.Minute)
		},
		"gas/auth/jwt: invalid ttl: must be positive": func() error {
			return sign(svc, "alice", nil, 0)
		},
		`gas/auth/jwt: reserved claim "sub"`: func() error {
			return sign(svc, "alice", m{"sub": "bob"}, time.Minute)
		},
		`gas/auth/jwt: invalid claim "ch": value cannot be encoded as JSON`: func() error {
			return sign(svc, "alice", m{"ch": make(chan int)}, time.Minute)
		},
	}

	assert.ElementsMatch(t,
		testutil.DocLines(t, "../doc.go", jwt.ServiceName+": "),
		slices.Collect(maps.Keys(cases)))

	for msg, produce := range cases {
		assert.EqualError(t, produce(), msg)
	}
}
