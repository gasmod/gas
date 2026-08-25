package jwt_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gasmod/gas"
	auth "github.com/gasmod/gas/auth"
	jwtpkg "github.com/gasmod/gas/auth/jwt"
	config "github.com/gasmod/gas/config"
	gojwt "github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Mock ConfigProvider
// ---------------------------------------------------------------------------

type mockConfigProvider struct{}

func (m *mockConfigProvider) SetDefault(_ string, _ any)               {}
func (m *mockConfigProvider) SetDefaults(_ any) error                  { return nil }
func (m *mockConfigProvider) Set(_ string, _ any)                      {}
func (m *mockConfigProvider) Get(_ string) any                         { return nil }
func (m *mockConfigProvider) Find(_ string) (any, bool)                { return nil, false }
func (m *mockConfigProvider) Values() map[string]any                   { return nil }
func (m *mockConfigProvider) Bind(_ any, _ ...config.BindOption) error { return nil }

var _ gas.ConfigProvider = (*mockConfigProvider)(nil)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// newHS256Service creates and initializes a JWT service with HS256 defaults.
func newHS256Service(t *testing.T, key string, opts ...func(*jwtpkg.Config)) *jwtpkg.Service {
	t.Helper()
	cfg := &jwtpkg.Config{
		JWT: jwtpkg.Settings{
			SigningKey:    key,
			SigningMethod: "HS256",
			Expiry:        15 * time.Minute,
		},
	}
	for _, fn := range opts {
		fn(cfg)
	}
	svc := jwtpkg.New(jwtpkg.WithConfig(cfg))(&mockConfigProvider{}, gas.NewNopLogger()())
	require.NoError(t, svc.Init())
	return svc
}

// writeRSAKeys generates a 2048-bit RSA key pair, writes them to temp files,
// and returns the file paths (public, private).
func writeRSAKeys(t *testing.T) (pubPath, privPath string) {
	t.Helper()

	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	dir := t.TempDir()
	pubPath = filepath.Join(dir, "pub.pem")
	privPath = filepath.Join(dir, "priv.pem")

	// Marshal public key.
	pubDER, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	require.NoError(t, err)
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})
	require.NoError(t, os.WriteFile(pubPath, pubPEM, 0o600))

	// Marshal private key (PKCS8).
	privDER, err := x509.MarshalPKCS8PrivateKey(priv)
	require.NoError(t, err)
	privPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER})
	require.NoError(t, os.WriteFile(privPath, privPEM, 0o600))

	return pubPath, privPath
}

// newRS256Service creates and initializes a JWT service backed by RSA keys.
func newRS256Service(t *testing.T, opts ...func(*jwtpkg.Config)) *jwtpkg.Service {
	t.Helper()
	pubPath, privPath := writeRSAKeys(t)
	cfg := &jwtpkg.Config{
		JWT: jwtpkg.Settings{
			SigningMethod:  "RS256",
			PublicKeyPath:  pubPath,
			PrivateKeyPath: privPath,
			Expiry:         15 * time.Minute,
		},
	}
	for _, fn := range opts {
		fn(cfg)
	}
	svc := jwtpkg.New(jwtpkg.WithConfig(cfg))(&mockConfigProvider{}, gas.NewNopLogger()())
	require.NoError(t, svc.Init())
	return svc
}

// ---------------------------------------------------------------------------
// Config Validation
// ---------------------------------------------------------------------------

