package coreservices_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver

	"github.com/gasmod/gas"
	cachemem "github.com/gasmod/gas/cache/memory"
	"github.com/gasmod/gas/database"
	"github.com/gasmod/gas/migrate"
)

// Docs: core/services.mdx#the-service-interface
// "type Service interface { Name() string ... Init(ctx context.Context) error ... Close(ctx context.Context) error ... }"
func TestServiceInterfaceShape(t *testing.T) {
	t.Parallel()
	typ := reflect.TypeFor[gas.Service]()
	want := map[string]reflect.Type{
		"Name":  reflect.TypeFor[func() string](),
		"Init":  reflect.TypeFor[func(context.Context) error](),
		"Close": reflect.TypeFor[func(context.Context) error](),
	}
	if typ.NumMethod() != len(want) {
		t.Fatalf("gas.Service has %d methods, want %d", typ.NumMethod(), len(want))
	}
	for name, sig := range want {
		m, ok := typ.MethodByName(name)
		if !ok || m.Type != sig {
			t.Fatalf("gas.Service.%s = %v (present=%v), want %v", name, m.Type, ok, sig)
		}
	}
}

// Docs: core/services.mdx#the-service-interface
// "The
// container calls `Init(ctx)` after constructing it, and `Close(ctx)` at shutdown
// (singletons) or when its scope closes (scoped services)."
func TestInitAfterConstructCloseAtShutdownOrScopeClose(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	w := gas.NewWorker(
		gas.WithSingletonService[*svc[kA]](func() *svc[kA] { rec.add("build kA"); return &svc[kA]{rec: rec} }),
		gas.WithScopedService[*svc[kB]](func() *svc[kB] { rec.add("build kB"); return &svc[kB]{rec: rec} }),
	)
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	scope := w.ServiceContainer().NewScope(t.Context())
	scope.MustResolve[*svc[kB]]()
	if err := scope.Close(context.Background()); err != nil {
		t.Fatalf("scope close: %v", err)
	}
	if got, want := rec.list(), []string{"build kA", "init kA", "build kB", "init kB", "close kB"}; !slices.Equal(got, want) {
		t.Fatalf("before shutdown = %v, want %v", got, want)
	}
	if err := w.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if got := rec.list(); got[len(got)-1] != "close kA" || len(got) != 6 {
		t.Fatalf("after shutdown = %v, want the singleton closed last and once", got)
	}
}

// store declares neither Init nor Close, like the page's Store.
type store struct{ notes []string }

// notes is the page's Notes service.
type notes struct {
	router *gas.Router
	logger gas.Logger
	store  *store
}

func (s *notes) Name() string { return "notes" }

func (s *notes) Init(context.Context) error {
	s.router.Handle(s, http.MethodGet, "/notes", func(ctx gas.Context) error {
		return ctx.JSON(http.StatusOK, s.store.notes)
	})
	return nil
}

func (s *notes) Close(context.Context) error { return nil }

func newNotesApp() *gas.App {
	return gas.NewApp(
		gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
		gas.WithSingletonService[*store](func() *store { return &store{notes: []string{}} }),
		gas.WithSingletonService[*notes](func(r *gas.Router, l gas.Logger, s *store) *notes {
			return &notes{router: r, logger: l, store: s}
		}),
	)
}

// Docs: core/services.mdx#the-service-interface
// "`NewNotes` is an ordinary function. Its parameters are its dependencies, and
// the container fills each one in by its type."
// Docs: core/services.mdx#the-service-interface
// "`Store` declares neither `Init` nor
// `Close`, so the container builds it and hands it out, and that is all"
func TestConstructorParamsFilledByTypeAndStoreIsPlain(t *testing.T) {
	t.Parallel()
	app := newNotesApp()
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	c := app.ServiceContainer()
	n := c.MustResolve[*notes]()
	if n.router != app.Router() || n.logger != c.MustResolve[gas.Logger]() || n.store != c.MustResolve[*store]() {
		t.Fatal("NewNotes did not receive the container's router, logger, and store")
	}
	if rr := get(app.Handler(), "/notes"); rr.Code != http.StatusOK || strings.TrimSpace(rr.Body.String()) != "[]" {
		t.Fatalf("GET /notes = %d %q, want 200 []", rr.Code, rr.Body.String())
	}
	if !slices.Contains(app.ActiveServices(), "notes") {
		t.Errorf("ActiveServices = %v, want notes in it", app.ActiveServices())
	}
}

// Docs: core/services.mdx#the-service-interface
// "Each registration takes the service as its owner, which is
// what lets one service be torn down at runtime"
func TestOwnerLetsServiceBeTornDown(t *testing.T) {
	t.Parallel()
	app := newNotesApp()
	app.Router().Get(nil, "/other", func(ctx gas.Context) error { return ctx.NoContent() })
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	if routes := app.Router().Routes()["notes"]; len(routes) != 1 {
		t.Fatalf("Routes()[notes] = %v, want the route Init registered", routes)
	}
	if err := app.CloseService[*notes](t.Context()); err != nil {
		t.Fatalf("CloseService: %v", err)
	}
	if rr := get(app.Handler(), "/notes"); rr.Code != http.StatusServiceUnavailable {
		t.Errorf("GET /notes after CloseService = %d, want 503", rr.Code)
	}
	if rr := get(app.Handler(), "/other"); rr.Code >= 500 {
		t.Errorf("GET /other after CloseService = %d, want it still served", rr.Code)
	}
}

type initNoCtx struct{}

func (initNoCtx) Name() string                { return "init-no-ctx" }
func (initNoCtx) Init() error                 { return nil }
func (initNoCtx) Close(context.Context) error { return nil }

type initOnly struct{}

func (initOnly) Init(context.Context) error { return nil }

type nameOnly struct{}

func (nameOnly) Name() string { return "name-only" }

// ptrService declares its methods on the pointer and is registered by value.
type ptrService struct{}

func (*ptrService) Name() string                { return "ptr" }
func (*ptrService) Init(context.Context) error  { return nil }
func (*ptrService) Close(context.Context) error { return nil }

// Docs: core/services.mdx#service-shape
// "A registered type that declares `Init` or `Close` must implement all three
// methods, with the signatures above. One that declares either and falls short,
// for example an `Init` without the `context.Context` parameter, or an `Init`
// with no `Close`, fails startup with an error that wraps
// `gas.ErrInvalidServiceShape` and names each missing or mistyped method."
func TestPartialServicesRejected(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		opt   gas.Option
		names []string
	}{
		"Init without context": {
			gas.WithSingletonService[*initNoCtx](func() *initNoCtx { return &initNoCtx{} }),
			[]string{"Init"},
		},
		"Init with no Close": {
			gas.WithSingletonService[*initOnly](func() *initOnly { return &initOnly{} }),
			[]string{"Name", "Close"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := gas.NewApp(tc.opt).Start(t.Context())
			if !errors.Is(err, gas.ErrInvalidServiceShape) {
				t.Fatalf("Start = %v, want ErrInvalidServiceShape", err)
			}
			// The part of the message in parentheses lists the problems.
			_, problems, _ := strings.Cut(err.Error(), "(")
			for _, m := range tc.names {
				if !strings.Contains(problems, m) {
					t.Errorf("error %q does not name %s among the problems", err, m)
				}
			}
		})
	}
}

// Docs: core/services.mdx#service-shape
// "`Name` alone does not count: a type that declares only `Name()` is an ordinary
// dependency."
// Docs: core/services.mdx#service-shape
// "A registration under a value type whose methods are declared on the
// pointer is rejected too, with a message telling you to register the pointer
// type."
func TestNameOnlyAndValueTypeRegistration(t *testing.T) {
	t.Parallel()
	app := gas.NewApp(gas.WithSingletonService[*nameOnly](func() *nameOnly { return &nameOnly{} }))
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("Start with a Name-only type: %v", err)
	}
	_ = app.Stop(context.Background())
	if slices.Contains(app.ActiveServices(), "name-only") {
		t.Error("a Name-only type was tracked as a service")
	}

	err := gas.NewApp(gas.WithSingletonService[ptrService](func() ptrService { return ptrService{} })).Start(t.Context())
	if !errors.Is(err, gas.ErrInvalidServiceShape) {
		t.Fatalf("Start with a value registration = %v, want ErrInvalidServiceShape", err)
	}
	if want := "register it as *" + reflect.TypeFor[ptrService]().String(); !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q does not tell to %s", err, want)
	}
}

