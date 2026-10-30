package jwt_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"io/fs"
	"strings"
	"testing"

	gojwt "github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/auth/jwt"
)

// secret is text written to key files, which errors must not quote.
const secret = "hunter2-key-material"

// RS256 signs and verifies with a PKIX public key and a PKCS #8 private
// key, or PKCS #1 for both.
func TestRS256_KeyFormats(t *testing.T) {
	t.Parallel()

	key := rsaKey2048()

	pkixPub, pkcs8Priv := rsaFiles(t, key)
	pkcs1Pub := writePEM(t, "RSA PUBLIC KEY", x509.MarshalPKCS1PublicKey(&key.PublicKey))
	pkcs1Priv := writePEM(t, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(key))

	for _, pair := range [][2]string{{pkixPub, pkcs8Priv}, {pkcs1Pub, pkcs1Priv}} {
		svc := newService(t, rsConfig(pair[0], pair[1]))

		token, err := svc.Sign("alice", m{"role": "viewer"})
		require.NoError(t, err)
		assert.Equal(t, "RS256", headerAlg(t, token))

		got, err := svc.Verify(token)
		require.NoError(t, err)
		assert.Equal(t, "alice", got.Subject)
		assert.Equal(t, "viewer", got.CustomClaims["role"])
	}
}

// headerAlg returns the `alg` of token's header.
func headerAlg(t *testing.T, token string) string {
	t.Helper()

	parsed, _, err := gojwt.NewParser().ParseUnverified(token, gojwt.MapClaims{})
	require.NoError(t, err)

	alg, _ := parsed.Header["alg"].(string)

	return alg
}

// RS256 with only a public key verifies and authenticates, and Sign
// returns ErrSigningKeyNotConfigured.
func TestRS256_VerifyOnly(t *testing.T) {
	t.Parallel()

	pub, priv := rsaFiles(t, rsaKey2048())
	signer := newService(t, rsConfig(pub, priv))
	verifier := newService(t, rsConfig(pub, ""))

	token, err := signer.Sign("alice", nil)
	require.NoError(t, err)

	got, err := verifier.Verify(token)
	require.NoError(t, err)
	assert.Equal(t, "alice", got.Subject)

	p, err := verifier.Authenticate(t.Context(), request("Bearer "+token))
	require.NoError(t, err)
	assert.Equal(t, "alice", p.Subject())

	_, err = verifier.Sign("alice", nil)
	require.ErrorIs(t, err, jwt.ErrSigningKeyNotConfigured)
	require.EqualError(t, err, "gas/auth/jwt: signing key not configured")

	_, err = verifier.SignWithExpiry("alice", nil, 1)
	require.ErrorIs(t, err, jwt.ErrSigningKeyNotConfigured)
}

// An RSA key under 2048 bits is rejected, public or private.
func TestRS256_SmallKeys(t *testing.T) {
	t.Parallel()

	small := newRSAKey(t, 1024)
	smallPub, smallPriv := rsaFiles(t, small)
	bigPub, _ := rsaFiles(t, rsaKey2048())

	err := initErr(t, rsConfig(smallPub, ""))
	require.ErrorIs(t, err, jwt.ErrKeyLoadFailed)
	require.ErrorIs(t, err, jwt.ErrKeyTooSmall)
	require.EqualError(t, err, "gas/auth/jwt: failed to load key JWT.PublicKeyPath: "+
		"key too small: RSA key has 1024 bits, fewer than 2048")

	err = initErr(t, rsConfig(bigPub, smallPriv))
	require.ErrorIs(t, err, jwt.ErrKeyTooSmall)
	require.EqualError(t, err, "gas/auth/jwt: failed to load key JWT.PrivateKeyPath: "+
		"key too small: RSA key has 1024 bits, fewer than 2048")

	pkcs1 := writePEM(t, "RSA PUBLIC KEY", x509.MarshalPKCS1PublicKey(&small.PublicKey))
	require.ErrorIs(t, initErr(t, rsConfig(pkcs1, "")), jwt.ErrKeyTooSmall)

	pkcs1Priv := writePEM(t, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(small))
	require.ErrorIs(t, initErr(t, rsConfig(bigPub, pkcs1Priv)), jwt.ErrKeyTooSmall)
}

// A private key that does not belong to the public key fails Init, so the
// service never signs a token it would reject.
func TestRS256_KeyMismatch(t *testing.T) {
	t.Parallel()

	pub, _ := rsaFiles(t, rsaKey2048())
	_, priv := rsaFiles(t, newRSAKey(t, 2048))

	err := initErr(t, rsConfig(pub, priv))
	require.ErrorIs(t, err, jwt.ErrKeyLoadFailed)
	require.EqualError(t, err,
		"gas/auth/jwt: failed to load key JWT.PrivateKeyPath: key does not match JWT.PublicKeyPath")
}