func TestConfigValidation(t *testing.T) {
	t.Run("default config with signing key set validates", func(t *testing.T) {
		cfg := jwtpkg.DefaultConfig()
		cfg.JWT.SigningKey = "test-secret-key-that-is-32-bytes"
		assert.NoError(t, cfg.Validate())
	})

	t.Run("HS256 with empty signing key errors", func(t *testing.T) {
		cfg := &jwtpkg.Config{
			JWT: jwtpkg.Settings{
				SigningMethod: "HS256",
				Expiry:        15 * time.Minute,
			},
		}
		err := cfg.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "signing key is required")
	})

	t.Run("RS256 with empty public key path errors", func(t *testing.T) {
		cfg := &jwtpkg.Config{
			JWT: jwtpkg.Settings{
				SigningMethod: "RS256",
				Expiry:        15 * time.Minute,
			},
		}
		err := cfg.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "public key path is required")
	})

	t.Run("HS256 with short signing key errors", func(t *testing.T) {
		cfg := &jwtpkg.Config{
			JWT: jwtpkg.Settings{
				SigningMethod: "HS256",
				SigningKey:    "too-short",
				Expiry:        15 * time.Minute,
			},
		}
		err := cfg.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "at least 32 bytes")
	})

	t.Run("zero expiry errors", func(t *testing.T) {
		cfg := &jwtpkg.Config{
			JWT: jwtpkg.Settings{
				SigningMethod: "HS256",
				SigningKey:    "test-secret-key-that-is-32-bytes",
				Expiry:        0,
			},
		}
		err := cfg.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "expiry must be positive")
	})

	t.Run("negative expiry errors", func(t *testing.T) {
		cfg := &jwtpkg.Config{
			JWT: jwtpkg.Settings{
				SigningMethod: "HS256",
				SigningKey:    "test-secret-key-that-is-32-bytes",
				Expiry:        -5 * time.Minute,
			},
		}
		err := cfg.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "expiry must be positive")
	})

	t.Run("unknown signing method PS256 errors", func(t *testing.T) {
		cfg := &jwtpkg.Config{
			JWT: jwtpkg.Settings{
				SigningMethod: "PS256",
				Expiry:        15 * time.Minute,
			},
		}
		err := cfg.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unsupported signing method")
	})
}

// ---------------------------------------------------------------------------
// Sign + Verify (HS256)
// ---------------------------------------------------------------------------