// dbService wraps a *sql.DB in a service of its own.
type dbService struct{ db *sql.DB }

func (s *dbService) Name() string                   { return "db" }
func (s *dbService) Init(ctx context.Context) error { return s.db.PingContext(ctx) }
func (s *dbService) Close(context.Context) error    { return s.db.Close() }

// Docs: core/services.mdx#service-shape
// "A type from another package that declares `Close`, such as `*sql.DB`, cannot be
// given the other two methods. Wrap it in a service of your own instead of
// registering it directly."
func TestSQLDBMustBeWrapped(t *testing.T) {
	t.Parallel()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := gas.NewApp(gas.WithServiceInstance(db)).Start(t.Context()); !errors.Is(err, gas.ErrInvalidServiceShape) {
		t.Fatalf("Start with *sql.DB registered directly = %v, want ErrInvalidServiceShape", err)
	}

	db2, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	app := gas.NewApp(gas.WithServiceInstance(&dbService{db: db2}))
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("Start with the wrapped *sql.DB: %v", err)
	}
	if err := app.Stop(context.Background()); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := db2.PingContext(t.Context()); err == nil {
		t.Error("the wrapper's Close did not close the database at shutdown")
	}
}

// reporter is a service that reports healthy.
type reporter struct{ svc[kA] }

func (*reporter) CheckHealth(context.Context) error { return nil }

// Docs: core/services.mdx#names
// "`Name()` identifies a service at runtime: active services, health and
// readiness results, the kill switch, and the ownership of routes and
// subscriptions are all keyed by it."
// The kill switch is shown to be keyed by name by a route whose owner is
// another value with the same Name, which also answers 503.
func TestNameKeysRuntimeState(t *testing.T) {
	t.Parallel()
	rep := &reporter{svc: svc[kA]{name: "search"}}
	app := gas.NewApp(gas.WithServiceInstance(rep))
	r := app.Router()
	ok := func(ctx gas.Context) error { return ctx.NoContent() }
	r.Get(rep, "/search", ok)
	r.Get(&svc[kB]{name: "search"}, "/same-name", ok)
	r.Register(rep, "search-mw", func(next http.Handler) http.Handler { return next })
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })

	if !slices.Contains(app.ActiveServices(), "search") {
		t.Errorf("ActiveServices = %v, want search", app.ActiveServices())
	}
	if res, ok := app.CheckHealth(t.Context())["search"]; !ok || res != nil {
		t.Errorf("CheckHealth()[search] = %v (present=%v), want nil", res, ok)
	}
	if routes := r.Routes()["search"]; len(routes) != 2 {
		t.Errorf("Routes()[search] = %v, want both routes", routes)
	}
	if got := r.NamedMiddleware()["search-mw"]; got != "search" {
		t.Errorf("NamedMiddleware()[search-mw] = %q, want search", got)
	}
	if err := app.CloseService[*reporter](t.Context()); err != nil {
		t.Fatalf("CloseService: %v", err)
	}
	for _, p := range []string{"/search", "/same-name"} {
		if rr := get(app.Handler(), p); rr.Code != http.StatusServiceUnavailable {
			t.Errorf("GET %s after CloseService = %d, want 503", p, rr.Code)
		}
	}
}

// Docs: core/services.mdx#names
// "A Worker tracks one active service per name,
// so a second service with the same name is not tracked, and shutdown does not
// close it."
// Assumption: which of the two is "second" is not settled, so the test wants
// exactly one of them tracked and closed.
func TestDuplicateNameNotTrackedOrClosed(t *testing.T) {
	t.Parallel()
	a := &svc[kA]{name: "dup"}
	b := &svc[kB]{name: "dup"}
	w := gas.NewWorker(
		gas.WithSingletonService[*svc[kA]](func() *svc[kA] { return a }),
		gas.WithSingletonService[*svc[kB]](func() *svc[kB] { return b }),
	)
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	n := 0
	for _, name := range w.ActiveServices() {
		if name == "dup" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("ActiveServices lists dup %d times, want 1", n)
	}
	if err := w.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if a.inits() != 1 || b.inits() != 1 {
		t.Errorf("Init ran %d and %d times, want both once", a.inits(), b.inits())
	}
	if a.closes()+b.closes() != 1 {
		t.Errorf("Close ran %d and %d times, want exactly one of them closed", a.closes(), b.closes())
	}
}

// mailer is an interface; smtpMailer implements it.
type mailer interface{ Send() error }

type smtpMailer struct{}

func (*smtpMailer) Send() error { return nil }

// Docs: core/services.mdx#registering
// "The type parameter is the key the value is stored under. Every resolution is by
// exact type; the container never searches its registrations for a type that
// happens to satisfy an interface"
func TestTypeParameterIsTheKey(t *testing.T) {
	t.Parallel()
	c := gas.NewServiceContainer()
	c.RegisterSingletonService[*smtpMailer](func() *smtpMailer { return &smtpMailer{} })
	if _, err := c.Resolve[mailer](); !errors.Is(err, gas.ErrNotRegistered) {
		t.Fatalf("Resolve[mailer] with only *smtpMailer registered = %v, want ErrNotRegistered", err)
	}
	c2 := gas.NewServiceContainer()
	c2.RegisterSingletonService[mailer](func() *smtpMailer { return &smtpMailer{} })
	if _, err := c2.Resolve[*smtpMailer](); !errors.Is(err, gas.ErrNotRegistered) {
		t.Fatalf("Resolve[*smtpMailer] with it registered as mailer = %v, want ErrNotRegistered", err)
	}
	if _, err := c2.Resolve[mailer](); err != nil {
		t.Fatalf("Resolve[mailer]: %v", err)
	}
}

type (
	holder  struct{ id int }
	holder2 struct{}
	dep     struct{}
	missing struct{}
)

// Docs: core/services.mdx#constructors
// "A constructor is a function that takes its dependencies as parameters and
// returns the registered type, optionally with an error"
// Docs: core/services.mdx#constructors
// "A non-nil error from a constructor fails the build, and with it startup."
func TestConstructorForms(t *testing.T) {
	t.Parallel()
	w := gas.NewWorker(
		gas.WithSingletonService[*dep](func() *dep { return &dep{} }),
		gas.WithSingletonService[*holder](func(*dep) *holder { return &holder{id: 1} }),
		gas.WithSingletonService[*holder2](func(*dep, *holder) (*holder2, error) { return &holder2{}, nil }),
	)
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	_ = w.Shutdown(context.Background())

	boom := errors.New("boom")
	failing := gas.NewWorker(gas.WithSingletonService[*holder](func() (*holder, error) { return nil, boom }))
	if err := failing.Start(t.Context()); !errors.Is(err, boom) {
		t.Fatalf("Start with a failing constructor = %v, want it to wrap the constructor's error", err)
	}
}

// Docs: core/services.mdx#constructor-checks
// "The constructor is checked when it is registered. Registration panics if it is
// not a function, is variadic, returns anything other than one or two values,
// returns a first value not assignable to the registered type (or, for an
// interface type, not implementing it), or returns a second value that is not an
// `error`."
func TestMalformedConstructorPanicsAtRegistration(t *testing.T) {
	t.Parallel()
	c := gas.NewServiceContainer()
	mustPanic(t, "not a function", func() { c.RegisterSingletonService[*holder](42) })
	mustPanic(t, "variadic", func() { c.RegisterSingletonService[*holder](func(...*dep) *holder { return nil }) })
	mustPanic(t, "no return values", func() { c.RegisterSingletonService[*holder](func() {}) })
	mustPanic(t, "three return values", func() {
		//nolint:staticcheck // the malformed signature is what is under test
		c.RegisterSingletonService[*holder](func() (*holder, error, int) { return nil, nil, 0 })
	})
	mustPanic(t, "first value not assignable", func() { c.RegisterSingletonService[*holder](func() *holder2 { return nil }) })
	mustPanic(t, "first value not implementing the interface", func() {
		c.RegisterSingletonService[mailer](func() *holder { return nil })
	})
	mustPanic(t, "second value not an error", func() {
		c.RegisterSingletonService[*holder](func() (*holder, int) { return nil, 0 })
	})
	mustPanic(t, "option form", func() { gas.NewWorker(gas.WithSingletonService[*holder](42)) })
}

