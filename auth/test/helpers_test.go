package auth_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth"
	"github.com/gasmod/gas/auth/authtest"
)

// errDB stands for an infrastructure error, such as a database outage.
var errDB = errors.New("database is down")

// returning is an Authenticator that returns principal and err.
func returning(principal gas.Principal, err error) *authtest.MockAuthenticator {
	return &authtest.MockAuthenticator{
		AuthenticateFn: func(context.Context, *http.Request) (gas.Principal, error) {
			return principal, err
		},
	}
}

// succeeding is an Authenticator that returns a principal named subject.
func succeeding(subject string) *authtest.MockAuthenticator {
	return returning(auth.NewPrincipal(subject, auth.SchemeJWT, "cred-"+subject, nil), nil)
}

// okHandler answers 200 with the principal's subject, or 299 when there is
// none, so a test sees both that it ran and what it saw.
var okHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	p, ok := gas.PrincipalFromContext(r.Context())
	if !ok {
		w.WriteHeader(299)

		return
	}

	_, _ = w.Write([]byte(p.Subject()))
})

// serve sends a GET through h and returns the recorded response.
func serve(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	if r == nil {
		r = newGet()
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)

	return rec
}

// newGet returns a GET request for `/`.
func newGet() *http.Request {
	return httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
}

// withPrincipal returns a GET request carrying p, as Middleware leaves it.
func withPrincipal(p gas.Principal) *http.Request {
	r := newGet()

	return r.WithContext(gas.WithPrincipal(r.Context(), p))
}

// withLogger returns r with l on its context, as gas.RequestLogger leaves
// it.
func withLogger(r *http.Request, l gas.Logger) *http.Request {
	return r.WithContext(gas.WithLogger(r.Context(), l))
}

// acceptHTML returns r asking for HTML, so the core's error writer
// answers it with plain text.
func acceptHTML(r *http.Request) *http.Request {
	r.Header.Set("Accept", "text/html")

	return r
}

// assertProblem asserts that rec holds the core's problem details
// response with status and code, and returns its detail.
func assertProblem(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) string {
	t.Helper()

	assert.Equal(t, status, rec.Code)
	assert.Equal(t, gas.ProblemContentType, rec.Header().Get("Content-Type"))
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))

	var p gas.Problem

	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &p))
	assert.Equal(t, status, p.Status)
	assert.Equal(t, http.StatusText(status), p.Title)
	assert.Equal(t, code, p.Code)

	return p.Detail
}

// assertPlainText asserts that rec holds the core's plain-text error
// response with status and body.
func assertPlainText(t *testing.T, rec *httptest.ResponseRecorder, status int, body string) {
	t.Helper()

	assert.Equal(t, status, rec.Code)
	assert.Equal(t, "text/plain; charset=utf-8", rec.Header().Get("Content-Type"))
	assert.Equal(t, body+"\n", rec.Body.String())
}