func TestSignVerifyHS256(t *testing.T) {
	const secret = "my-test-secret-key-for-hs256!!!!"

	t.Run("round-trip with custom claims", func(t *testing.T) {
		svc := newHS256Service(t, secret)
		customClaims := map[string]any{
			"role":  "admin",
			"email": "user@example.com",
		}
		token, err := svc.Sign("user-123", customClaims)
		require.NoError(t, err)
		require.NotEmpty(t, token)

		claims, err := svc.Verify(token)
		require.NoError(t, err)
		assert.Equal(t, "user-123", claims.Subject)
		assert.Equal(t, "admin", claims.CustomClaims["role"])
		assert.Equal(t, "user@example.com", claims.CustomClaims["email"])
		assert.False(t, claims.ExpiresAt.IsZero())
		assert.False(t, claims.IssuedAt.IsZero())
	})

	t.Run("verify expired token returns ErrTokenExpired", func(t *testing.T) {
		svc := newHS256Service(t, secret)
		token, err := svc.SignWithExpiry("user-123", nil, -1*time.Hour)
		require.NoError(t, err)

		_, err = svc.Verify(token)
		require.Error(t, err)
		assert.True(t, errors.Is(err, gojwt.ErrTokenExpired), "expected ErrTokenExpired, got: %v", err)
	})

	t.Run("verify with wrong signing key rejects", func(t *testing.T) {
		svc1 := newHS256Service(t, secret)
		svc2 := newHS256Service(t, "different-secret-key-for-test32b!")

		token, err := svc1.Sign("user-123", nil)
		require.NoError(t, err)

		_, err = svc2.Verify(token)
		require.Error(t, err)
	})

	t.Run("sign with nil claims does not panic", func(t *testing.T) {
		svc := newHS256Service(t, secret)
		token, err := svc.Sign("user-456", nil)
		require.NoError(t, err)
		require.NotEmpty(t, token)

		claims, err := svc.Verify(token)
		require.NoError(t, err)
		assert.Equal(t, "user-456", claims.Subject)
	})

	t.Run("custom claims must not overwrite standard claims", func(t *testing.T) {
		svc := newHS256Service(t, secret)
		evilClaims := map[string]any{
			"sub": "evil",
			"exp": gojwt.NewNumericDate(time.Now().Add(100 * 365 * 24 * time.Hour)),
			"iss": "attacker",
		}
		token, err := svc.Sign("legitimate-user", evilClaims)
		require.NoError(t, err)

		claims, err := svc.Verify(token)
		require.NoError(t, err)
		assert.Equal(t, "legitimate-user", claims.Subject,
			"standard 'sub' claim must not be overwritten by custom claims")
		assert.True(t, claims.ExpiresAt.Before(time.Now().Add(16*time.Minute)),
			"standard 'exp' claim must not be overwritten by custom claims")
	})

	t.Run("SignWithExpiry with zero duration expires immediately", func(t *testing.T) {
		svc := newHS256Service(t, secret)
		token, err := svc.SignWithExpiry("user-123", nil, 0)
		require.NoError(t, err)

		// Token with exp == iat should be expired or just at the boundary.
		// Sleep a tiny bit to ensure it's past.
		time.Sleep(1 * time.Millisecond)
		_, err = svc.Verify(token)
		require.Error(t, err)
		assert.True(t, errors.Is(err, gojwt.ErrTokenExpired), "expected ErrTokenExpired, got: %v", err)
	})

	t.Run("SignWithExpiry with negative duration is already expired", func(t *testing.T) {
		svc := newHS256Service(t, secret)
		token, err := svc.SignWithExpiry("user-123", nil, -5*time.Minute)
		require.NoError(t, err)

		_, err = svc.Verify(token)
		require.Error(t, err)
		assert.True(t, errors.Is(err, gojwt.ErrTokenExpired), "expected ErrTokenExpired, got: %v", err)
	})

	t.Run("verify empty string errors", func(t *testing.T) {
		svc := newHS256Service(t, secret)
		_, err := svc.Verify("")
		require.Error(t, err)
	})

	t.Run("verify garbage string errors", func(t *testing.T) {
		svc := newHS256Service(t, secret)
		_, err := svc.Verify("this-is-not-a-jwt")
		require.Error(t, err)
	})

	t.Run("verify valid base64 but invalid JWT errors", func(t *testing.T) {
		svc := newHS256Service(t, secret)
		b64 := base64.RawURLEncoding.EncodeToString([]byte("not-jwt-header"))
		fakeToken := b64 + "." + b64 + "." + b64
		_, err := svc.Verify(fakeToken)
		require.Error(t, err)
	})

	t.Run("sign with nil signing key errors", func(t *testing.T) {
		// Create a service with valid config but then try to sign without
		// having a signing key. We can achieve this by using RS256 with only
		// a public key (no private key).
		pubPath, _ := writeRSAKeys(t)
		cfg := &jwtpkg.Config{
			JWT: jwtpkg.Settings{
				SigningMethod: "RS256",
				PublicKeyPath: pubPath,
				// No PrivateKeyPath → signingKey stays nil
				Expiry: 15 * time.Minute,
			},
		}
		svc := jwtpkg.New(jwtpkg.WithConfig(cfg))(&mockConfigProvider{}, gas.NewNopLogger()())

		err := svc.Init()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "signing key not configured")
	})
}

// ---------------------------------------------------------------------------
// Algorithm Confusion (Security)
// ---------------------------------------------------------------------------

func TestAlgorithmConfusion(t *testing.T) {
	const secret = "hmac-secret-key-for-testing-32b!"

	t.Run("token with alg none is rejected", func(t *testing.T) {
		svc := newHS256Service(t, secret)

		// Craft a token with alg "none".
		token := gojwt.NewWithClaims(gojwt.SigningMethodNone, gojwt.MapClaims{
			"sub": "attacker",
			"exp": gojwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
		})
		tokenStr, err := token.SignedString(gojwt.UnsafeAllowNoneSignatureType)
		require.NoError(t, err)

		_, err = svc.Verify(tokenStr)
		require.Error(t, err, "alg=none must be rejected by WithValidMethods")
	})

	t.Run("HS256 token verified with different key is rejected", func(t *testing.T) {
		svc := newHS256Service(t, secret)

		// Sign with a different key using the raw library.
		otherKey := []byte("other-secret")
		token := gojwt.NewWithClaims(gojwt.SigningMethodHS256, gojwt.MapClaims{
			"sub": "attacker",
			"exp": gojwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
		})
		tokenStr, err := token.SignedString(otherKey)
		require.NoError(t, err)

		_, err = svc.Verify(tokenStr)
		require.Error(t, err, "token signed with different key must be rejected")
	})

	t.Run("RS256 sign and verify round-trip", func(t *testing.T) {
		svc := newRS256Service(t)

		token, err := svc.Sign("rsa-user", map[string]any{"role": "viewer"})
		require.NoError(t, err)
		require.NotEmpty(t, token)

		claims, err := svc.Verify(token)
		require.NoError(t, err)
		assert.Equal(t, "rsa-user", claims.Subject)
		assert.Equal(t, "viewer", claims.CustomClaims["role"])
	})
}

