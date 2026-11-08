package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth"
	"github.com/gasmod/gas/auth/authtest"
)

// The example in the package documentation's Usage section: a JWT or a
// session authenticates, the authorizer decides, and the responses are a
// 200, a 401 with the challenge, and a 403.
func TestDoc_Usage(t *testing.T) {
	t.Parallel()

	// Stand-ins for the jwt and session services: each finds a credential
	// in its own header.
	byHeader := func(header, scheme string) *authtest.MockAuthenticator {
		return &authtest.MockAuthenticator{
			AuthenticateFn: func(_ context.Context, r *http.Request) (gas.Principal, error) {
				v := r.Header.Get(header)
				if v == "" {
					return nil, auth.ErrNoCredentials
				}

				return auth.NewPrincipal(v, scheme, "cred", nil), nil
			},
		}
	}
	jwtService := byHeader("X-Token", auth.SchemeJWT)
	sessionService := byHeader("X-Session", auth.SchemeSession)
	authorizer := &authtest.MockAuthorizer{
		AuthorizeFn: func(_ context.Context, p gas.Principal, action, resource string) error {
			if p.Subject() == "alice" && action == "read" && resource == "reports" {
				return nil
			}

			return auth.ErrForbidden
		},
	}
	reportsHandler := okHandler
	mux := http.NewServeMux()

	authn := auth.Middleware(
		auth.Chain{jwtService, sessionService},
		auth.WithChallenge(`Bearer realm="api"`),
	)
	reports := auth.RequireAuthorization(authorizer, "read", "reports")

	mux.Handle("/reports", authn(reports(reportsHandler)))

	get := func(header, value string) *httptest.ResponseRecorder {
		r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/reports", nil)
		if header != "" {
			r.Header.Set(header, value)
		}

		return serve(mux, r)
	}

	rec := get("X-Session", "alice")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "alice", rec.Body.String())

	rec = get("", "")
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, `Bearer realm="api"`, rec.Header().Get("WWW-Authenticate"))

	rec = get("X-Token", "bob")
	assert.Equal(t, http.StatusForbidden, rec.Code)
}