// Docs: core/services.mdx#constructor-checks
// "Parameters are not checked at registration, since whether they resolve
// is only known once everything is registered; a parameter with no registration
// fails the build."
func TestParametersCheckedAtBuild(t *testing.T) {
	t.Parallel()
	c := gas.NewServiceContainer()
	if r := recovered(func() { c.RegisterSingletonService[*holder](func(*dep) *holder { return &holder{} }) }); r != nil {
		t.Fatalf("registering a constructor whose parameter is not yet registered panicked: %v", r)
	}
	c.RegisterSingletonService[*dep](func() *dep { return &dep{} })
	if err := c.BuildAll(t.Context()); err != nil {
		t.Fatalf("BuildAll with the dependency registered afterwards: %v", err)
	}

	c2 := gas.NewServiceContainer()
	c2.RegisterSingletonService[*holder](func(*missing) *holder { return &holder{} })
	if err := c2.BuildAll(t.Context()); !errors.Is(err, gas.ErrNotRegistered) {
		t.Fatalf("BuildAll with an unregistered parameter = %v, want ErrNotRegistered", err)
	}
}

// Docs: core/services.mdx#overriding-a-registration
// "Registering a type again replaces its earlier registration: the last one
// wins."
func TestRegisteringTwiceReplaces(t *testing.T) {
	t.Parallel()
	w := gas.NewWorker(
		gas.WithSingletonService[*holder](func() *holder { return &holder{id: 1} }),
		gas.WithSingletonService[*holder](func() *holder { return &holder{id: 2} }),
	)
	w.RegisterSingletonService[*dep](func() *dep { t.Error("the replaced constructor ran"); return &dep{} })
	w.RegisterServiceInstance[*dep](&dep{})
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
	if got := w.ServiceContainer().MustResolve[*holder]().id; got != 2 {
		t.Fatalf("resolved holder %d, want the second registration", got)
	}
}

// Docs: core/services.mdx#instances-override-constructors
// "a value registered with `WithServiceInstance` wins over a constructor
// registration for the same type, whichever was registered last and whatever
// the constructor's lifetime. The constructor never runs."
func TestInstanceWinsOverConstructor(t *testing.T) {
	t.Parallel()
	ctor := func(t *testing.T) func() *holder {
		return func() *holder { t.Error("the constructor ran"); return &holder{id: 1} }
	}
	cases := map[string]func(t *testing.T) []gas.Option{
		"instance last": func(t *testing.T) []gas.Option {
			return []gas.Option{
				gas.WithSingletonService[*holder](ctor(t)),
				gas.WithServiceInstance(&holder{id: 2}),
			}
		},
		"instance first": func(t *testing.T) []gas.Option {
			return []gas.Option{
				gas.WithServiceInstance(&holder{id: 2}),
				gas.WithSingletonService[*holder](ctor(t)),
			}
		},
		"instance first, scoped constructor": func(t *testing.T) []gas.Option {
			return []gas.Option{
				gas.WithServiceInstance(&holder{id: 2}),
				gas.WithScopedService[*holder](ctor(t)),
			}
		},
		"instance first, transient constructor": func(t *testing.T) []gas.Option {
			return []gas.Option{
				gas.WithServiceInstance(&holder{id: 2}),
				gas.WithTransientService[*holder](ctor(t)),
			}
		},
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			w := gas.NewWorker(opts(t)...)
			if err := w.Start(t.Context()); err != nil {
				t.Fatalf("start: %v", err)
			}
			t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
			if got := w.ServiceContainer().MustResolve[*holder]().id; got != 2 {
				t.Fatalf("container resolved holder %d, want the instance", got)
			}
			scope := w.ServiceContainer().NewScope(t.Context())
			defer func() { _ = scope.Close(context.Background()) }()
			if got := scope.MustResolve[*holder]().id; got != 2 {
				t.Fatalf("scope resolved holder %d, want the instance", got)
			}
		})
	}
}

// Docs: core/services.mdx#instances-override-constructors
// "To replace an instance, register another instance."
func TestInstanceReplacesInstance(t *testing.T) {
	t.Parallel()
	w := gas.NewWorker(
		gas.WithServiceInstance(&holder{id: 1}),
		gas.WithServiceInstance(&holder{id: 2}),
	)
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
	if got := w.ServiceContainer().MustResolve[*holder]().id; got != 2 {
		t.Fatalf("resolved holder %d, want the second instance", got)
	}
}

// Docs: core/services.mdx#lifetimes
// "| Singleton | `WithSingletonService` | One, shared by every consumer | `Init` when built at startup, `Close` at shutdown |"
func TestSingletonLifetime(t *testing.T) {
	t.Parallel()
	var built atomic.Int32
	w := gas.NewWorker(gas.WithSingletonService[*svc[kA]](func() *svc[kA] { built.Add(1); return &svc[kA]{} }))
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	c := w.ServiceContainer()
	s := c.MustResolve[*svc[kA]]()
	if s.inits() != 1 {
		t.Fatalf("Init ran %d times by the end of Start, want 1", s.inits())
	}
	scope := c.NewScope(t.Context())
	if c.MustResolve[*svc[kA]]() != s || scope.MustResolve[*svc[kA]]() != s || built.Load() != 1 {
		t.Fatal("a singleton resolved to more than one instance")
	}
	if s.closes() != 0 {
		t.Fatal("Close ran before shutdown")
	}
	_ = w.Shutdown(context.Background())
	if s.closes() != 1 {
		t.Fatalf("Close ran %d times at shutdown, want 1", s.closes())
	}
}

type requestLog struct{ n int32 }

type requestUser struct{ log *requestLog }

// Docs: core/services.mdx#lifetimes
// "| Scoped | `WithScopedService` | One per `Scope`; in an App, one per request | `Init` on first resolution in the scope, `Close` when the scope closes |"
func TestScopedLifetime(t *testing.T) {
	t.Parallel()
	var built atomic.Int32
	c := gas.NewServiceContainer()
	c.RegisterScopedService[*svc[kA]](func() *svc[kA] { built.Add(1); return &svc[kA]{} })
	if err := c.BuildAll(t.Context()); err != nil {
		t.Fatalf("BuildAll: %v", err)
	}
	if built.Load() != 0 {
		t.Fatal("a scoped service was built by BuildAll")
	}
	s1, s2 := c.NewScope(t.Context()), c.NewScope(t.Context())
	a := s1.MustResolve[*svc[kA]]()
	if a.inits() != 1 {
		t.Fatalf("Init ran %d times on first resolution, want 1", a.inits())
	}
	if s1.MustResolve[*svc[kA]]() != a || a.inits() != 1 {
		t.Fatal("a second resolution in the scope gave another instance or ran Init again")
	}
	if s2.MustResolve[*svc[kA]]() == a {
		t.Fatal("two scopes shared one instance")
	}
	if a.closes() != 0 {
		t.Fatal("Close ran before the scope closed")
	}
	if err := s1.Close(context.Background()); err != nil {
		t.Fatalf("scope close: %v", err)
	}
	if a.closes() != 1 {
		t.Fatalf("Close ran %d times when the scope closed, want 1", a.closes())
	}
}

// Docs: core/services.mdx#lifetimes
// "One per `Scope`; in an App, one per request"
func TestScopedOnePerRequest(t *testing.T) {
	t.Parallel()
	var built atomic.Int32
	app := gas.NewApp(
		gas.WithScopedService[*requestLog](func() *requestLog { return &requestLog{n: built.Add(1)} }),
		gas.WithScopedService[*requestUser](func(l *requestLog) *requestUser { return &requestUser{log: l} }),
	)
	app.Router().Get(nil, "/r", func(ctx gas.Context, l *requestLog, u *requestUser) error {
		if u.log != l {
			return ctx.Text(http.StatusInternalServerError, "two instances in one request")
		}
		return ctx.NoContent()
	})
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	for range 2 {
		if rr := get(app.Handler(), "/r"); rr.Code >= 300 {
			t.Fatalf("GET /r = %d %q", rr.Code, rr.Body.String())
		}
	}
	if built.Load() != 2 {
		t.Fatalf("two requests built %d request logs, want 2", built.Load())
	}
}

