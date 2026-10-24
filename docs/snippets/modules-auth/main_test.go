package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth"
	"github.com/gasmod/gas/auth/authtest"
)

// #region testing
// A protected handler tested without issuing real credentials: the mock
// authenticator returns whatever principal the test needs.
func TestMiddlewareStoresPrincipal(t *testing.T) {
	mock := &authtest.MockAuthenticator{
		AuthenticateFn: func(context.Context, *http.Request) (gas.Principal, error) {
			return auth.NewPrincipal("u-123", auth.SchemeSession, "s-1", nil), nil
		},
	}

	var subject string
	h := auth.Middleware(mock)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p, ok := gas.PrincipalFromContext(r.Context()); ok {
			subject = p.Subject()
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/me", nil))

	if rec.Code != http.StatusNoContent || subject != "u-123" {
		t.Fatalf("status %d, subject %q", rec.Code, subject)
	}
	if n := mock.CallCount("Authenticate"); n != 1 {
		t.Fatalf("Authenticate called %d times, want 1", n)
	}
}

// #endregion testing
