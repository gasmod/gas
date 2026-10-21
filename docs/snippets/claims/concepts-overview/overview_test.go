package conceptsoverview_test

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"slices"
	"testing"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/migrate/migratetest"
)

// Docs: concepts/overview.mdx#services
// "type Service interface { Name() string ... Init(ctx context.Context) error ... Close(ctx context.Context) error }"
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

// Docs: concepts/overview.mdx#services
// "The container calls `Init` on every service it holds during startup, and `Close` in reverse initialization order at shutdown."
func TestInitEveryServiceCloseInReverse(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	w := gas.NewWorker(
		gas.WithServiceInstance[*svc[kA]](&svc[kA]{rec: rec}),
		gas.WithSingletonService[*svc[kB]](func() *svc[kB] { return &svc[kB]{rec: rec} }),
		gas.WithSingletonService[*svc[kC]](func(*svc[kB]) *svc[kC] { return &svc[kC]{rec: rec} }),
	)
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := w.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	inits := rec.only("init")
	if len(inits) != 3 {
		t.Fatalf("Init calls = %v, want one per service", inits)
	}
	var wantClose []string
	for _, e := range slices.Backward(inits) {
		wantClose = append(wantClose, "close"+e[len("init"):])
	}
	if got := rec.only("close"); !slices.Equal(got, wantClose) {
		t.Fatalf("Close order = %v, want reverse of Init order %v", got, wantClose)
	}
}

type owner struct {
	router *gas.Router
	bus    *gas.EventBus
	hits   *int
}

type ping struct{ gas.Event[struct{}] }

func (o *owner) Name() string { return "owner" }
func (o *owner) Init(context.Context) error {
	o.router.Handle(o, http.MethodGet, "/owned", func(w http.ResponseWriter, _ *http.Request) {})
	o.router.Register(o, "owned-mw", func(next http.Handler) http.Handler { return next })
	o.bus.SubscribeWithOwner[ping](o, func(struct{}) { *o.hits++ })
	return nil
}
func (o *owner) Close(context.Context) error { return nil }

// Docs: concepts/overview.mdx#ownership-tracking
// "Every route, middleware registration, and event subscription is tagged with the service that created it"
// Assumption: "middleware registration" means named middleware passed to
// Register; the ownership page says inline middleware has no owner.
func TestRegistrationsAreTaggedWithOwner(t *testing.T) {
	t.Parallel()
	hits := 0
	app := gas.NewApp(gas.WithSingletonService[*owner](func(r *gas.Router, b *gas.EventBus) *owner {
		return &owner{router: r, bus: b, hits: &hits}
	}))
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	if routes := app.Router().Routes()["owner"]; len(routes) != 1 || routes[0].Path != "/owned" {
		t.Fatalf("Routes()[owner] = %v, want /owned", routes)
	}
	if got := app.Router().NamedMiddleware()["owned-mw"]; got != "owner" {
		t.Fatalf("NamedMiddleware()[owned-mw] = %q, want owner", got)
	}
	if err := app.CloseService[*owner](t.Context()); err != nil {
		t.Fatalf("CloseService: %v", err)
	}
	app.EventBus().Emit[ping](struct{}{}).Wait()
	if hits != 0 {
		t.Fatal("subscription survived its owner's teardown, so it was not tagged")
	}
}

type halfService struct{}

func (halfService) Close(context.Context) error { return nil }

type scopedDep struct{}
type transientDep struct{}
type missing struct{}