// nonce has a field so that two instances never share an address.
type nonce struct{ _ int }

// Docs: core/services.mdx#lifetimes
// "| Transient | `WithTransientService` | A new one on every resolution | Not allowed: the type cannot implement `Service` |"
// Docs: core/services.mdx#instances-and-transients
// "**Transient** registrations of a type that implements `Service` panic at
// registration."
func TestTransientLifetime(t *testing.T) {
	t.Parallel()
	c := gas.NewServiceContainer()
	c.RegisterTransientService[*nonce](func() *nonce { return &nonce{} })
	if n1, n2 := c.MustResolve[*nonce](), c.MustResolve[*nonce](); n1 == n2 {
		t.Fatal("two resolutions of a transient gave one instance")
	}
	mustPanic(t, "container", func() {
		c.RegisterTransientService[*svc[kA]](func() *svc[kA] { return &svc[kA]{} })
	})
	mustPanic(t, "option", func() {
		gas.NewWorker(gas.WithTransientService[*svc[kB]](func() *svc[kB] { return &svc[kB]{} }))
	})
}

// Docs: core/services.mdx#lifetimes
// "| Instance | `WithServiceInstance` | The value you pass, treated as a singleton | `Init` at startup, `Close` at shutdown |"
// Docs: core/services.mdx#instances-and-transients
// "**Instances** are pre-built, not pre-initialized. If the value implements
// `Service`, the container calls its `Init` at startup and its `Close` at
// shutdown like any other singleton"
func TestInstanceLifetime(t *testing.T) {
	t.Parallel()
	inst := &svc[kA]{}
	w := gas.NewWorker(gas.WithServiceInstance(inst))
	if inst.inits() != 0 {
		t.Fatal("Init ran at registration")
	}
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	if w.ServiceContainer().MustResolve[*svc[kA]]() != inst {
		t.Fatal("the instance did not resolve to the value passed")
	}
	if inst.inits() != 1 || inst.closes() != 0 {
		t.Fatalf("after Start Init=%d Close=%d, want 1 and 0", inst.inits(), inst.closes())
	}
	_ = w.Shutdown(context.Background())
	if inst.closes() != 1 {
		t.Fatalf("Close ran %d times at shutdown, want 1", inst.closes())
	}
}

// Docs: core/services.mdx#lifetimes
// "`gas.WithService[T](ctor, lifetime)` takes the lifetime as a value
// (`gas.ServiceLifetimeSingleton`, `gas.ServiceLifetimeScoped`, or
// `gas.ServiceLifetimeTransient`); the three shorthands call it."
func TestWithServiceTakesLifetime(t *testing.T) {
	t.Parallel()
	w := gas.NewWorker(
		gas.WithService[*svc[kA]](func() *svc[kA] { return &svc[kA]{} }, gas.ServiceLifetimeSingleton),
		gas.WithService[*requestLog](func() *requestLog { return &requestLog{} }, gas.ServiceLifetimeScoped),
		gas.WithService[*nonce](func() *nonce { return &nonce{} }, gas.ServiceLifetimeTransient),
	)
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
	c := w.ServiceContainer()
	if a1, a2 := c.MustResolve[*svc[kA]](), c.MustResolve[*svc[kA]](); a1 != a2 {
		t.Error("ServiceLifetimeSingleton gave two instances")
	}
	if _, err := c.Resolve[*requestLog](); !errors.Is(err, gas.ErrScopeRequired) {
		t.Errorf("ServiceLifetimeScoped resolved from the container = %v, want ErrScopeRequired", err)
	}
	s1, s2 := c.NewScope(t.Context()), c.NewScope(t.Context())
	l1, l1again, l2 := s1.MustResolve[*requestLog](), s1.MustResolve[*requestLog](), s2.MustResolve[*requestLog]()
	if l1 != l1again || l1 == l2 {
		t.Error("ServiceLifetimeScoped is not one per scope")
	}
	if n1, n2 := c.MustResolve[*nonce](), c.MustResolve[*nonce](); n1 == n2 {
		t.Error("ServiceLifetimeTransient gave one instance twice")
	}
	mustPanic(t, "WithService transient of a Service", func() {
		gas.NewWorker(gas.WithService[*svc[kB]](func() *svc[kB] { return &svc[kB]{} }, gas.ServiceLifetimeTransient))
	})
}

// Docs: core/services.mdx#instances-and-transients
// "An instance is stored under `T`, the static type at the call
// site, so passing an interface-typed variable registers it under that interface
// and not under its concrete type."
func TestInstanceStoredUnderStaticType(t *testing.T) {
	t.Parallel()
	var m mailer = &smtpMailer{}
	c := gas.NewServiceContainer()
	c.RegisterServiceInstance(m)
	if got, err := c.Resolve[mailer](); err != nil || got != m {
		t.Fatalf("Resolve[mailer] = %v, %v; want the instance", got, err)
	}
	if _, err := c.Resolve[*smtpMailer](); !errors.Is(err, gas.ErrNotRegistered) {
		t.Fatalf("Resolve[*smtpMailer] = %v, want ErrNotRegistered", err)
	}
}

// Docs: core/services.mdx#lifetimes
// "They cannot be resolved from the container itself, only from a `Scope`."
func TestScopedOnlyFromScope(t *testing.T) {
	t.Parallel()
	c := gas.NewServiceContainer()
	c.RegisterScopedService[*requestLog](func() *requestLog { return &requestLog{} })
	if _, err := c.Resolve[*requestLog](); !errors.Is(err, gas.ErrScopeRequired) {
		t.Fatalf("container Resolve = %v, want ErrScopeRequired", err)
	}
	if _, err := c.NewScope(t.Context()).Resolve[*requestLog](); err != nil {
		t.Fatalf("scope Resolve: %v", err)
	}
}

type (
	cache        struct{}
	cacheOnNonce struct{}
	scopedUser   struct{}
	clock        struct{}
)

// Docs: core/services.mdx#captive-dependencies
// "A singleton may not depend on a scoped or transient service."
// Docs: core/services.mdx#captive-dependencies
// "Startup fails with an error that wraps `gas.ErrCaptiveDependency`:"
// Docs: core/services.mdx#captive-dependencies
// "captive dependency: singleton *main.Cache depends on scoped *main.RequestLog"
// The message is checked for this package's types in place of main's.
func TestCaptiveDependency(t *testing.T) {
	t.Parallel()
	err := gas.NewApp(
		gas.WithScopedService[*requestLog](func() *requestLog { return &requestLog{} }),
		gas.WithSingletonService[*cache](func(*requestLog) *cache { return &cache{} }),
	).Start(t.Context())
	if !errors.Is(err, gas.ErrCaptiveDependency) {
		t.Fatalf("Start = %v, want ErrCaptiveDependency", err)
	}
	want := "captive dependency: singleton " + reflect.TypeFor[*cache]().String() +
		" depends on scoped " + reflect.TypeFor[*requestLog]().String()
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q does not contain %q", err, want)
	}

	err = gas.NewApp(
		gas.WithTransientService[*nonce](func() *nonce { return &nonce{} }),
		gas.WithSingletonService[*cacheOnNonce](func(*nonce) *cacheOnNonce { return &cacheOnNonce{} }),
	).Start(t.Context())
	if !errors.Is(err, gas.ErrCaptiveDependency) {
		t.Fatalf("singleton on transient: Start = %v, want ErrCaptiveDependency", err)
	}
}

