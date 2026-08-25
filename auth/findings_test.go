package auth_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gasmod/gas"
	auth "github.com/gasmod/gas/auth"
	"github.com/gasmod/gas/auth/authtest"
	"github.com/stretchr/testify/assert"
)

// ---------------------------------------------------------------------------
// Finding #5: Chain authenticator discards intermediate errors
// ---------------------------------------------------------------------------

// TestChainLosesExpiredError demonstrates that when a JWT authenticator returns
// ErrCredentialsExpired but a later authenticator returns ErrUnauthenticated,
// the more informative expired error is lost.
func TestChainLosesExpiredError(t *testing.T) {
	jwtAuth := &authtest.MockAuthenticator{
		AuthenticateFn: func(_ context.Context, _ *http.Request) (gas.Principal, error) {
			// JWT finds a token but it's expired.
			return nil, auth.ErrCredentialsExpired
		},
	}
	sessionAuth := &authtest.MockAuthenticator{
		AuthenticateFn: func(_ context.Context, _ *http.Request) (gas.Principal, error) {
			// Session finds no cookie.
			return nil, auth.ErrUnauthenticated
		},
	}
	chain := auth.Chain{jwtAuth, sessionAuth}

	_, err := chain.Authenticate(context.Background(), httptest.NewRequest(http.MethodGet, "/", nil))

	// The caller sent an expired JWT. The most useful error is ErrCredentialsExpired
	// so the client knows to refresh. But the chain returns only the last error.
	assert.True(t, errors.Is(err, auth.ErrUnauthenticated),
		"chain returns ErrUnauthenticated (the last error)")
	assert.False(t, errors.Is(err, auth.ErrCredentialsExpired),
		"ErrCredentialsExpired from the JWT authenticator is lost")
}

// TestChainLosesRevokedError demonstrates the same issue with a revoked credential.
func TestChainLosesRevokedError(t *testing.T) {
	first := &authtest.MockAuthenticator{
		AuthenticateFn: func(_ context.Context, _ *http.Request) (gas.Principal, error) {
			return nil, auth.ErrCredentialRevoked
		},
	}
	second := &authtest.MockAuthenticator{
		AuthenticateFn: func(_ context.Context, _ *http.Request) (gas.Principal, error) {
			return nil, auth.ErrUnauthenticated
		},
	}
	chain := auth.Chain{first, second}

	_, err := chain.Authenticate(context.Background(), httptest.NewRequest(http.MethodGet, "/", nil))

	assert.True(t, errors.Is(err, auth.ErrUnauthenticated),
		"chain returns ErrUnauthenticated (the last error)")
	assert.False(t, errors.Is(err, auth.ErrCredentialRevoked),
		"ErrCredentialRevoked is lost — caller cannot distinguish revoked from missing credentials")
}

// ---------------------------------------------------------------------------
// Finding #10: RequireScheme returns bare 403 with no context
// ---------------------------------------------------------------------------

func TestRequireSchemeGiveNoContextOnMismatch(t *testing.T) {
	principal := auth.NewPrincipal("user-1", auth.SchemeSession, "sess-1", nil)

	handler := auth.RequireScheme(auth.SchemeAPIKey)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(gas.WithPrincipal(req.Context(), principal))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)

	// The response body says "Forbidden" but doesn't indicate that the
	// required scheme was "apikey" while the principal has "session".
	body := rec.Body.String()
	assert.NotContains(t, body, "apikey",
		"response body gives no indication of the required scheme")
	assert.NotContains(t, body, "session",
		"response body gives no indication of the principal's scheme")
}
