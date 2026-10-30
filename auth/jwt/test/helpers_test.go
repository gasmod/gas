package jwt_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	gojwt "github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth/jwt"
)

// testKey is a 32-byte HS256 signing key.
const testKey = "0123456789abcdef0123456789abcdef"

// otherKey is another 32-byte HS256 signing key.
const otherKey = "fedcba9876543210fedcba9876543210"

// m is a shorthand for claim maps.
type m = map[string]any

// hsConfig returns an HS256 configuration with testKey, changed by edit.
func hsConfig(edit ...func(*jwt.Settings)) *jwt.Config {
	cfg := &jwt.Config{JWT: jwt.Settings{
		SigningMethod: jwt.MethodHS256,
		SigningKey:    testKey,
		Expiry:        15 * time.Minute,
	}}

	for _, e := range edit {
		e(&cfg.JWT)
	}

	return cfg
}

// rsConfig returns an RS256 configuration with the given key files.
func rsConfig(pub, priv string) *jwt.Config {
	return &jwt.Config{JWT: jwt.Settings{
		SigningMethod:  jwt.MethodRS256,
		PublicKeyPath:  pub,
		PrivateKeyPath: priv,
		Expiry:         15 * time.Minute,
	}}
}

// build returns a Service with cfg, not initialized.
func build(tb testing.TB, cfg *jwt.Config) *jwt.Service {
	tb.Helper()

	svc, err := jwt.New(jwt.WithConfig(cfg))(nil, &gas.NopLogger{})
	require.NoError(tb, err)

	return svc
}

// newService returns a Service with cfg, initialized.
func newService(tb testing.TB, cfg *jwt.Config) *jwt.Service {
	tb.Helper()

	svc := build(tb, cfg)
	require.NoError(tb, svc.Init(tb.Context()))

	return svc
}

// initErr returns the error of initializing a Service with cfg.
func initErr(tb testing.TB, cfg *jwt.Config) error {
	tb.Helper()

	return build(tb, cfg).Init(tb.Context())
}

// rsaKey2048 is one 2048-bit key shared by the tests, since generating
// one takes a while.
var rsaKey2048 = sync.OnceValue(func() *rsa.PrivateKey {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}

	return key
})

// newRSAKey returns a new RSA key of bits.
func newRSAKey(tb testing.TB, bits int) *rsa.PrivateKey {
	tb.Helper()

	key, err := rsa.GenerateKey(rand.Reader, bits)
	require.NoError(tb, err)

	return key
}

// writeFile writes data to a new file in a temporary directory and
// returns its path.
func writeFile(tb testing.TB, data []byte) string {
	tb.Helper()

	path := filepath.Join(tb.TempDir(), "key.pem")
	require.NoError(tb, os.WriteFile(path, data, 0o600))

	return path
}

// writePEM writes one PEM block of typ holding der and returns the path.
func writePEM(tb testing.TB, typ string, der []byte) string {
	tb.Helper()

	return writeFile(tb, pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}))
}

// writePKIX writes pub as a PKIX `PUBLIC KEY` and returns the path.
func writePKIX(tb testing.TB, pub any) string {
	tb.Helper()

	der, err := x509.MarshalPKIXPublicKey(pub)
	require.NoError(tb, err)

	return writePEM(tb, "PUBLIC KEY", der)
}

// writePKCS8 writes priv as a PKCS #8 `PRIVATE KEY` and returns the path.
func writePKCS8(tb testing.TB, priv any) string {
	tb.Helper()

	der, err := x509.MarshalPKCS8PrivateKey(priv)
	require.NoError(tb, err)

	return writePEM(tb, "PRIVATE KEY", der)
}

// rsaFiles writes key's public half as PKIX and the key as PKCS #8, and
// returns their paths.
func rsaFiles(tb testing.TB, key *rsa.PrivateKey) (pub, priv string) {
	tb.Helper()

	return writePKIX(tb, &key.PublicKey), writePKCS8(tb, key)
}

// claims returns claims that pass every check: `sub`, `iat` now and `exp`
// in an hour, changed by edit.
func claims(edit func(gojwt.MapClaims)) gojwt.MapClaims {
	now := time.Now()
	c := gojwt.MapClaims{
		"sub": "alice",
		"iat": now.Unix(),
		"exp": now.Add(time.Hour).Unix(),
	}

	if edit != nil {
		edit(c)
	}

	return c
}

// mint signs c with method and key directly, bypassing the service.
func mint(tb testing.TB, method gojwt.SigningMethod, key any, c gojwt.MapClaims) string {
	tb.Helper()

	token, err := gojwt.NewWithClaims(method, c).SignedString(key)
	require.NoError(tb, err)

	return token
}

// mintHS signs c with HS256 and testKey.
func mintHS(tb testing.TB, c gojwt.MapClaims) string {
	tb.Helper()

	return mint(tb, gojwt.SigningMethodHS256, []byte(testKey), c)
}

// unverified returns the claims of token without checking it.
func unverified(tb testing.TB, token string) gojwt.MapClaims {
	tb.Helper()

	c := gojwt.MapClaims{}
	_, _, err := gojwt.NewParser().ParseUnverified(token, c)
	require.NoError(tb, err)

	return c
}

// request returns a GET with the given Authorization header values.
func request(authorization ...string) *http.Request {
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	for _, v := range authorization {
		r.Header.Add("Authorization", v)
	}

	return r
}