// Docs: core/services.mdx#captive-dependencies
// "A scoped service may depend on
// singletons and on other scoped services. A dependency registered with
// `WithServiceInstance` is a singleton and is always allowed."
func TestAllowedLifetimeDependencies(t *testing.T) {
	t.Parallel()
	w := gas.NewWorker(
		gas.WithSingletonService[*holder](func() *holder { return &holder{} }),
		gas.WithServiceInstance(&clock{}),
		gas.WithScopedService[*requestLog](func() *requestLog { return &requestLog{} }),
		gas.WithScopedService[*scopedUser](func(*holder, *requestLog, *clock) *scopedUser { return &scopedUser{} }),
		gas.WithSingletonService[*holder2](func(*clock) *holder2 { return &holder2{} }),
	)
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
	if _, err := w.ServiceContainer().NewScope(t.Context()).Resolve[*scopedUser](); err != nil {
		t.Fatalf("scoped on singleton, scoped, and instance: %v", err)
	}
}

// Docs: core/services.mdx#register-under-the-type-consumers-ask-for
// "To reach a backend feature that the interface does not expose, type-assert the provider
// you were given"
func TestTypeAssertProvider(t *testing.T) {
	t.Parallel()
	w := gas.NewWorker(
		gas.WithServiceInstance(mustConfig(t, map[string]any{})),
		gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
		gas.WithSingletonService[gas.CacheProvider](cachemem.New()),
	)
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
	p := w.ServiceContainer().MustResolve[gas.CacheProvider]()
	if _, ok := p.(*cachemem.Service); !ok {
		t.Fatalf("the provider is %T, which does not type-assert to *memory.Service", p)
	}
}

// Docs: core/services.mdx#register-under-the-type-consumers-ask-for
// "Module constructors ask for the provider
// interfaces declared in core (`gas.ConfigProvider`, `gas.Logger`,
// `gas.DatabaseProvider`, and so on)"
func TestModuleConstructorsAskForCoreInterfaces(t *testing.T) {
	t.Parallel()
	ctors := map[string]any{
		"database.New": database.New(),
		"migrate.New":  migrate.New(),
		"memory.New":   cachemem.New(),
	}
	for name, ctor := range ctors {
		typ := reflect.TypeOf(ctor)
		for p := range typ.Ins() {
			if p.Kind() != reflect.Interface || p.PkgPath() != "github.com/gasmod/gas" {
				t.Errorf("%s constructor takes %v, want a core interface", name, p)
			}
		}
	}
}

type invalidShape struct{}

func (invalidShape) Close(context.Context) error { return nil }

// Docs: core/services.mdx#build-order
// "It first validates the service
// shape and lifetimes of every registration, then runs `Init` on every `Service`
// registered as an instance, in the order they were registered, then builds the
// singletons in dependency order."
// Assumption: validating lifetimes includes the captive-dependency check.
func TestBuildOrderValidateThenInstancesThenSingletons(t *testing.T) {
	t.Parallel()
	t.Run("instances in registration order before singletons", func(t *testing.T) {
		t.Parallel()
		rec := &recorder{}
		w := gas.NewWorker(
			gas.WithSingletonService[*svc[kD]](func(*svc[kC]) *svc[kD] { rec.add("build kD"); return &svc[kD]{rec: rec} }),
			gas.WithSingletonService[*svc[kC]](func() *svc[kC] { rec.add("build kC"); return &svc[kC]{rec: rec} }),
			gas.WithServiceInstance(&svc[kB]{rec: rec}),
			gas.WithServiceInstance(&svc[kA]{rec: rec}),
		)
		if err := w.Start(t.Context()); err != nil {
			t.Fatalf("start: %v", err)
		}
		_ = w.Shutdown(context.Background())
		got := rec.list()[:6]
		want := []string{"init kB", "init kA", "build kC", "init kC", "build kD", "init kD"}
		if !slices.Equal(got, want) {
			t.Fatalf("startup = %v, want %v", got, want)
		}
	})
	for name, bad := range map[string]gas.Option{
		"shape": gas.WithSingletonService[*invalidShape](func() *invalidShape { return &invalidShape{} }),
		"captive": gas.WithSingletonService[*cacheOnNonce](func(*nonce) *cacheOnNonce {
			return &cacheOnNonce{}
		}),
	} {
		t.Run("validates "+name+" before any Init", func(t *testing.T) {
			t.Parallel()
			inst := &svc[kA]{}
			w := gas.NewWorker(
				gas.WithTransientService[*nonce](func() *nonce { return &nonce{} }),
				gas.WithServiceInstance(inst),
				bad,
			)
			if err := w.Start(t.Context()); err == nil {
				t.Fatal("Start succeeded")
			}
			if inst.inits() != 0 {
				t.Fatal("an instance's Init ran before the registrations were validated")
			}
		})
	}
}

// Docs: core/services.mdx#build-order
// "A constructor runs only after every dependency
// it names has been constructed and, if that dependency is a service,
// initialized, so a service can use its dependencies from its constructor and
// from `Init`."
func TestConstructorRunsAfterDependencyInitialized(t *testing.T) {
	t.Parallel()
	var initsSeenByCtor int
	w := gas.NewWorker(
		gas.WithSingletonService[*holder](func(a *svc[kA]) *holder { initsSeenByCtor = a.inits(); return &holder{} }),
		gas.WithSingletonService[*svc[kA]](func() *svc[kA] { return &svc[kA]{} }),
	)
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	_ = w.Shutdown(context.Background())
	if initsSeenByCtor != 1 {
		t.Fatalf("the dependent's constructor saw %d Init calls on its dependency, want 1", initsSeenByCtor)
	}
}

// Docs: core/services.mdx#build-order
// "The order is the same on every run: singletons with no registered
// dependencies come first, in the order they were registered, and each of the
// others follows once everything it depends on has been built."
// kA is registered first but depends on kD, registered last; kC and kB have no
// dependencies and are registered in that order.
func TestBuildOrderIsFixed(t *testing.T) {
	t.Parallel()
	want := []string{"build kC", "build kB", "build kD", "build kA"}
	for range 50 {
		rec := &recorder{}
		c := gas.NewServiceContainer()
		c.RegisterSingletonService[*svc[kA]](func(*svc[kD]) *svc[kA] { rec.add("build kA"); return &svc[kA]{} })
		c.RegisterSingletonService[*svc[kC]](func() *svc[kC] { rec.add("build kC"); return &svc[kC]{} })
		c.RegisterSingletonService[*svc[kB]](func() *svc[kB] { rec.add("build kB"); return &svc[kB]{} })
		c.RegisterSingletonService[*svc[kD]](func() *svc[kD] { rec.add("build kD"); return &svc[kD]{} })
		if err := c.BuildAll(t.Context()); err != nil {
			t.Fatalf("BuildAll: %v", err)
		}
		if got := rec.list(); !slices.Equal(got, want) {
			t.Fatalf("build order %v, want %v", got, want)
		}
	}
}

