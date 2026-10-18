package gas_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gasmod/gas"
)

// testPrincipal is a minimal gas.Principal for the core principal tests.
type testPrincipal struct {
	meta    gas.PrincipalMetadata
	subject string
}

func (p testPrincipal) Subject() string                 { return p.subject }
func (p testPrincipal) Scheme() string                  { return "test" }
func (p testPrincipal) CredentialID() string            { return "cred-1" }
func (p testPrincipal) Metadata() gas.PrincipalMetadata { return p.meta }

// principalRouter returns a router with one route that reports the result of
// Context.Principal, and the error the handler saw. When attach is set, a
// middleware stores principal on the request context first, the way an
// authentication middleware does.
func principalRouter(t *testing.T, attach bool, principal gas.Principal) (*gas.Router, *error) {
	t.Helper()
	app := gas.NewApp()
	if err := app.InitServices(context.Background()); err != nil {
		t.Fatal(err)
	}
	if attach {
		app.Router().UseMiddlewareFunc(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				next.ServeHTTP(w, r.WithContext(gas.WithPrincipal(r.Context(), principal)))
			})
		})
	}
	var seen error
	app.Router().Handle(nil, "GET", "/me", func(ctx gas.Context) error {
		p, err := ctx.Principal()
		seen = err
		if err != nil {
			return err
		}
		return ctx.Text(http.StatusOK, p.Subject())
	})
	return app.Router(), &seen
}

// TestContext_Principal_Present checks that a principal stored by middleware
// reaches the handler through Context.Principal.
func TestContext_Principal_Present(t *testing.T) {
	t.Parallel()

	router, seen := principalRouter(t, true, testPrincipal{subject: "user-42"})
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest("GET", "/me", nil))

	if *seen != nil {
		t.Fatalf("Principal() error = %v, want nil", *seen)
	}
	if rr.Code != http.StatusOK || rr.Body.String() != "user-42" {
		t.Fatalf("response = %d %q, want 200 %q", rr.Code, rr.Body.String(), "user-42")
	}
}

// TestContext_Principal_Absent checks that a request with no principal gets a
// 401 *gas.Error with CodeUnauthorized from Context.Principal, and that
// returning it from the handler produces a 401 response.
func TestContext_Principal_Absent(t *testing.T) {
	t.Parallel()

	router, seen := principalRouter(t, false, nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest("GET", "/me", nil))

	var gerr *gas.Error
	if !errors.As(*seen, &gerr) {
		t.Fatalf("Principal() error = %v, want a *gas.Error", *seen)
	}
	if gerr.Status != http.StatusUnauthorized || gerr.Code != gas.CodeUnauthorized {
		t.Fatalf("error = %d %q, want 401 %q", gerr.Status, gerr.Code, gas.CodeUnauthorized)
	}
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}

// TestContext_Principal_NilStored checks that a nil Principal stored in the
// context counts as absent.
func TestContext_Principal_NilStored(t *testing.T) {
	t.Parallel()

	router, seen := principalRouter(t, true, nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest("GET", "/me", nil))

	var gerr *gas.Error
	if !errors.As(*seen, &gerr) || gerr.Code != gas.CodeUnauthorized {
		t.Fatalf("Principal() error = %v, want a 401 *gas.Error", *seen)
	}
}

// TestPrincipalFromContext checks the round trip through WithPrincipal and
// the nil result when no principal is stored.
func TestPrincipalFromContext(t *testing.T) {
	t.Parallel()

	if p := gas.PrincipalFromContext(context.Background()); p != nil {
		t.Fatalf("PrincipalFromContext(empty) = %v, want nil", p)
	}
	want := testPrincipal{subject: "user-1"}
	got := gas.PrincipalFromContext(gas.WithPrincipal(context.Background(), want))
	if got == nil || got.Subject() != "user-1" {
		t.Fatalf("PrincipalFromContext = %v, want subject user-1", got)
	}
	if p := gas.PrincipalFromContext(gas.WithPrincipal(context.Background(), nil)); p != nil {
		t.Fatalf("PrincipalFromContext(nil stored) = %v, want nil", p)
	}
}

// TestMetadataValue checks typed metadata lookup: a present key of the right
// type, a present key of the wrong type, and a missing key.
func TestMetadataValue(t *testing.T) {
	t.Parallel()

	m := gas.BasePrincipalMetadata{"role": "admin", "level": 3}

	if v, ok := gas.MetadataValue[string](m, "role"); !ok || v != "admin" {
		t.Fatalf("MetadataValue[string](role) = %q, %v; want admin, true", v, ok)
	}
	if v, ok := gas.MetadataValue[string](m, "level"); ok || v != "" {
		t.Fatalf("MetadataValue[string](level) = %q, %v; want \"\", false", v, ok)
	}
	if v, ok := gas.MetadataValue[int](m, "missing"); ok || v != 0 {
		t.Fatalf("MetadataValue[int](missing) = %d, %v; want 0, false", v, ok)
	}
	if v := m.Value("missing"); v != nil {
		t.Fatalf("Value(missing) = %v, want nil", v)
	}
}

// ptrPrincipal is a gas.Principal implemented on a pointer, so a nil
// *ptrPrincipal is a typed-nil Principal.
type ptrPrincipal struct{ subject string }

func (p *ptrPrincipal) Subject() string                 { return p.subject }
func (p *ptrPrincipal) Scheme() string                  { return "test" }
func (p *ptrPrincipal) CredentialID() string            { return "cred-1" }
func (p *ptrPrincipal) Metadata() gas.PrincipalMetadata { return nil }

// A typed-nil Principal stored in the context counts as absent, so a
// downstream nil check cannot pass on it and calling its methods cannot
// panic.
func TestPrincipalFromContext_TypedNil(t *testing.T) {
	t.Parallel()

	var typedNil *ptrPrincipal
	ctx := gas.WithPrincipal(context.Background(), typedNil)
	if p := gas.PrincipalFromContext(ctx); p != nil {
		t.Fatalf("PrincipalFromContext(typed nil stored) = %#v, want nil", p)
	}
}

// Context.Principal treats a typed-nil Principal as unauthenticated.
func TestContext_Principal_TypedNilStored(t *testing.T) {
	t.Parallel()

	var typedNil *ptrPrincipal
	router, seen := principalRouter(t, true, typedNil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest("GET", "/me", nil))

	var gerr *gas.Error
	if !errors.As(*seen, &gerr) || gerr.Code != gas.CodeUnauthorized {
		t.Fatalf("Principal() error = %v, want a 401 *gas.Error", *seen)
	}
}
