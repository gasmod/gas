package corehandlers_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config/configtest"
	"github.com/gasmod/gas/docs/snippets/claims/internal/testport"
)

// svc is a singleton service whose Init runs init. Each K is its own
// registration type, so tests can close and restart one by type.
type svc[K any] struct {
	router *gas.Router
	init   func(s *svc[K])
	name   string
}

func (s *svc[K]) Name() string { return s.name }

func (s *svc[K]) Init(context.Context) error {
	if s.init != nil {
		s.init(s)
	}
	return nil
}

func (s *svc[K]) Close(context.Context) error { return nil }

// withSvc registers a *svc[K] named name that takes the router from the
// container and runs init during Init.
func withSvc[K any](name string, init func(s *svc[K])) gas.WorkerOption {
	return gas.WithSingletonService[*svc[K]](func(r *gas.Router) *svc[K] {
		return &svc[K]{router: r, init: init, name: name}
	})
}

type kA struct{}

func start(t *testing.T, app *gas.App) {
	t.Helper()
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })
}

func serve(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func get(h http.Handler, path string) *httptest.ResponseRecorder {
	return serve(h, httptest.NewRequest(http.MethodGet, path, nil))
}

func ok(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }

// freePort returns a port reserved for this test process; see
// testport.Free for why a port-0 listener is not enough.
func freePort(t *testing.T) int {
	t.Helper()
	return testport.Free(t)
}

func mustConfig(t *testing.T, values map[string]any) gas.ConfigProvider {
	t.Helper()
	cfg, err := configtest.NewMockConfigWithValues(values)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	return cfg
}

// mustPanic runs fn and fails unless it panics.
func mustPanic(t *testing.T, what string, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Errorf("%s: no panic", what)
		}
	}()
	fn()
}