// Docs: core/services.mdx#build-order
// "Because the order is fixed, so is the error startup reports when several
// registrations are broken. A captive dependency is reported before any build
// error, and of several, the one in the singleton registered first. Of several
// build errors, startup reports the first broken singleton in build order, which
// for singletons that wait on the same dependencies, or on none, is the one
// registered first. Fix it and the next one surfaces."
// Each case registers the Y types before the X types, so registration order
// is not alphabetical, and runs 50 fresh containers.
func TestBrokenRegistrationReportedIsFixed(t *testing.T) {
	t.Parallel()
	type (
		root     struct{}
		missingX struct{}
		missingY struct{}
		needsX   struct{}
		needsY   struct{}
		scopedX  struct{}
		scopedY  struct{}
		captiveX struct{}
		captiveY struct{}
	)
	newNeedsY := func(*missingY) *needsY { return &needsY{} }
	newNeedsX := func(*missingX) *needsX { return &needsX{} }
	cases := []struct {
		register func(c *gas.ServiceContainer)
		name     string
		want     string
	}{
		{
			register: func(c *gas.ServiceContainer) {
				c.RegisterSingletonService[*needsY](newNeedsY)
				c.RegisterSingletonService[*needsX](newNeedsX)
			},
			name: "no dependencies",
			want: "building *coreservices_test.needsY: ",
		},
		{
			register: func(c *gas.ServiceContainer) {
				c.RegisterSingletonService[*root](func() *root { return &root{} })
				c.RegisterSingletonService[*needsY](func(*root, *missingY) *needsY { return &needsY{} })
				c.RegisterSingletonService[*needsX](func(*root, *missingX) *needsX { return &needsX{} })
			},
			name: "same dependencies",
			want: "building *coreservices_test.needsY: ",
		},
		{
			register: func(c *gas.ServiceContainer) {
				c.RegisterSingletonService[*needsY](func(*root, *missingY) *needsY { return &needsY{} })
				c.RegisterSingletonService[*needsX](newNeedsX)
				c.RegisterSingletonService[*root](func() *root { return &root{} })
			},
			name: "first in build order, not registration order",
			want: "building *coreservices_test.needsX: ",
		},
		{
			register: func(c *gas.ServiceContainer) {
				c.RegisterScopedService[*scopedY](func() *scopedY { return &scopedY{} })
				c.RegisterScopedService[*scopedX](func() *scopedX { return &scopedX{} })
				c.RegisterSingletonService[*needsY](newNeedsY)
				c.RegisterSingletonService[*captiveY](func(*scopedY) *captiveY { return &captiveY{} })
				c.RegisterSingletonService[*captiveX](func(*scopedX) *captiveX { return &captiveX{} })
			},
			name: "captive before build error",
			want: "captive dependency: singleton *coreservices_test.captiveY depends on scoped",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for range 50 {
				c := gas.NewServiceContainer()
				tc.register(c)
				err := c.BuildAll(t.Context())
				if err == nil || !strings.HasPrefix(err.Error(), tc.want) {
					t.Fatalf("BuildAll() = %v, want an error starting %q", err, tc.want)
				}
			}
		})
	}
	// Fixing the reported registration surfaces the next one.
	c := gas.NewServiceContainer()
	c.RegisterSingletonService[*needsY](func() *needsY { return &needsY{} })
	c.RegisterSingletonService[*needsX](newNeedsX)
	if err := c.BuildAll(t.Context()); err == nil || !strings.HasPrefix(err.Error(), "building *coreservices_test.needsX: ") {
		t.Fatalf("after fixing needsY, BuildAll() = %v, want the needsX error", err)
	}
}

// Docs: core/services.mdx#build-order
// "Shutdown closes services in the reverse of the order they were initialized, so
// a service's dependencies are still open while it closes."
func TestShutdownReverseOfInit(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	w := gas.NewWorker(
		gas.WithSingletonService[*svc[kC]](func(*svc[kB]) *svc[kC] { return &svc[kC]{rec: rec} }),
		gas.WithSingletonService[*svc[kB]](func(*svc[kA]) *svc[kB] { return &svc[kB]{rec: rec} }),
		gas.WithSingletonService[*svc[kA]](func() *svc[kA] { return &svc[kA]{rec: rec} }),
		gas.WithServiceInstance(&svc[kD]{rec: rec}),
	)
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := w.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	var inits, closes []string
	for _, e := range rec.list() {
		if name, ok := strings.CutPrefix(e, "init "); ok {
			inits = append(inits, name)
		} else if name, ok := strings.CutPrefix(e, "close "); ok {
			closes = append(closes, name)
		}
	}
	slices.Reverse(inits)
	if !slices.Equal(closes, inits) {
		t.Fatalf("close order %v, want the reverse of init order", closes)
	}
}

// Docs: core/services.mdx#resolving
// "`Resolve` and `MustResolve` are generic methods on `*ServiceContainer` and on
// `*Scope`. `MustResolve` panics with a message that includes the same error
// text."
func TestResolveAndMustResolve(t *testing.T) {
	t.Parallel()
	c := gas.NewServiceContainer()
	c.RegisterSingletonService[*holder](func() *holder { return &holder{} })
	c.RegisterScopedService[*requestLog](func() *requestLog { return &requestLog{} })
	scope := c.NewScope(t.Context())
	if h, err := c.Resolve[*holder](); err != nil || h != c.MustResolve[*holder]() {
		t.Fatalf("container Resolve = %v, %v", h, err)
	}
	if l, err := scope.Resolve[*requestLog](); err != nil || l != scope.MustResolve[*requestLog]() {
		t.Fatalf("scope Resolve = %v, %v", l, err)
	}
	for name, pair := range map[string]struct {
		resolve func() error
		must    func()
	}{
		"container": {
			func() error { _, err := c.Resolve[*missing](); return err },
			func() { c.MustResolve[*missing]() },
		},
		"scope": {
			func() error { _, err := scope.Resolve[*missing](); return err },
			func() { scope.MustResolve[*missing]() },
		},
	} {
		err := pair.resolve()
		if err == nil {
			t.Fatalf("%s: Resolve of an unregistered type succeeded", name)
		}
		r := recovered(pair.must)
		var msg string
		switch v := r.(type) {
		case string:
			msg = v
		case error:
			msg = v.Error()
		default:
			t.Fatalf("%s: MustResolve panicked with %T %v", name, r, r)
		}
		if !strings.Contains(msg, err.Error()) {
			t.Errorf("%s: panic %q does not include %q", name, msg, err.Error())
		}
	}
}

// Docs: core/services.mdx#resolving
// "`CanResolve[T]()` on the container reports whether `T` has a
// registration or instance, or is an alias whose target has one, without
// building anything."
func TestCanResolve(t *testing.T) {
	t.Parallel()
	var built atomic.Bool
	c := gas.NewServiceContainer()
	c.RegisterSingletonService[*holder](func() *holder { built.Store(true); return &holder{} })
	c.RegisterServiceInstance(&clock{})
	if !c.CanResolve[*holder]() || !c.CanResolve[*clock]() {
		t.Error("CanResolve is false for a registration or an instance")
	}
	if c.CanResolve[*missing]() {
		t.Error("CanResolve is true for an unregistered type")
	}
	c.RegisterServiceAlias[*holder, fmt.Stringer]()
	if !c.CanResolve[fmt.Stringer]() {
		t.Error("CanResolve is false for an alias whose target is registered")
	}
	c.RegisterServiceAlias[*svc[kC], named]()
	if c.CanResolve[named]() {
		t.Error("CanResolve is true for an alias whose target is missing")
	}
	if built.Load() {
		t.Error("CanResolve built the singleton")
	}
}

// Docs: core/services.mdx#concurrent-resolution
// "Resolving a singleton that has not been built yet builds it, and its
// dependencies, on the spot."
func TestResolveBuildsOnTheSpot(t *testing.T) {
	t.Parallel()
	var built []string
	c := gas.NewServiceContainer()
	c.RegisterSingletonService[*holder](func(*dep) *holder { built = append(built, "holder"); return &holder{} })
	c.RegisterSingletonService[*dep](func() *dep { built = append(built, "dep"); return &dep{} })
	if _, err := c.Resolve[*holder](); err != nil {
		t.Fatalf("Resolve before BuildAll: %v", err)
	}
	if want := []string{"dep", "holder"}; !slices.Equal(built, want) {
		t.Fatalf("built %v, want %v", built, want)
	}
}

// Docs: core/services.mdx#concurrent-resolution
// "The container is safe for concurrent resolution: a
// singleton is built by one goroutine at a time, and another goroutine asking for
// it while it is being built waits and gets the same instance."
func TestConcurrentResolutionBuildsOnce(t *testing.T) {
	t.Parallel()
	var builds atomic.Int32
	entered := make(chan struct{})
	c := gas.NewServiceContainer()
	c.RegisterSingletonService[*holder](func() *holder {
		if builds.Add(1) == 1 {
			close(entered)
		}
		time.Sleep(100 * time.Millisecond)
		return &holder{}
	})
	const n = 16
	got := make([]*holder, n)
	var wg sync.WaitGroup
	wg.Go(func() { got[0] = c.MustResolve[*holder]() })
	<-entered
	for i := 1; i < n; i++ {
		wg.Go(func() { got[i] = c.MustResolve[*holder]() })
	}
	wg.Wait()
	if builds.Load() != 1 {
		t.Fatalf("constructor ran %d times, want 1", builds.Load())
	}
	for i, h := range got {
		if h != got[0] {
			t.Fatalf("goroutine %d got another instance", i)
		}
	}
}

