package jwt_test

import (
	"encoding/base64"
	"slices"
	"strings"
	"testing"
	"time"

	gojwt "github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/auth"
	"github.com/gasmod/gas/auth/jwt"
)

// denials are the documented messages of every error Verify returns for a
// token.
var denials = []string{
	"gas/auth/jwt: unauthenticated: credentials expired: token expired",
	"gas/auth/jwt: unauthenticated: invalid token: malformed",
	"gas/auth/jwt: unauthenticated: invalid token: unknown or missing algorithm",
	"gas/auth/jwt: unauthenticated: invalid token: bad signature or unexpected algorithm",
	"gas/auth/jwt: unauthenticated: invalid token: missing required claim",
	"gas/auth/jwt: unauthenticated: invalid token: claim of the wrong type",
	"gas/auth/jwt: unauthenticated: invalid token: issued in the future",
	"gas/auth/jwt: unauthenticated: invalid token: not valid yet",
	"gas/auth/jwt: unauthenticated: invalid token: issuer not accepted",
	"gas/auth/jwt: unauthenticated: invalid token: audience not accepted",
	"gas/auth/jwt: unauthenticated: invalid token: more than one bearer token",
}

// Error messages for the reasons, as Verify returns them.
const (
	errMalformed  = "gas/auth/jwt: unauthenticated: invalid token: malformed"
	errAlgorithm  = "gas/auth/jwt: unauthenticated: invalid token: unknown or missing algorithm"
	errSignature  = "gas/auth/jwt: unauthenticated: invalid token: bad signature or unexpected algorithm"
	errMissing    = "gas/auth/jwt: unauthenticated: invalid token: missing required claim"
	errType       = "gas/auth/jwt: unauthenticated: invalid token: claim of the wrong type"
	errFuture     = "gas/auth/jwt: unauthenticated: invalid token: issued in the future"
	errNotYet     = "gas/auth/jwt: unauthenticated: invalid token: not valid yet"
	errIssuer     = "gas/auth/jwt: unauthenticated: invalid token: issuer not accepted"
	errAudience   = "gas/auth/jwt: unauthenticated: invalid token: audience not accepted"
	errExpired    = "gas/auth/jwt: unauthenticated: credentials expired: token expired"
	errNotInitial = "gas/auth/jwt: service not initialized"
	errNoCreds    = "gas/auth/jwt: no credentials" //nolint:gosec // G101: an error message, not a credential
)

// A signed token verifies to its claims: the registered ones in fields,
// the rest in CustomClaims as encoding/json decodes them.
func TestVerify_RoundTrip(t *testing.T) {
	t.Parallel()

	svc := newService(t, hsConfig(func(s *jwt.Settings) {
		s.Issuer = "auth.example"
		s.Audience = "api.example"
	}))

	token, err := svc.Sign("alice", m{"n": 3, "tags": []string{"a"}, "o": m{"k": true}})
	require.NoError(t, err)

	got, err := svc.Verify(token)
	require.NoError(t, err)

	c := unverified(t, token)
	assert.Equal(t, "alice", got.Subject)
	assert.Equal(t, c["jti"], got.TokenID)
	assert.Equal(t, "auth.example", got.Issuer)
	assert.Equal(t, []string{"api.example"}, got.Audience)
	assert.Equal(t, m{"n": float64(3), "tags": []any{"a"}, "o": m{"k": true}}, got.CustomClaims)
	assert.WithinDuration(t, time.Now(), got.IssuedAt, 2*time.Second)
	assert.Equal(t, 15*time.Minute, got.ExpiresAt.Sub(got.IssuedAt))
}

// A token from another issuer verifies without `jti` and `iat`, with an
// empty TokenID, a zero IssuedAt and a nil Audience.
func TestVerify_OptionalClaims(t *testing.T) {
	t.Parallel()

	token := mintHS(t, claims(func(c gojwt.MapClaims) { delete(c, "iat") }))

	got, err := newService(t, hsConfig()).Verify(token)
	require.NoError(t, err)
	assert.Empty(t, got.TokenID)
	assert.True(t, got.IssuedAt.IsZero())
	assert.Nil(t, got.Audience)
	assert.Empty(t, got.Issuer)
}

