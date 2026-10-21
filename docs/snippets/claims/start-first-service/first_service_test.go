package startfirstservice_test

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/gasmod/gas"
)

// fakeDB satisfies gas.DatabaseProvider without a database.
type fakeDB struct{}

func (fakeDB) DB() *sql.DB                                                       { return nil }
func (fakeDB) Driver() string                                                    { return "fake" }
func (fakeDB) Ping(context.Context) error                                        { return nil }
func (fakeDB) Query(context.Context, string, ...any) (gas.Rows, error)           { return nil, nil }
func (fakeDB) Exec(context.Context, string, ...any) (gas.Result, error)          { return nil, nil }
func (fakeDB) BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)          { return nil, nil }
func (fakeDB) WithTx(context.Context, *sql.TxOptions, func(*sql.Tx) error) error { return nil }

// notes mirrors the service on the page.
type notes struct {
	router *gas.Router
	bus    *gas.EventBus
	db     gas.DatabaseProvider
}

func (s *notes) Name() string { return "notes" }

func (s *notes) Init(context.Context) error {
	s.router.Handle(s, http.MethodGet, "/notes/{id}", func(ctx gas.Context) error {
		return ctx.Text(http.StatusOK, ctx.Param("id"))
	})
	s.router.Register(s, "notes-mw", func(next http.Handler) http.Handler { return next })
	return nil
}

func (s *notes) Close(context.Context) error { return nil }

// Docs: start/first-service.mdx#the-shape
// "`New` is an ordinary constructor. Its parameters are dependencies, and the container fills every one of them in."
func TestConstructorParametersAreFilled(t *testing.T) {
	t.Parallel()
	var got *notes
	db := fakeDB{}
	app := gas.NewApp(
		gas.WithServiceInstance[gas.DatabaseProvider](db),
		gas.WithSingletonService[*notes](func(router *gas.Router, bus *gas.EventBus, db gas.DatabaseProvider) *notes {
			got = &notes{router: router, bus: bus, db: db}
			return got
		}),
	)
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	if got.router != app.Router() || got.bus != app.EventBus() || got.db != gas.DatabaseProvider(db) {
		t.Fatalf("constructor got router=%p bus=%p db=%v, want the app's router, bus and the registered db",
			got.router, got.bus, got.db)
	}
}

