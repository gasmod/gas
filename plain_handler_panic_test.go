package gas_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gasmod/gas"
)

func panicking(w http.ResponseWriter, r *http.Request) { panic("plain boom") }

// A plain http.HandlerFunc that panics gets the same recovery as a DI-aware
// handler: the error handler answers 500.
func TestRouter_PlainHandlerPanicRecovered(t *testing.T) {
	var got error
	app := gas.NewApp(gas.WithErrorHandler(func(ctx gas.Context, err error) {
		got = err
		_ = ctx.Text(http.StatusInternalServerError, "recovered")
	}))
	app.Router().Handle(nil, http.MethodGet, "/plain", http.HandlerFunc(panicking))
	app.Router().Handle(nil, http.MethodGet, "/func", panicking)
	if err := app.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })

	for _, path := range []string{"/plain", "/func"} {
		got = nil
		rr := httptest.NewRecorder()
		app.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusInternalServerError || rr.Body.String() != "recovered" {
			t.Errorf("GET %s = %d %q, want 500 %q", path, rr.Code, rr.Body.String(), "recovered")
		}
		if got == nil {
			t.Errorf("GET %s: error handler not called", path)
		}
	}
}

// A plain NotFound handler that panics is recovered too.
func TestRouter_PlainNotFoundPanicRecovered(t *testing.T) {
	app := gas.NewApp()
	// chi runs middleware, including the request scope, only once a route
	// exists, so register one as a real app would.
	app.Router().Handle(nil, http.MethodGet, "/", func(http.ResponseWriter, *http.Request) {})
	app.Router().NotFound(nil, http.HandlerFunc(panicking))
	if err := app.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })

	rr := httptest.NewRecorder()
	app.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/nowhere", nil))
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("GET /nowhere = %d, want 500", rr.Code)
	}
}

// Recovery works on a router used without an App, where there is no
// request scope to resolve a logger from.
func TestRouter_PlainHandlerPanicWithoutApp(t *testing.T) {
	router := gas.NewRouter()
	router.Handle(nil, http.MethodGet, "/plain", panicking)
	router.Seal()

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/plain", nil))
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("GET /plain = %d, want 500", rr.Code)
	}
}

// http.ErrAbortHandler still aborts the request: it is re-panicked for
// net/http rather than turned into a response.
func TestRouter_PlainHandlerAbortNotRecovered(t *testing.T) {
	router := gas.NewRouter()
	router.Handle(nil, http.MethodGet, "/abort", func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	})
	router.Seal()

	defer func() {
		rec := recover()
		if err, ok := rec.(error); !ok || !errors.Is(err, http.ErrAbortHandler) {
			t.Errorf("recovered %v, want http.ErrAbortHandler", rec)
		}
	}()
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/abort", nil))
}