// Each check rejects a token with ErrTokenInvalid and its reason, which
// matches auth.ErrUnauthenticated and never includes the token.
func TestVerify_Checks(t *testing.T) {
	t.Parallel()

	tests := checkCases()

	svc := newService(t, hsConfig())

	for _, tt := range tests {
		token := mintHS(t, claims(tt.edit))

		got, err := svc.Verify(token)
		require.ErrorIs(t, err, jwt.ErrTokenInvalid, tt.name)
		require.ErrorIs(t, err, auth.ErrUnauthenticated, tt.name)
		require.NotErrorIs(t, err, jwt.ErrTokenExpired, tt.name)
		require.EqualError(t, err, tt.want, tt.name)
		assert.Nil(t, got)
	}
}

// A token that passes every check but `exp` is denied with
// ErrTokenExpired, which matches auth.ErrCredentialsExpired.
func TestVerify_Expired(t *testing.T) {
	t.Parallel()

	svc := newService(t, hsConfig())
	token := mintHS(
		t,
		claims(func(c gojwt.MapClaims) { c["exp"] = time.Now().Add(-10 * time.Second).Unix() }),
	)

	_, err := svc.Verify(token)
	require.ErrorIs(t, err, jwt.ErrTokenExpired)
	require.ErrorIs(t, err, auth.ErrCredentialsExpired)
	require.ErrorIs(t, err, auth.ErrUnauthenticated)
	require.NotErrorIs(t, err, jwt.ErrTokenInvalid)
	require.EqualError(t, err, errExpired)
}

// The leeway allows clock skew on `exp`, `nbf` and `iat`, and no more.
func TestVerify_Leeway(t *testing.T) {
	t.Parallel()

	strict := newService(t, hsConfig())
	lenient := newService(t, hsConfig(func(s *jwt.Settings) { s.Leeway = time.Minute }))
	now := time.Now()

	for name, edit := range map[string]func(gojwt.MapClaims){
		"exp 30s ago": func(c gojwt.MapClaims) { c["exp"] = now.Add(-30 * time.Second).Unix() },
		"iat in 30s":  func(c gojwt.MapClaims) { c["iat"] = now.Add(30 * time.Second).Unix() },
		"nbf in 30s":  func(c gojwt.MapClaims) { c["nbf"] = now.Add(30 * time.Second).Unix() },
		"iat and nbf in 30": func(c gojwt.MapClaims) {
			c["iat"], c["nbf"] = now.Add(30*time.Second).Unix(), now.Add(30*time.Second).Unix()
		},
	} {
		token := mintHS(t, claims(edit))

		_, err := strict.Verify(token)
		require.Error(t, err, name)

		_, err = lenient.Verify(token)
		require.NoError(t, err, name)
	}

	for name, edit := range map[string]func(gojwt.MapClaims){
		"exp 2m ago": func(c gojwt.MapClaims) { c["exp"] = now.Add(-2 * time.Minute).Unix() },
		"iat in 2m":  func(c gojwt.MapClaims) { c["iat"] = now.Add(2 * time.Minute).Unix() },
		"nbf in 2m":  func(c gojwt.MapClaims) { c["nbf"] = now.Add(2 * time.Minute).Unix() },
	} {
		_, err := lenient.Verify(mintHS(t, claims(edit)))
		require.ErrorIs(t, err, auth.ErrUnauthenticated, name)
	}
}

// A configured issuer and audience are required: `iss` equal, `aud`
// including the audience. Without them, any `iss` and `aud` pass.
func TestVerify_IssuerAudience(t *testing.T) {
	t.Parallel()

	svc := newService(t, hsConfig(func(s *jwt.Settings) {
		s.Issuer = "auth.example"
		s.Audience = "api.example"
	}))
	open := newService(t, hsConfig())

	tests := []struct {
		edit func(gojwt.MapClaims)
		name string
		want string
	}{
		{name: "both", edit: issAud(nil)},
		{name: "aud a string", edit: issAud(m{"aud": "api.example"})},
		{name: "no iss", edit: issAud(m{"iss": nil}), want: errMissing},
		{name: "other iss", edit: issAud(m{"iss": "evil"}), want: errIssuer},
		{name: "no aud", edit: issAud(m{"aud": nil}), want: errMissing},
		{name: "other aud", edit: issAud(m{"aud": "evil"}), want: errAudience},
	}

	for _, tt := range tests {
		token := mintHS(t, claims(tt.edit))

		_, err := svc.Verify(token)
		if tt.want == "" {
			require.NoError(t, err, tt.name)
		} else {
			require.EqualError(t, err, tt.want, tt.name)
		}

		_, err = open.Verify(token)
		require.NoError(t, err, tt.name)
	}
}

