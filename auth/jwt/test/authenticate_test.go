package jwt_test

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	gojwt "github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth"
	"github.com/gasmod/gas/auth/jwt"
)

// A valid bearer token gives a principal with the subject, the JWT scheme,
// the `jti` as credential ID, and the custom claims as metadata.
func TestAuthenticate_Principal(t *testing.T) {
	t.Parallel()

	svc := newService(t, hsConfig())

	token, err := svc.Sign("alice", m{"role": "admin"})
	require.NoError(t, err)

	p, err := svc.Authenticate(t.Context(), request("Bearer "+token))
	require.NoError(t, err)

	assert.Equal(t, "alice", p.Subject())
	assert.Equal(t, auth.SchemeJWT, p.Scheme())
	assert.Equal(t, unverified(t, token)["jti"], p.CredentialID())
	assert.Equal(t, gas.BasePrincipalMetadata{"role": "admin"}, p.Metadata())

	foreign := mintHS(t, claims(nil))

	p, err = svc.Authenticate(t.Context(), request("Bearer "+foreign))
	require.NoError(t, err)
	assert.Empty(t, p.CredentialID(), "a token without jti")
}

// The Authorization header is parsed as documented: no bearer header is
// ErrNoCredentials, a bearer header without a token or more than one is
// ErrTokenInvalid, and the scheme matches in any case.
func TestAuthenticate_Header(t *testing.T) {
	t.Parallel()

	svc := newService(t, hsConfig())

	token, err := svc.Sign("alice", nil)
	require.NoError(t, err)

	for _, tt := range headerCases(token) {
		p, err := svc.Authenticate(t.Context(), request(tt.headers...))

		switch tt.want {
		case "":
			require.NoError(t, err, tt.name)
			assert.Equal(t, "alice", p.Subject(), tt.name)
		case errNoCreds:
			require.ErrorIs(t, err, auth.ErrNoCredentials, tt.name)
			require.EqualError(t, err, tt.want, tt.name)
			require.NotErrorIs(t, err, auth.ErrUnauthenticated, tt.name)
			assert.Nil(t, p, tt.name)
		default:
			require.ErrorIs(t, err, jwt.ErrTokenInvalid, tt.name)
			require.EqualError(t, err, tt.want, tt.name)
			assert.Nil(t, p, tt.name)
		}
	}
}

// An expired token is ErrTokenExpired, which matches
// auth.ErrCredentialsExpired, and errors never include the token.
func TestAuthenticate_Expired(t *testing.T) {
	t.Parallel()

	svc := newService(t, hsConfig())
	token := mintHS(
		t,
		claims(func(c gojwt.MapClaims) { c["exp"] = time.Now().Add(-time.Minute).Unix() }),
	)

	_, err := svc.Authenticate(t.Context(), request("Bearer "+token))
	require.ErrorIs(t, err, jwt.ErrTokenExpired)
	require.ErrorIs(t, err, auth.ErrCredentialsExpired)
	assert.NotContains(t, err.Error(), token)
}

// Before Init, Authenticate returns ErrNotInitialized whatever the request
// carries, even nothing, so a miswired service is never a mere 401.
func TestAuthenticate_NotInitialized(t *testing.T) {
	t.Parallel()

	svc := build(t, hsConfig())

	for _, r := range []*http.Request{request(), request("Bearer x"), request("Basic x")} {
		_, err := svc.Authenticate(t.Context(), r)
		require.ErrorIs(t, err, jwt.ErrNotInitialized)
		require.EqualError(t, err, errNotInitial)
	}
}

// A nil request carries no credentials: Authenticate returns an error
// wrapping auth.ErrNoCredentials and does not panic. Before Init it is
// ErrNotInitialized, as for any request.
func TestAuthenticate_NilRequest(t *testing.T) {
	t.Parallel()

	p, err := newService(t, hsConfig()).Authenticate(t.Context(), nil)
	require.ErrorIs(t, err, auth.ErrNoCredentials)
	require.NotErrorIs(t, err, auth.ErrUnauthenticated)
	require.EqualError(t, err, errNoCreds)
	assert.Nil(t, p)

	_, err = build(t, hsConfig()).Authenticate(t.Context(), nil)
	require.ErrorIs(t, err, jwt.ErrNotInitialized)
}