// Docs: concepts/overview.mdx#failing-at-startup
// "Rejected at startup: - A dependency with no registration, by exact type. - A type that declares `Init` or `Close` without implementing all three methods. - A transient registration whose type implements `Service`. - A singleton depending on a scoped or transient service. - A DI-aware handler declaring a parameter the container cannot resolve. - An invalid `Config`, per each module's `Validate`."
// Assumption: a panic raised while the app is being wired, before Start
// returns, also counts as "rejected at startup".
func TestRejectedAtStartup(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		build    func(t *testing.T) *gas.App
		sentinel error
	}{
		"dependency with no registration": {
			build: func(*testing.T) *gas.App {
				return gas.NewApp(gas.WithSingletonService[*svc[kA]](func(*missing) *svc[kA] { return &svc[kA]{} }))
			},
			sentinel: gas.ErrNotRegistered,
		},
		"Init or Close without all three": {
			build: func(*testing.T) *gas.App {
				return gas.NewApp(gas.WithSingletonService[*halfService](func() *halfService { return &halfService{} }))
			},
			sentinel: gas.ErrInvalidServiceShape,
		},
		"transient implementing Service": {
			build: func(*testing.T) *gas.App {
				return gas.NewApp(gas.WithTransientService[*svc[kA]](func() *svc[kA] { return &svc[kA]{} }))
			},
		},
		"singleton depending on scoped": {
			build: func(*testing.T) *gas.App {
				return gas.NewApp(
					gas.WithScopedService[*scopedDep](func() *scopedDep { return &scopedDep{} }),
					gas.WithSingletonService[*svc[kA]](func(*scopedDep) *svc[kA] { return &svc[kA]{} }),
				)
			},
			sentinel: gas.ErrCaptiveDependency,
		},
		"singleton depending on transient": {
			build: func(*testing.T) *gas.App {
				return gas.NewApp(
					gas.WithTransientService[*transientDep](func() *transientDep { return &transientDep{} }),
					gas.WithSingletonService[*svc[kA]](func(*transientDep) *svc[kA] { return &svc[kA]{} }),
				)
			},
			sentinel: gas.ErrCaptiveDependency,
		},
		"handler parameter the container cannot resolve": {
			build: func(*testing.T) *gas.App {
				app := gas.NewApp()
				app.Router().Handle(nil, http.MethodGet, "/x", func(gas.Context, *missing) error { return nil })
				return app
			},
		},
		"invalid Config per Validate": {
			build: func(t *testing.T) *gas.App {
				return gas.NewApp(gas.WithServiceInstance[gas.ConfigProvider](mustConfig(t, map[string]any{
					"server": map[string]any{"host": "not a host name!"},
				})))
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := func() (err error) {
				defer func() {
					if r := recover(); r != nil {
						err = errors.New("panicked while wiring")
					}
				}()
				app := tc.build(t)
				if err := app.Start(t.Context()); err != nil {
					return err
				}
				_ = app.Stop(context.Background())
				return nil
			}()
			if err == nil {
				t.Fatal("the app started; want it rejected")
			}
			if tc.sentinel != nil && !errors.Is(err, tc.sentinel) {
				t.Fatalf("error %q does not wrap %v", err, tc.sentinel)
			}
		})
	}
}

// Docs: concepts/overview.mdx#two-entry-points
// "`Worker` provides the same container, lifecycle, events, and migrations without a router or server"
func TestWorkerHasContainerEventsMigrationsButNoRouter(t *testing.T) {
	t.Parallel()
	if _, ok := reflect.TypeFor[*gas.Worker]().MethodByName("Router"); ok {
		t.Fatal("Worker has a Router method")
	}
	if _, ok := reflect.TypeFor[*gas.Worker]().MethodByName("Serve"); ok {
		t.Fatal("Worker has a Serve method")
	}
	mm := &migratetest.MockMigrationManager{}
	a := &svc[kA]{}
	w := gas.NewWorker(
		gas.WithServiceInstance[gas.MigrationManager](mm),
		gas.WithSingletonService[*svc[kA]](func() *svc[kA] { return a }),
	)
	if w.ServiceContainer() == nil || w.EventBus() == nil {
		t.Fatal("Worker has no container or event bus")
	}
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
	if a.inits() != 1 || mm.CallCount("RunPending") != 1 {
		t.Fatalf("Init=%d RunPending=%d, want 1 and 1", a.inits(), mm.CallCount("RunPending"))
	}
}