// Docs: core/services.mdx#resolving
// "Registration also has imperative forms, for wiring done after the App or Worker
// exists: `RegisterSingletonService`, `RegisterScopedService`,
// `RegisterTransientService`, `RegisterService`, `RegisterServiceInstance`, and
// `RegisterServiceAlias`, on the `ServiceContainer` and forwarded on the
// `Worker`. They share the
// container's registrations with the options and return the same instances."
func TestImperativeRegistrationShared(t *testing.T) {
	t.Parallel()
	var injected *svc[kA]
	w := gas.NewWorker(gas.WithSingletonService[*svc[kB]](func(a *svc[kA], _ *clock) *svc[kB] { injected = a; return &svc[kB]{} }))
	c := w.ServiceContainer()
	c.RegisterSingletonService[*svc[kA]](func() *svc[kA] { return &svc[kA]{} })
	w.RegisterServiceInstance(&clock{})
	w.RegisterSingletonService[*holder](func() *holder { return &holder{} })
	w.RegisterScopedService[*requestLog](func() *requestLog { return &requestLog{} })
	w.RegisterTransientService[*nonce](func() *nonce { return &nonce{} })
	w.RegisterService[*dep](func() *dep { return &dep{} }, gas.ServiceLifetimeSingleton)
	c.RegisterScopedService[*scopedUser](func() *scopedUser { return &scopedUser{} })
	c.RegisterTransientService[*holder2](func() *holder2 { return &holder2{} })
	c.RegisterService[*cache](func() *cache { return &cache{} }, gas.ServiceLifetimeSingleton)
	c.RegisterServiceInstance(&smtpMailer{})
	w.RegisterServiceAlias[*holder, fmt.Stringer]()
	c.RegisterServiceAlias[*svc[kA], named]()
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
	if injected == nil || c.MustResolve[*svc[kA]]() != injected {
		t.Fatal("the option-registered service and the container returned different instances")
	}
	scope := c.NewScope(t.Context())
	for name, err := range map[string]error{
		"Worker.RegisterSingletonService":           errOf(c.Resolve[*holder]()),
		"Worker.RegisterScopedService":              errOf(scope.Resolve[*requestLog]()),
		"Worker.RegisterTransientService":           errOf(c.Resolve[*nonce]()),
		"Worker.RegisterService":                    errOf(c.Resolve[*dep]()),
		"ServiceContainer.RegisterScopedService":    errOf(scope.Resolve[*scopedUser]()),
		"ServiceContainer.RegisterTransientService": errOf(c.Resolve[*holder2]()),
		"ServiceContainer.RegisterService":          errOf(c.Resolve[*cache]()),
		"ServiceContainer.RegisterServiceInstance":  errOf(c.Resolve[*smtpMailer]()),
		"Worker.RegisterServiceAlias":               errOf(c.Resolve[fmt.Stringer]()),
		"ServiceContainer.RegisterServiceAlias":     errOf(c.Resolve[named]()),
	} {
		if err != nil {
			t.Errorf("%s registration not visible on the container: %v", name, err)
		}
	}
	if h1, h2 := c.MustResolve[*holder](), c.MustResolve[*holder](); h1 != h2 {
		t.Error("a Worker-registered singleton resolved to two instances")
	}
}

func errOf[T any](_ T, err error) error { return err }

type (
	cycA struct{}
	cycB struct{}
	reA  struct{}
	reB  struct{}
	reC  struct{}
	gA   struct{}
	gB   struct{}
)

// Docs: core/services.mdx#resolution-errors
// "Errors from the container wrap one of these sentinels, so classify them with
// `errors.Is`."
// Docs: core/services.mdx#resolution-errors
// "| `gas.ErrNotRegistered` | A type, or a dependency of one, has no registration or instance; or an alias's target has none |"
// Docs: core/services.mdx#resolution-errors
// "| `gas.ErrCircularDependency` | Constructors depend on each other in a cycle; a constructor or `Init` resolves the type being built through a container or scope it captured; two goroutines' singleton builds would wait on each other"
// Docs: core/services.mdx#resolution-errors
// "| `gas.ErrCaptiveDependency` | A singleton's constructor depends on a scoped or transient service |"
// Docs: core/services.mdx#resolution-errors
// "| `gas.ErrScopeRequired` | A scoped service is resolved from the container instead of a `Scope` |"
// Docs: core/services.mdx#resolution-errors
// "| `gas.ErrInvalidLifetime` | A registration whose `ServiceLifetime` is not one of the three is resolved, directly or as a dependency |"
// Docs: core/services.mdx#resolution-errors
// "| `gas.ErrInvalidServiceShape` | A registered type declares `Init` or `Close` but does not implement `Service` |"
func TestResolutionSentinels(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		run  func(t *testing.T) error
		want error
	}{
		"ErrNotRegistered: type": {
			run: func(*testing.T) error {
				_, err := gas.NewServiceContainer().Resolve[*holder]()
				return err
			},
			want: gas.ErrNotRegistered,
		},
		"ErrNotRegistered: a dependency": {
			run: func(t *testing.T) error {
				c := gas.NewServiceContainer()
				c.RegisterSingletonService[*holder](func(*missing) *holder { return &holder{} })
				return c.BuildAll(t.Context())
			},
			want: gas.ErrNotRegistered,
		},
		"ErrNotRegistered: an alias's target": {
			run: func(t *testing.T) error {
				c := gas.NewServiceContainer()
				c.RegisterServiceAlias[*holder, fmt.Stringer]()
				return c.BuildAll(t.Context())
			},
			want: gas.ErrNotRegistered,
		},
		"ErrCircularDependency: constructor cycle": {
			run: func(t *testing.T) error {
				c := gas.NewServiceContainer()
				c.RegisterSingletonService[*cycA](func(*cycB) *cycA { return &cycA{} })
				c.RegisterSingletonService[*cycB](func(*cycA) *cycB { return &cycB{} })
				return c.BuildAll(t.Context())
			},
			want: gas.ErrCircularDependency,
		},
		"ErrCircularDependency: constructor resolves itself through a captured container": {
			run: func(*testing.T) error {
				c := gas.NewServiceContainer()
				var inner error
				c.RegisterSingletonService[*reA](func() *reA {
					_, inner = c.Resolve[*reA]()
					return &reA{}
				})
				_, err := c.Resolve[*reA]()
				return errors.Join(err, inner)
			},
			want: gas.ErrCircularDependency,
		},
		"ErrCircularDependency: Init resolves itself through a captured container": {
			run: func(*testing.T) error {
				c := gas.NewServiceContainer()
				var inner error
				c.RegisterSingletonService[*svc[reB]](func() *svc[reB] {
					return &svc[reB]{onInit: func(context.Context) error {
						_, inner = c.Resolve[*svc[reB]]()
						return inner
					}}
				})
				_, err := c.Resolve[*svc[reB]]()
				return errors.Join(err, inner)
			},
			want: gas.ErrCircularDependency,
		},
		"ErrCircularDependency: constructor resolves itself through a captured scope": {
			run: func(t *testing.T) error {
				c := gas.NewServiceContainer()
				var scope *gas.Scope
				var inner error
				c.RegisterScopedService[*reC](func() *reC {
					_, inner = scope.Resolve[*reC]()
					return &reC{}
				})
				scope = c.NewScope(t.Context())
				_, err := scope.Resolve[*reC]()
				return errors.Join(err, inner)
			},
			want: gas.ErrCircularDependency,
		},
		"ErrCircularDependency: two goroutines' builds wait on each other": {
			run: func(*testing.T) error {
				c := gas.NewServiceContainer()
				var both sync.WaitGroup
				both.Add(2)
				meet := func() {
					both.Done()
					done := make(chan struct{})
					go func() { both.Wait(); close(done) }()
					select {
					case <-done:
					case <-time.After(2 * time.Second):
					}
				}
				errs := make(chan error, 4)
				c.RegisterSingletonService[*gA](func() *gA {
					meet()
					_, err := c.Resolve[*gB]()
					errs <- err
					return &gA{}
				})
				c.RegisterSingletonService[*gB](func() *gB {
					meet()
					_, err := c.Resolve[*gA]()
					errs <- err
					return &gB{}
				})
				var wg sync.WaitGroup
				wg.Go(func() { _, err := c.Resolve[*gA](); errs <- err })
				wg.Go(func() { _, err := c.Resolve[*gB](); errs <- err })
				finished := make(chan struct{})
				go func() { wg.Wait(); close(finished) }()
				select {
				case <-finished:
				case <-time.After(5 * time.Second):
					return errors.New("builds deadlocked instead of reporting a cycle")
				}
				close(errs)
				var all []error
				for err := range errs {
					all = append(all, err)
				}
				return errors.Join(all...)
			},
			want: gas.ErrCircularDependency,
		},
		"ErrCaptiveDependency": {
			run: func(t *testing.T) error {
				c := gas.NewServiceContainer()
				c.RegisterScopedService[*requestLog](func() *requestLog { return &requestLog{} })
				c.RegisterSingletonService[*cache](func(*requestLog) *cache { return &cache{} })
				return c.BuildAll(t.Context())
			},
			want: gas.ErrCaptiveDependency,
		},
		"ErrScopeRequired": {
			run: func(*testing.T) error {
				c := gas.NewServiceContainer()
				c.RegisterScopedService[*requestLog](func() *requestLog { return &requestLog{} })
				_, err := c.Resolve[*requestLog]()
				return err
			},
			want: gas.ErrScopeRequired,
		},
		"ErrInvalidLifetime: resolved directly": {
			run: func(*testing.T) error {
				c := gas.NewServiceContainer()
				c.RegisterService[*holder](func() *holder { return &holder{} }, gas.ServiceLifetime(42))
				_, err := c.Resolve[*holder]()
				return err
			},
			want: gas.ErrInvalidLifetime,
		},
		"ErrInvalidLifetime: as a dependency": {
			run: func(t *testing.T) error {
				c := gas.NewServiceContainer()
				c.RegisterService[*holder](func() *holder { return &holder{} }, gas.ServiceLifetime(42))
				c.RegisterSingletonService[*holder2](func(*holder) *holder2 { return &holder2{} })
				return c.BuildAll(t.Context())
			},
			want: gas.ErrInvalidLifetime,
		},
		"ErrInvalidServiceShape": {
			run: func(t *testing.T) error {
				c := gas.NewServiceContainer()
				c.RegisterSingletonService[*invalidShape](func() *invalidShape { return &invalidShape{} })
				return c.BuildAll(t.Context())
			},
			want: gas.ErrInvalidServiceShape,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var err error
			if r := recovered(func() { err = tc.run(t) }); r != nil {
				t.Fatalf("panicked instead of returning %v: %v", tc.want, r)
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want one wrapping %v", err, tc.want)
			}
		})
	}
}