// Behind auth.Middleware, a valid token reaches the handler, a bad or
// missing one gets a 401, and an uninitialized service a 500.
func TestAuthenticate_Middleware(t *testing.T) {
	t.Parallel()

	svc := newService(t, hsConfig())

	token, err := svc.Sign("alice", nil)
	require.NoError(t, err)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := gas.PrincipalFromContext(r.Context())

		_, _ = w.Write([]byte(p.Subject()))
	})

	serve := func(svc *jwt.Service, r *http.Request) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		auth.Middleware(svc)(handler).ServeHTTP(rec, r)

		return rec
	}

	rec := serve(svc, request("Bearer "+token))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "alice", rec.Body.String())

	assert.Equal(t, http.StatusUnauthorized, serve(svc, request()).Code)
	assert.Equal(t, http.StatusUnauthorized, serve(svc, request("Bearer "+token+"x")).Code)
	assert.Equal(t, http.StatusInternalServerError, serve(build(t, hsConfig()), request()).Code)
}

// FuzzAuthenticateHeader checks Authenticate on arbitrary Authorization
// header values: it never panics, and every error is ErrNoCredentials or
// one of the documented denials, so it cannot quote the header.
func FuzzAuthenticateHeader(f *testing.F) {
	svc := newService(f, hsConfig())

	token, err := svc.Sign("alice", nil)
	if err != nil {
		f.Fatal(err)
	}

	for _, seed := range []string{
		"", " ", "Bearer", "Bearer ", "bearer " + token, "Bearer  " + token, "Basic hunter2",
		"Bearer\t" + token, "Bearer " + token + " extra", "\x00Bearer \xff", "Bearer " + strings.Repeat(".", 64),
	} {
		f.Add(seed, "")
		f.Add(seed, "Bearer "+token)
	}

	f.Fuzz(func(t *testing.T, first, second string) {
		headers := []string{first}
		if second != "" {
			headers = append(headers, second)
		}

		p, err := svc.Authenticate(t.Context(), request(headers...))
		if err == nil {
			require.Equal(t, "alice", p.Subject())

			return
		}

		require.Nil(t, p)
		require.True(t, slices.Contains(denials, err.Error()) ||
			err.Error() == errNoCreds, err.Error())
	})
}

// headerCase is a set of Authorization headers and the error message
// Authenticate returns for them, empty for success.
type headerCase struct {
	name    string
	want    string
	headers []string
}

// headerCases returns the cases of TestAuthenticate_Header, with token a
// valid token.
func headerCases(token string) []headerCase {
	const (
		ok          = ""
		noCreds     = errNoCreds
		multiple    = "gas/auth/jwt: unauthenticated: invalid token: more than one bearer token"
		malformed   = errMalformed
		badBasicVal = "Basic dXNlcjpwYXNz"
	)

	return []headerCase{
		{name: "no header", want: noCreds},
		{name: "empty header", headers: []string{""}, want: noCreds},
		{name: "blank header", headers: []string{"   "}, want: noCreds},
		{name: "basic", headers: []string{badBasicVal}, want: noCreds},
		{name: "scheme glued to token", headers: []string{"Bearer" + token}, want: noCreds},
		{name: "tab after scheme", headers: []string{"Bearer\t" + token}, want: noCreds},
		{name: "scheme only", headers: []string{"Bearer"}, want: malformed},
		{name: "scheme and spaces", headers: []string{"Bearer    "}, want: malformed},
		{name: "bearer", headers: []string{"Bearer " + token}, want: ok},
		{name: "lowercase", headers: []string{"bearer " + token}, want: ok},
		{name: "uppercase, two spaces", headers: []string{"BEARER  " + token}, want: ok},
		{name: "surrounding spaces", headers: []string{"  Bearer " + token + "  "}, want: ok},
		{name: "basic and bearer", headers: []string{badBasicVal, "Bearer " + token}, want: ok},
		{
			name:    "two bearers",
			headers: []string{"Bearer " + token, "Bearer " + token},
			want:    multiple,
		},
		{
			name:    "bearer and empty bearer",
			headers: []string{"Bearer " + token, "Bearer"},
			want:    multiple,
		},
		{name: "garbage token", headers: []string{"Bearer abc"}, want: malformed},
	}
}