// issAud returns an edit that sets an accepted `iss` and `aud`, then
// applies changes, deleting the claims whose value is nil.
func issAud(changes m) func(gojwt.MapClaims) {
	return func(c gojwt.MapClaims) {
		c["iss"] = "auth.example"
		c["aud"] = []string{"other", "api.example"}

		for k, v := range changes {
			if v == nil {
				delete(c, k)
			} else {
				c[k] = v
			}
		}
	}
}

// The algorithm is pinned: `none`, another HMAC hash with the same key, an
// unknown or a missing `alg` are rejected, and so is another key.
func TestVerify_Algorithm(t *testing.T) {
	t.Parallel()

	svc := newService(t, hsConfig())

	none := mint(t, gojwt.SigningMethodNone, gojwt.UnsafeAllowNoneSignatureType, claims(nil))
	_, err := svc.Verify(none)
	require.EqualError(t, err, errSignature)

	hs512 := mint(t, gojwt.SigningMethodHS512, []byte(testKey), claims(nil))

	_, err = svc.Verify(hs512)
	require.EqualError(t, err, errSignature)

	wrongKey := mint(t, gojwt.SigningMethodHS256, []byte(otherKey), claims(nil))

	_, err = svc.Verify(wrongKey)
	require.EqualError(t, err, errSignature)

	body := strings.SplitN(mintHS(t, claims(nil)), ".", 2)[1]

	for _, header := range []string{`{"alg":"XX99","typ":"JWT"}`, `{"typ":"JWT"}`, `{"alg":7}`} {
		_, err = svc.Verify(b64(header) + "." + body)
		require.EqualError(t, err, errAlgorithm, header)
	}
}

// b64 returns s as unpadded base64url.
func b64(s string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(s))
}

// A token that is not a compact JWS of canonical unpadded base64url and
// JSON objects is malformed.
func TestVerify_Malformed(t *testing.T) {
	t.Parallel()

	svc := newService(t, hsConfig())
	valid := mintHS(t, claims(nil))
	parts := strings.Split(valid, ".")

	// The signature's last character carries 4 unused bits, zero in the
	// canonical encoding; setting one keeps the decoded bytes the same.
	sig := []byte(parts[2])
	alphabet := "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"

	sig[len(sig)-1] = alphabet[strings.IndexByte(alphabet, sig[len(sig)-1])|1]

	for _, token := range []string{
		"",
		"garbage",
		"a.b",
		"a.b.c.d",
		parts[0] + "=." + parts[1] + "." + parts[2],
		parts[0] + "." + parts[1] + "." + string(sig),
		b64("[]") + "." + parts[1] + "." + parts[2],
		parts[0] + "." + b64("[]") + "." + parts[2],
		parts[0] + "." + b64(`{"sub": hunter2}`) + "." + parts[2],
		" " + valid,
	} {
		_, err := svc.Verify(token)
		require.EqualError(t, err, errMalformed, token)
	}

	_, err := svc.Verify(valid)
	require.NoError(t, err)
}

// Verify before Init returns ErrNotInitialized, which is not a denial.
func TestVerify_NotInitialized(t *testing.T) {
	t.Parallel()

	_, err := build(t, hsConfig()).Verify(mintHS(t, claims(nil)))
	require.ErrorIs(t, err, jwt.ErrNotInitialized)
	require.NotErrorIs(t, err, auth.ErrUnauthenticated)
	require.EqualError(t, err, errNotInitial)
}