// ---------------------------------------------------------------------------
// Issuer / Audience Validation
// ---------------------------------------------------------------------------

func TestIssuerAudience(t *testing.T) {
	const secret = "iss-aud-secret-for-testing-32b!!"

	t.Run("sign with issuer configured includes iss claim", func(t *testing.T) {
		svc := newHS256Service(t, secret, func(cfg *jwtpkg.Config) {
			cfg.JWT.Issuer = "my-app"
		})

		token, err := svc.Sign("user-1", nil)
		require.NoError(t, err)

		claims, err := svc.Verify(token)
		require.NoError(t, err)
		assert.Equal(t, "my-app", claims.Issuer)
	})

	t.Run("verify with issuer mismatch rejects", func(t *testing.T) {
		signer := newHS256Service(t, secret, func(cfg *jwtpkg.Config) {
			cfg.JWT.Issuer = "app-A"
		})
		verifier := newHS256Service(t, secret, func(cfg *jwtpkg.Config) {
			cfg.JWT.Issuer = "app-B"
		})

		token, err := signer.Sign("user-1", nil)
		require.NoError(t, err)

		_, err = verifier.Verify(token)
		require.Error(t, err, "issuer mismatch must be rejected")
	})

	t.Run("sign with audience configured includes aud claim", func(t *testing.T) {
		svc := newHS256Service(t, secret, func(cfg *jwtpkg.Config) {
			cfg.JWT.Audience = "my-api"
		})

		token, err := svc.Sign("user-1", nil)
		require.NoError(t, err)

		claims, err := svc.Verify(token)
		require.NoError(t, err)
		require.NotEmpty(t, claims.Audience)
		assert.Contains(t, claims.Audience, "my-api")
	})

	t.Run("verify with audience mismatch rejects", func(t *testing.T) {
		signer := newHS256Service(t, secret, func(cfg *jwtpkg.Config) {
			cfg.JWT.Audience = "api-A"
		})
		verifier := newHS256Service(t, secret, func(cfg *jwtpkg.Config) {
			cfg.JWT.Audience = "api-B"
		})

		token, err := signer.Sign("user-1", nil)
		require.NoError(t, err)

		_, err = verifier.Verify(token)
		require.Error(t, err, "audience mismatch must be rejected")
	})
}

// ---------------------------------------------------------------------------
// Authenticate
// ---------------------------------------------------------------------------

