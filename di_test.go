package gas_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gasmod/gas"
)

// MustResolve returns a registered service and panics for an unregistered
// one.
func TestServiceContainer_MustResolve(t *testing.T) {
	t.Parallel()

	dep := &testDep{Value: "instance"}
	app := gas.NewApp(gas.WithServiceInstance[*testDep](dep))
	if err := app.InitServices(context.Background()); err != nil {
		t.Fatal(err)
	}

	if got := app.ServiceContainer().MustResolve[*testDep](); got != dep {
		t.Errorf("MustResolve[*testDep]() = %p, want the registered instance %p", got, dep)
	}
	assertPanics(t, "MustResolve of an unregistered type", func() {
		app.ServiceContainer().MustResolve[*testDepB]()
	})
}

// MustResolveFromRequestScope resolves a scoped service from the request's
// scope and panics for an unregistered one.
func TestMustResolveFromRequestScope(t *testing.T) {
	t.Parallel()

	app := gas.NewApp(gas.WithService[*testDep](newTestDep, gas.ServiceLifetimeScoped))
	if err := app.InitServices(context.Background()); err != nil {
		t.Fatal(err)
	}
	app.Router().Get(nil, "/ok", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(gas.MustResolveFromRequestScope[*testDep](r).Value))
	})
	app.Router().Get(nil, "/missing", func(w http.ResponseWriter, r *http.Request) {
		assertPanics(t, "MustResolveFromRequestScope of an unregistered type", func() {
			gas.MustResolveFromRequestScope[*testDepB](r)
		})
		w.WriteHeader(http.StatusNoContent)
	})

	rr := httptest.NewRecorder()
	app.Router().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/ok", nil))
	if rr.Body.String() != "resolved" {
		t.Errorf("GET /ok body = %q, want %q", rr.Body.String(), "resolved")
	}

	rr = httptest.NewRecorder()
	app.Router().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/missing", nil))
	if rr.Code != http.StatusNoContent {
		t.Errorf("GET /missing = %d, want 204", rr.Code)
	}
}