// FuzzVerify checks Verify and Authenticate on arbitrary token strings:
// neither panics, every error is one of the documented denials, so it
// cannot quote the input, the two agree, and a token that verifies has a
// subject and an expiry.
func FuzzVerify(f *testing.F) {
	svc := newService(f, hsConfig())
	now := time.Now()

	valid, err := svc.Sign("alice", m{"role": "admin"})
	if err != nil {
		f.Fatal(err)
	}

	for _, seed := range []string{
		"",
		".",
		"..",
		"a.b.c",
		valid,
		valid + "x",
		strings.ToUpper(valid),
		b64(`{"alg":"HS256"}`) + "." + b64(`{"sub": hunter2}`) + ".sig",
		b64(`{"alg":"none"}`) + "." + b64(`{"sub":"a","exp":1e300}`) + ".",
		b64(`{"alg":"HS256"}`) + "." + b64(`{"exp":-1,"iat":"x","aud":[1,{}]}`) + ".AAAA",
		string(rune(0xFFFD)) + "\x00.\xff.\n",
	} {
		f.Add(seed)
	}

	f.Add(mintHS(f, claims(func(c gojwt.MapClaims) { c["exp"] = now.Add(-time.Hour).Unix() })))

	f.Fuzz(func(t *testing.T, token string) {
		got, verr := svc.Verify(token)

		p, aerr := svc.Authenticate(t.Context(), request("Bearer "+token))

		if verr != nil {
			require.Nil(t, got)
			require.ErrorIs(t, verr, auth.ErrUnauthenticated)
			require.True(t, slices.Contains(denials, verr.Error()), verr.Error())
		} else {
			require.NotEmpty(t, got.Subject)
			require.False(t, got.ExpiresAt.IsZero())
		}

		if aerr != nil {
			require.Nil(t, p)
			require.True(t, slices.Contains(denials, aerr.Error()) ||
				aerr.Error() == errNoCreds, aerr.Error())
		}

		// Authenticate trims the header, so it may see a token that
		// Verify did not; with no space at the ends they see the same.
		if strings.TrimSpace(token) == token && token != "" {
			require.Equal(t, verr == nil, aerr == nil)
		}
	})
}

// checkCase is a change to valid claims and the error it causes.
type checkCase struct {
	edit func(gojwt.MapClaims)
	name string
	want string
}

// checkCases returns the cases of TestVerify_Checks.
func checkCases() []checkCase {
	now := time.Now()
	hour := time.Hour

	return []checkCase{
		{name: "no exp", edit: func(c gojwt.MapClaims) { delete(c, "exp") }, want: errMissing},
		{name: "exp zero", edit: func(c gojwt.MapClaims) { c["exp"] = 0 }, want: errMissing},
		{
			name: "exp a string",
			edit: func(c gojwt.MapClaims) { c["exp"] = "tomorrow" },
			want: errType,
		},
		{name: "no sub", edit: func(c gojwt.MapClaims) { delete(c, "sub") }, want: errMissing},
		{name: "empty sub", edit: func(c gojwt.MapClaims) { c["sub"] = "" }, want: errMissing},
		{name: "sub a number", edit: func(c gojwt.MapClaims) { c["sub"] = 7 }, want: errType},
		{name: "jti a number", edit: func(c gojwt.MapClaims) { c["jti"] = 7 }, want: errType},
		{name: "jti null", edit: func(c gojwt.MapClaims) { c["jti"] = nil }, want: errType},
		{name: "iss a number", edit: func(c gojwt.MapClaims) { c["iss"] = 7 }, want: errType},
		{name: "aud numbers", edit: func(c gojwt.MapClaims) { c["aud"] = []int{7} }, want: errType},
		{name: "iat a string", edit: func(c gojwt.MapClaims) { c["iat"] = "now" }, want: errType},
		{name: "nbf a string", edit: func(c gojwt.MapClaims) { c["nbf"] = "now" }, want: errType},
		{
			name: "iat in the future",
			edit: func(c gojwt.MapClaims) { c["iat"] = now.Add(hour).Unix() },
			want: errFuture,
		},
		{
			name: "nbf in the future",
			edit: func(c gojwt.MapClaims) { c["nbf"] = now.Add(hour).Unix() },
			want: errNotYet,
		},
		{
			name: "expired without sub",
			edit: func(c gojwt.MapClaims) {
				c["exp"] = now.Add(-hour).Unix()
				delete(c, "sub")
			},
			want: errMissing,
		},
		{
			name: "expired with a bad jti",
			edit: func(c gojwt.MapClaims) {
				c["exp"] = now.Add(-hour).Unix()
				c["jti"] = 1
			},
			want: errType,
		},
		{
			name: "expired and not valid yet",
			edit: func(c gojwt.MapClaims) {
				c["exp"] = now.Add(-hour).Unix()
				c["nbf"] = now.Add(hour).Unix()
			},
			want: errNotYet,
		},
	}
}
