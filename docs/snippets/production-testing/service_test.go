package visits

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/cache/cachetest"
)

// #region unit
// A unit test builds the service with a mock and calls it directly.
func TestVisitIncrements(t *testing.T) {
	mock := &cachetest.MockCache{
		GetFn: func(context.Context, string) ([]byte, error) { return []byte("41"), nil },
	}
	svc := New(mock, gas.NewRouter())

	n, err := svc.Visit(t.Context(), "home")
	if err != nil {
		t.Fatalf("visit: %v", err)
	}
	if n != 42 {
		t.Fatalf("got %d, want 42", n)
	}
	if got := mock.CallCount("Set"); got != 1 {
		t.Fatalf("Set called %d times, want 1", got)
	}
}

// #endregion unit

// #region app
// newTestApp wires the real service into an App with the mock registered under
// the interface the service depends on, and starts it.
func newTestApp(t *testing.T, cache gas.CacheProvider) *gas.App {
	t.Helper()

	app := gas.NewApp(
		gas.WithServiceInstance[gas.CacheProvider](cache),
		gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
		gas.WithSingletonService[*Service](New),
	)
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		if err := app.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	})
	return app
}

// #endregion app

// #region handler
// An end-to-end test sends a request through App.Handler: routing, middleware,
// dependency injection, and error rendering, with no port bound.
func TestRecordRoute(t *testing.T) {
	app := newTestApp(t, &cachetest.MockCache{})

	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/visits/home", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	var body map[string]int
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["visits"] != 1 {
		t.Fatalf("got %v", body)
	}
}

// #endregion handler

// #region failure
// A failing dependency shows up as the error envelope the client would see.
func TestRecordRouteCacheDown(t *testing.T) {
	mock := &cachetest.MockCache{
		GetFn: func(context.Context, string) ([]byte, error) { return nil, errors.New("connection refused") },
	}
	app := newTestApp(t, mock)

	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/visits/home", nil))

	var resp gas.ErrorResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if rec.Code != http.StatusServiceUnavailable || resp.Error.Code != gas.CodeUnavailable {
		t.Fatalf("got %d %+v", rec.Code, resp.Error)
	}
	if mock.CallCount("Set") != 0 {
		t.Fatal("Set should not be called when Get fails")
	}
}

// #endregion failure

// #region server
// httptest.NewServer serves the same handler on a real loopback listener, for
// tests that need a client and a network round trip.
func TestRecordOverHTTP(t *testing.T) {
	app := newTestApp(t, &cachetest.MockCache{})

	srv := httptest.NewServer(app.Handler())
	t.Cleanup(srv.Close)

	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+"/visits/home", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck // documentation snippet

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

// #endregion server

// #region smoke
// Start fails on wiring mistakes, so starting the production wiring is a
// useful test on its own. Here the cache is missing.
func TestWiringFailsWithoutCache(t *testing.T) {
	app := gas.NewApp(
		gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
		gas.WithSingletonService[*Service](New),
	)
	if err := app.Start(t.Context()); err == nil {
		t.Fatal("expected Start to fail without a gas.CacheProvider")
	}
}

// #endregion smoke

// #region scope
// Scoped services come from a request's scope. Outside a request, make one.
type Tracker struct{ pages []string }

func (tr *Tracker) Name() string                { return "tracker" }
func (tr *Tracker) Init(context.Context) error  { return nil }
func (tr *Tracker) Close(context.Context) error { tr.pages = nil; return nil }

func TestScopedService(t *testing.T) {
	container := gas.NewServiceContainer()
	container.RegisterScopedService[*Tracker](func() *Tracker { return &Tracker{} })

	scope := container.NewScope(t.Context())
	defer func() {
		// Close calls Close on every scoped Service resolved in this scope.
		if err := scope.Close(context.Background()); err != nil {
			t.Errorf("close scope: %v", err)
		}
	}()

	tracker := scope.MustResolve[*Tracker]()
	tracker.pages = append(tracker.pages, "home")

	// Code that reads the request scope finds this one on the context.
	ctx := gas.WithRequestScope(t.Context(), scope)
	r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/", nil)
	same, err := gas.ResolveFromRequestScope[*Tracker](r)
	if err != nil || same != tracker {
		t.Fatalf("got %v, %v", same, err)
	}
}

// #endregion scope

// #region principal
// testPrincipal is a fixed gas.Principal for tests.
type testPrincipal struct{ subject string }

func (p testPrincipal) Subject() string                 { return p.subject }
func (p testPrincipal) Scheme() string                  { return "test" }
func (p testPrincipal) CredentialID() string            { return "" }
func (p testPrincipal) Metadata() gas.PrincipalMetadata { return gas.BasePrincipalMetadata{} }

// asUser attaches a principal the way an authentication middleware would.
func asUser(subject string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := gas.WithPrincipal(r.Context(), testPrincipal{subject: subject})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func TestWhoAmI(t *testing.T) {
	app := gas.NewApp()
	app.Router().Get(nil, "/me", func(ctx gas.Context) error {
		p, err := ctx.Principal()
		if err != nil {
			return err
		}
		return ctx.Text(http.StatusOK, p.Subject())
	}, gas.MiddlewareFunc(asUser("user-1")))
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		if err := app.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	})

	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/me", nil))
	if rec.Body.String() != "user-1" {
		t.Fatalf("got %d %q", rec.Code, rec.Body)
	}
}

// #endregion principal