// Docs: start/first-service.mdx#the-shape
// "It runs at startup as the service is built, after the services it depends on, and before the server accepts traffic."
// "As the service is built": each Init follows its own constructor before the
// next service is constructed.
func TestInitRunsAsBuiltAfterDependencies(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	// Registered dependent first, so only dependency order puts kA first.
	w := gas.NewWorker(
		gas.WithSingletonService[*svc[kB]](func(*svc[kA]) *svc[kB] { rec.add("build kB"); return &svc[kB]{rec: rec} }),
		gas.WithSingletonService[*svc[kA]](func() *svc[kA] { rec.add("build kA"); return &svc[kA]{rec: rec} }),
	)
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
	got := rec.list()
	want := []string{"build kA", "init kA", "build kB", "init kB"}
	if !slices.Equal(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
}

// Docs: start/first-service.mdx#the-shape
// "Everything registered here is tagged with the service that registered it"
func TestInitRegistrationsAreTaggedWithService(t *testing.T) {
	t.Parallel()
	app := gas.NewApp(gas.WithSingletonService[*notes](func(r *gas.Router) *notes { return &notes{router: r} }))
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	routes := app.Router().Routes()["notes"]
	if !slices.ContainsFunc(routes, func(r gas.RegisteredRoute) bool { return r.Path == "/notes/{id}" }) {
		t.Fatalf("Routes()[notes] = %v, want /notes/{id}", routes)
	}
	if owner := app.Router().NamedMiddleware()["notes-mw"]; owner != "notes" {
		t.Fatalf("NamedMiddleware()[notes-mw] = %q, want notes", owner)
	}
}

// Docs: start/first-service.mdx#the-shape
// "`Close` releases whatever the service holds. It runs at shutdown in reverse initialization order"
func TestCloseRunsInReverseInitOrder(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	w := gas.NewWorker(
		gas.WithSingletonService[*svc[kA]](func() *svc[kA] { return &svc[kA]{rec: rec} }),
		gas.WithSingletonService[*svc[kB]](func(*svc[kA]) *svc[kB] { return &svc[kB]{rec: rec} }),
		gas.WithSingletonService[*svc[kC]](func(*svc[kB]) *svc[kC] { return &svc[kC]{rec: rec} }),
	)
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := w.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if got, want := rec.list(), []string{"init kA", "init kB", "init kC", "close kC", "close kB", "close kA"}; !slices.Equal(got, want) {
		t.Fatalf("lifecycle = %v, want %v", got, want)
	}
}

type initOnly struct{}

func (initOnly) Init(context.Context) error { return nil }

type plainDep struct{ n int }

// Docs: start/first-service.mdx#the-shape
// "A type that declares `Init` or `Close` must implement all three methods. Anything short of that is rejected at startup with an error naming what's missing ... A type with none of them is an ordinary dependency and is unaffected."
func TestServiceShape(t *testing.T) {
	t.Parallel()
	t.Run("partial service rejected naming what is missing", func(t *testing.T) {
		t.Parallel()
		w := gas.NewWorker(gas.WithSingletonService[*initOnly](func() *initOnly { return &initOnly{} }))
		err := w.Start(t.Context())
		if err == nil {
			t.Fatal("Start accepted a type with only Init")
		}
		if !strings.Contains(err.Error(), "Close") || !strings.Contains(err.Error(), "Name") {
			t.Fatalf("error %q does not name the missing Name and Close", err)
		}
	})
	t.Run("type with none is an ordinary dependency", func(t *testing.T) {
		t.Parallel()
		var got *plainDep
		w := gas.NewWorker(
			gas.WithSingletonService[*plainDep](func() *plainDep { return &plainDep{n: 7} }),
			gas.WithSingletonService[*svc[kA]](func(d *plainDep) *svc[kA] { got = d; return &svc[kA]{} }),
		)
		if err := w.Start(t.Context()); err != nil {
			t.Fatalf("start: %v", err)
		}
		t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
		if got == nil || got.n != 7 {
			t.Fatalf("dependency = %v, want the registered plainDep", got)
		}
	})
}

type perRequest struct{ id int }

// Docs: start/first-service.mdx#handlers-can-declare-dependencies
// "typed handlers that take `gas.Context` first, dependencies in the middle, and return `error`. Each dependency is resolved from that request's scope."
func TestHandlerDependenciesComeFromRequestScope(t *testing.T) {
	t.Parallel()
	next := 0
	app := gas.NewApp(gas.WithScopedService[*perRequest](func() *perRequest { next++; return &perRequest{id: next} }))
	var fromMiddleware, fromHandler []*perRequest
	app.Router().UseMiddlewareFunc(func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fromMiddleware = append(fromMiddleware, gas.MustResolveFromRequestScope[*perRequest](r))
			h.ServeHTTP(w, r)
		})
	})
	app.Router().Handle(nil, http.MethodGet, "/x", func(ctx gas.Context, p *perRequest) error {
		fromHandler = append(fromHandler, p)
		return ctx.NoContent()
	})
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	for range 2 {
		app.Handler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
	}
	if len(fromHandler) != 2 || fromHandler[0] != fromMiddleware[0] || fromHandler[1] != fromMiddleware[1] {
		t.Fatalf("handler got %v, request scope held %v; want the same instance per request", fromHandler, fromMiddleware)
	}
	if fromHandler[0] == fromHandler[1] {
		t.Fatal("two requests shared one scoped instance")
	}
}

// Docs: start/first-service.mdx#handlers-can-declare-dependencies
// "Returning a `gas.Error` renders it at the right status; returning anything else becomes a 500 with the real error logged."
func TestHandlerErrorRendering(t *testing.T) {
	t.Parallel()
	sink, logger := newSink()
	app := gas.NewApp(gas.WithSingletonService[gas.Logger](logger))
	app.Router().Handle(nil, http.MethodGet, "/gone", func(gas.Context) error { return gas.NotFound("note not found") })
	app.Router().Handle(nil, http.MethodGet, "/raw", func(gas.Context) error {
		return errors.New("disk on fire 7f3a")
	})
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })

	rr := httptest.NewRecorder()
	app.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/gone", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("gas.NotFound rendered %d, want 404", rr.Code)
	}

	rr = httptest.NewRecorder()
	app.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/raw", nil))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("raw error rendered %d, want 500", rr.Code)
	}
	if strings.Contains(rr.Body.String(), "disk on fire 7f3a") {
		t.Fatalf("raw error leaked into the response: %s", rr.Body.String())
	}
	if !strings.Contains(sink.String(), "disk on fire 7f3a") {
		t.Fatalf("real error not logged; log:\n%s", sink.String())
	}
}

// Docs: start/first-service.mdx#handlers-can-declare-dependencies
// "At startup the router validates that every handler dependency is registered. A typo fails the build of the app, not the first request that hits the route."
func TestUnregisteredHandlerDependencyFailsStartup(t *testing.T) {
	t.Parallel()
	app := gas.NewApp()
	app.Router().Handle(nil, http.MethodGet, "/x", func(gas.Context, gas.DatabaseProvider) error { return nil })
	if err := app.Start(t.Context()); err == nil {
		_ = app.Stop(context.Background())
		t.Fatal("Start succeeded with an unregistered handler dependency")
	}
}