// Key file errors name the setting and never include the path or the
// file's contents; a missing file matches fs.ErrNotExist.
func TestRS256_KeyFileErrors(t *testing.T) {
	t.Parallel()

	tests := keyFileCases(t)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := initErr(t, tt.cfg)
			require.ErrorIs(t, err, jwt.ErrKeyLoadFailed)
			require.EqualError(t, err, "gas/auth/jwt: failed to load key "+tt.want)

			if tt.is != nil {
				require.ErrorIs(t, err, tt.is)
			}

			for _, path := range []string{tt.cfg.JWT.PublicKeyPath, tt.cfg.JWT.PrivateKeyPath} {
				if path != "" {
					assert.NotContains(t, err.Error(), path)
				}
			}

			assert.NotContains(t, err.Error(), secret)
		})
	}
}

// An RS256 service rejects an HS256 token signed with its public key's
// bytes, the classic algorithm confusion, and an HS256 service rejects an
// RS256 token.
func TestAlgorithmConfusion_RSAAndHMAC(t *testing.T) {
	t.Parallel()

	key := rsaKey2048()
	pub, priv := rsaFiles(t, key)
	rs := newService(t, rsConfig(pub, priv))

	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	require.NoError(t, err)

	forged := mint(t, gojwt.SigningMethodHS256, der, claims(nil))

	_, err = rs.Verify(forged)
	require.EqualError(
		t,
		err,
		"gas/auth/jwt: unauthenticated: invalid token: bad signature or unexpected algorithm",
	)

	rsToken := mint(t, gojwt.SigningMethodRS256, key, claims(nil))

	_, err = newService(t, hsConfig()).Verify(rsToken)
	require.EqualError(
		t,
		err,
		"gas/auth/jwt: unauthenticated: invalid token: bad signature or unexpected algorithm",
	)

	_, err = rs.Verify(rsToken)
	require.NoError(t, err)
}

// keyFileCase is a configuration with a bad key file, the error it
// matches, if any, and the message after the setting's prefix.
type keyFileCase struct {
	cfg  *jwt.Config
	is   error
	name string
	want string
}

// The messages of a key that is not of the expected kind, or not there.
const (
	notPublic  = "not an RSA public key in PKIX or PKCS #1 form"
	notPrivate = "not an unencrypted RSA private key in PKCS #8 or PKCS #1 form"
	noFile     = "open: no such file or directory"
)

// keyFileCases returns the cases of TestRS256_KeyFileErrors, writing their
// files to temporary directories.
func keyFileCases(tb testing.TB) []keyFileCase {
	tb.Helper()

	pub, priv := rsaFiles(tb, rsaKey2048())
	missing := tb.TempDir() + "/missing.pem"

	return append([]keyFileCase{
		{
			name: "missing public key",
			cfg:  rsConfig(missing, ""),
			is:   fs.ErrNotExist,
			want: "JWT.PublicKeyPath: " + noFile,
		},
		{
			name: "missing private key",
			cfg:  rsConfig(pub, missing),
			is:   fs.ErrNotExist,
			want: "JWT.PrivateKeyPath: " + noFile,
		},
		{
			name: "directory",
			cfg:  rsConfig(tb.TempDir(), ""),
			want: "JWT.PublicKeyPath: read: is a directory",
		},
		{
			name: "private key as public",
			cfg:  rsConfig(priv, ""),
			want: "JWT.PublicKeyPath: " + notPublic,
		},
		{
			name: "public key as private",
			cfg:  rsConfig(pub, pub),
			want: "JWT.PrivateKeyPath: " + notPrivate,
		},
	}, keyContentCases(tb, pub)...)
}

// keyContentCases returns the cases of TestRS256_KeyFileErrors whose files
// exist but do not hold a usable key, with pub a valid public key file.
func keyContentCases(tb testing.TB, pub string) []keyFileCase {
	tb.Helper()

	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(tb, err)

	large := writeFile(tb, []byte(strings.Repeat("A", 64<<10+1)))
	noPEM := writeFile(tb, []byte(secret))
	garbage := writePEM(tb, "PUBLIC KEY", []byte(secret))

	return []keyFileCase{
		{
			name: "file over 64 KiB",
			cfg:  rsConfig(large, ""),
			want: "JWT.PublicKeyPath: file is larger than 64 KiB",
		},
		{
			name: "no PEM block",
			cfg:  rsConfig(noPEM, ""),
			want: "JWT.PublicKeyPath: no PEM block found",
		},
		{name: "not a key", cfg: rsConfig(garbage, ""), want: "JWT.PublicKeyPath: " + notPublic},
		{
			name: "EC public key",
			cfg:  rsConfig(writePKIX(tb, &ec.PublicKey), ""),
			want: "JWT.PublicKeyPath: " + notPublic,
		},
		{
			name: "EC private key",
			cfg:  rsConfig(pub, writePKCS8(tb, ec)),
			want: "JWT.PrivateKeyPath: " + notPrivate,
		},
	}
}