// Docs: core/services.mdx#resolution-errors
// "A registration whose `ServiceLifetime` is not one of the three is resolved"
// Only resolving it fails: a startup that never resolves it succeeds.
func TestInvalidLifetimeOnlyWhenResolved(t *testing.T) {
	t.Parallel()
	c := gas.NewServiceContainer()
	c.RegisterService[*holder](func() *holder { return &holder{} }, gas.ServiceLifetime(42))
	if err := c.BuildAll(t.Context()); err != nil {
		t.Fatalf("BuildAll = %v, want nil when nothing resolves the invalid registration", err)
	}
}

// Docs: core/services.mdx#resolution-errors
// "The message itself names the types involved."
func TestResolutionErrorNamesTypes(t *testing.T) {
	t.Parallel()
	c := gas.NewServiceContainer()
	c.RegisterSingletonService[*holder](func(*missing) *holder { return &holder{} })
	err := c.BuildAll(t.Context())
	for _, typ := range []string{reflect.TypeFor[*holder]().String(), reflect.TypeFor[*missing]().String()} {
		if err == nil || !strings.Contains(err.Error(), typ) {
			t.Errorf("error %v does not name %s", err, typ)
		}
	}
}

// Docs: core/services.mdx#resolution-errors
// "At startup these come back from `Start` (or `Run`)."
func TestStartupErrorsFromStartAndRun(t *testing.T) {
	t.Parallel()
	build := func(port int) *gas.App {
		return gas.NewApp(
			gas.WithServiceInstance(serverConfig(t, port)),
			gas.WithSingletonService[*holder](func(*missing) *holder { return &holder{} }),
		)
	}
	if err := build(freePort(t)).Start(t.Context()); !errors.Is(err, gas.ErrNotRegistered) {
		t.Fatalf("Start = %v, want ErrNotRegistered", err)
	}
	port := freePort(t)
	done := make(chan error, 1)
	go func() { done <- build(port).Run() }()
	select {
	case err := <-done:
		if !errors.Is(err, gas.ErrNotRegistered) {
			t.Fatalf("Run = %v, want ErrNotRegistered", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return the startup error")
	}
	if accepting(port) {
		t.Fatal("the port accepts connections after a failed Run")
	}
}

// Docs: core/services.mdx#resolution-errors
// "In an App, startup also
// fails, before any request is served, when a
// [DI-aware handler](/gas/core/handlers/) declares a parameter type that has no
// registration; that error names the route, the owning service, and the type."
func TestHandlerParameterUnregisteredFailsStart(t *testing.T) {
	t.Parallel()
	app := newNotesApp()
	n := &svc[kA]{name: "billing"}
	app.Router().Get(n, "/invoices/{id}", func(gas.Context, *missing) error { return nil })
	err := app.Start(t.Context())
	if err == nil {
		_ = app.Stop(context.Background())
		t.Fatal("Start succeeded with an unregistered handler parameter")
	}
	for _, want := range []string{"/invoices/{id}", "billing", reflect.TypeFor[*missing]().String()} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err, want)
		}
	}

	// Run with the same mistake returns the error without serving.
	port := freePort(t)
	bad := gas.NewApp(gas.WithServiceInstance(serverConfig(t, port)))
	bad.Router().Get(nil, "/x", func(gas.Context, *missing) error { return nil })
	done := make(chan error, 1)
	go func() { done <- bad.Run() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Run returned nil")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return the startup error")
	}
	if accepting(port) {
		t.Fatal("the port accepts connections after a failed Run")
	}
}

// Docs: core/services.mdx#resolution-errors
// "A missing registration, a partial service, a captive dependency, or an
// unresolvable handler parameter fails `Start`. A transient service or a
// malformed constructor panics as soon as it is registered."
func TestAsideStartFailuresAndRegistrationPanics(t *testing.T) {
	t.Parallel()
	failing := map[string]func() *gas.App{
		"missing registration": func() *gas.App {
			return gas.NewApp(gas.WithSingletonService[*holder](func(*missing) *holder { return &holder{} }))
		},
		"partial service": func() *gas.App {
			return gas.NewApp(gas.WithSingletonService[*initOnly](func() *initOnly { return &initOnly{} }))
		},
		"captive dependency": func() *gas.App {
			return gas.NewApp(
				gas.WithScopedService[*requestLog](func() *requestLog { return &requestLog{} }),
				gas.WithSingletonService[*cache](func(*requestLog) *cache { return &cache{} }),
			)
		},
		"unresolvable handler parameter": func() *gas.App {
			app := gas.NewApp()
			app.Router().Get(nil, "/x", func(gas.Context, *missing) error { return nil })
			return app
		},
	}
	for name, build := range failing {
		var app *gas.App
		if r := recovered(func() { app = build() }); r != nil {
			t.Errorf("%s: panicked while wiring (%v); want Start to fail", name, r)
			continue
		}
		if err := app.Start(t.Context()); err == nil {
			_ = app.Stop(context.Background())
			t.Errorf("%s: Start succeeded", name)
		}
	}
	mustPanic(t, "transient service", func() {
		gas.NewApp(gas.WithTransientService[*svc[kA]](func() *svc[kA] { return &svc[kA]{} }))
	})
	mustPanic(t, "malformed constructor", func() { gas.NewApp(gas.WithSingletonService[*holder]("not a func")) })
}