func TestAuthenticate(t *testing.T) {
	const secret = "auth-secret-key-for-testing-32b!"
	svc := newHS256Service(t, secret)

	validToken, err := svc.Sign("auth-user", map[string]any{"role": "member"})
	require.NoError(t, err)

	expiredToken, err := svc.SignWithExpiry("auth-user", nil, -1*time.Hour)
	require.NoError(t, err)

	t.Run("no Authorization header returns ErrUnauthenticated", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		_, err := svc.Authenticate(context.Background(), r)
		require.Error(t, err)
		assert.ErrorIs(t, err, auth.ErrUnauthenticated)
	})

	t.Run("Bearer with trailing space and no token returns ErrUnauthenticated", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Authorization", "Bearer ")
		_, err := svc.Authenticate(context.Background(), r)
		require.Error(t, err)
		// "Bearer " splits into ["Bearer", ""] — Verify("") will fail, which maps
		// to ErrUnauthenticated because it's not ErrTokenExpired.
		assert.ErrorIs(t, err, auth.ErrUnauthenticated)
	})

	t.Run("lowercase bearer works (case-insensitive)", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Authorization", "bearer "+validToken)
		principal, err := svc.Authenticate(context.Background(), r)
		require.NoError(t, err)
		assert.Equal(t, "auth-user", principal.Subject())
		assert.Equal(t, "jwt", principal.Scheme())
	})

	t.Run("Basic scheme returns ErrUnauthenticated", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Authorization", "Basic "+validToken)
		_, err := svc.Authenticate(context.Background(), r)
		require.Error(t, err)
		assert.ErrorIs(t, err, auth.ErrUnauthenticated)
	})

	t.Run("BearerTOKEN without space returns ErrUnauthenticated", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Authorization", "Bearer"+validToken)
		_, err := svc.Authenticate(context.Background(), r)
		require.Error(t, err)
		assert.ErrorIs(t, err, auth.ErrUnauthenticated)
	})

	t.Run("valid Bearer token returns Principal with correct subject and scheme", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Authorization", "Bearer "+validToken)
		principal, err := svc.Authenticate(context.Background(), r)
		require.NoError(t, err)
		assert.Equal(t, "auth-user", principal.Subject())
		assert.Equal(t, "jwt", principal.Scheme())
	})

	t.Run("expired Bearer token returns ErrCredentialsExpired", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Authorization", "Bearer "+expiredToken)
		_, err := svc.Authenticate(context.Background(), r)
		require.Error(t, err)
		assert.ErrorIs(t, err, auth.ErrCredentialsExpired)
	})
}

// ---------------------------------------------------------------------------
// RSA Key Loading
// ---------------------------------------------------------------------------

func TestRSAKeyLoading(t *testing.T) {
	t.Run("non-existent public key file errors on Init", func(t *testing.T) {
		cfg := &jwtpkg.Config{
			JWT: jwtpkg.Settings{
				SigningMethod:  "RS256",
				PublicKeyPath:  "/tmp/does-not-exist-pub.pem",
				PrivateKeyPath: "/tmp/does-not-exist-priv.pem",
				Expiry:         15 * time.Minute,
			},
		}
		svc := jwtpkg.New(jwtpkg.WithConfig(cfg))(&mockConfigProvider{}, gas.NewNopLogger()())
		err := svc.Init()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "load public key")
	})

	t.Run("garbled PEM public key errors on Init", func(t *testing.T) {
		dir := t.TempDir()
		pubPath := filepath.Join(dir, "bad-pub.pem")
		require.NoError(t, os.WriteFile(pubPath, []byte("this is not PEM data"), 0o600))

		cfg := &jwtpkg.Config{
			JWT: jwtpkg.Settings{
				SigningMethod: "RS256",
				PublicKeyPath: pubPath,
				Expiry:        15 * time.Minute,
			},
		}
		svc := jwtpkg.New(jwtpkg.WithConfig(cfg))(&mockConfigProvider{}, gas.NewNopLogger()())
		err := svc.Init()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "decode PEM")
	})

	t.Run("garbled PEM private key errors on Init", func(t *testing.T) {
		pubPath, _ := writeRSAKeys(t) // valid pub key
		dir := t.TempDir()
		badPrivPath := filepath.Join(dir, "bad-priv.pem")
		require.NoError(t, os.WriteFile(badPrivPath, []byte("not PEM"), 0o600))

		cfg := &jwtpkg.Config{
			JWT: jwtpkg.Settings{
				SigningMethod:  "RS256",
				PublicKeyPath:  pubPath,
				PrivateKeyPath: badPrivPath,
				Expiry:         15 * time.Minute,
			},
		}
		svc := jwtpkg.New(jwtpkg.WithConfig(cfg))(&mockConfigProvider{}, gas.NewNopLogger()())
		err := svc.Init()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "decode PEM")
	})
}

// ---------------------------------------------------------------------------
// Service Interface
// ---------------------------------------------------------------------------

func TestServiceName(t *testing.T) {
	svc := newHS256Service(t, "some-key-that-is-at-least-32bytes")
	assert.Equal(t, "gas/auth/jwt", svc.Name())
}

func TestServiceClose(t *testing.T) {
	svc := newHS256Service(t, "some-key-that-is-at-least-32bytes")
	assert.NoError(t, svc.Close())
}
