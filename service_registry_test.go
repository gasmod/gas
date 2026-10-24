package gas_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gasmod/gas"
)

type lateDep struct{}

// Registering a service while other goroutines resolve from the container is
// safe: the registrations map is guarded like the instance maps. Run with
// -race; before the guard it reported a concurrent map write and read.
func TestServiceContainer_RegisterDuringResolve(t *testing.T) {
	c := gas.NewServiceContainer()
	c.RegisterSingletonService[*testDep](newTestDep)

	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 200 {
				if _, err := c.Resolve[*testDep](); err != nil {
					t.Errorf("Resolve[*testDep]() = %v", err)
					return
				}
				_ = c.CanResolve[*lateDep]()
			}
		})
	}
	wg.Go(func() {
		for range 200 {
			c.RegisterTransientService[*lateDep](func() *lateDep { return &lateDep{} })
		}
	})
	wg.Wait()

	if !c.CanResolve[*lateDep]() {
		t.Error("CanResolve[*lateDep]() = false after registering it")
	}
}

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

// CanResolve answers the same question Resolve does: an interface is not
// resolvable just because a registered type implements it.
func TestCanResolve_MatchesResolve(t *testing.T) {
	c := gas.NewServiceContainer()
	c.RegisterSingletonService[*helloGreeter](newHelloGreeter)

	_, err := c.Resolve[Greeter]()
	if got := c.CanResolve[Greeter](); got != (err == nil) {
		t.Errorf("CanResolve[Greeter]() = %v, but Resolve[Greeter]() error = %v", got, err)
	}
}

func TestServiceContainer_CircularDependency(t *testing.T) {
	c := gas.NewServiceContainer()

	type A struct{}
	type B struct{}

	c.RegisterService[*A](func(b *B) *A { return &A{} }, gas.ServiceLifetimeSingleton)
	c.RegisterService[*B](func(a *A) *B { return &B{} }, gas.ServiceLifetimeSingleton)

	err := c.BuildAll(context.Background())
	if err == nil {
		t.Fatal("expected error for circular dependency")
	}
	if !errors.Is(err, gas.ErrCircularDependency) {
		t.Fatalf("expected ErrCircularDependency, got: %v", err)
	}
}

func TestServiceContainer_CaptiveDependency(t *testing.T) {
	t.Run("SingletonDependsOnScoped", func(t *testing.T) {
		c := gas.NewServiceContainer()
		type SvcScoped struct{}
		type SvcSingleton struct{}

		c.RegisterService[*SvcScoped](
			func() *SvcScoped { return &SvcScoped{} },
			gas.ServiceLifetimeScoped,
		)
		c.RegisterService[*SvcSingleton](
			func(s *SvcScoped) *SvcSingleton { return &SvcSingleton{} },
			gas.ServiceLifetimeSingleton,
		)

		err := c.BuildAll(context.Background())
		if err == nil {
			t.Fatal("expected error for captive dependency (singleton -> scoped)")
		}
		if !errors.Is(err, gas.ErrCaptiveDependency) {
			t.Fatalf("expected ErrCaptiveDependency, got: %v", err)
		}
	})

	t.Run("SingletonDependsOnTransient", func(t *testing.T) {
		c := gas.NewServiceContainer()
		type SvcTransient struct{}
		type SvcSingleton struct{}

		c.RegisterService[*SvcTransient](
			func() *SvcTransient { return &SvcTransient{} },
			gas.ServiceLifetimeTransient,
		)
		c.RegisterService[*SvcSingleton](
			func(s *SvcTransient) *SvcSingleton { return &SvcSingleton{} },
			gas.ServiceLifetimeSingleton,
		)

		err := c.BuildAll(context.Background())
		if err == nil {
			t.Fatal("expected error for captive dependency (singleton -> transient)")
		}
		if !errors.Is(err, gas.ErrCaptiveDependency) {
			t.Fatalf("expected ErrCaptiveDependency, got: %v", err)
		}
	})
}

// closeOnlyA and closeOnlyB declare Close without the rest of Service, so a
// registered instance of either fails shape validation.
type (
	closeOnlyA struct{}
	closeOnlyB struct{}
)

func (*closeOnlyA) Close(context.Context) error { return nil }
func (*closeOnlyB) Close(context.Context) error { return nil }

// With two independent broken registrations, BuildAll reports the same one
// on every run: the one registered first. Each case registers the B types
// before the A types, so registration order differs from alphabetical order.
func TestStartupErrorIsDeterministic(t *testing.T) {
	type (
		Root     struct{}
		MissingA struct{}
		MissingB struct{}
		NeedsA   struct{}
		NeedsB   struct{}
		ScopedA  struct{}
		ScopedB  struct{}
		CaptiveA struct{}
		CaptiveB struct{}
	)
	const runs = 100

	cases := []struct {
		register func(c *gas.ServiceContainer)
		name     string
		want     string
	}{
		{
			register: func(c *gas.ServiceContainer) {
				c.RegisterSingletonService[*NeedsB](func(*MissingB) *NeedsB { return &NeedsB{} })
				c.RegisterSingletonService[*NeedsA](func(*MissingA) *NeedsA { return &NeedsA{} })
			},
			name: "UnregisteredDependencies",
			want: "building *gas_test.NeedsB: ",
		},
		{
			register: func(c *gas.ServiceContainer) {
				c.RegisterSingletonService[*Root](func() *Root { return &Root{} })
				c.RegisterSingletonService[*NeedsB](
					func(*Root, *MissingB) *NeedsB { return &NeedsB{} },
				)
				c.RegisterSingletonService[*NeedsA](
					func(*Root, *MissingA) *NeedsA { return &NeedsA{} },
				)
			},
			name: "UnregisteredDependenciesBelowRoot",
			want: "building *gas_test.NeedsB: ",
		},
		{
			register: func(c *gas.ServiceContainer) {
				c.RegisterScopedService[*ScopedB](func() *ScopedB { return &ScopedB{} })
				c.RegisterScopedService[*ScopedA](func() *ScopedA { return &ScopedA{} })
				c.RegisterSingletonService[*CaptiveB](
					func(*ScopedB) *CaptiveB { return &CaptiveB{} },
				)
				c.RegisterSingletonService[*CaptiveA](
					func(*ScopedA) *CaptiveA { return &CaptiveA{} },
				)
			},
			name: "CaptiveDependencies",
			want: "singleton *gas_test.CaptiveB depends on",
		},
		{
			register: func(c *gas.ServiceContainer) {
				c.RegisterServiceInstance(&closeOnlyB{})
				c.RegisterServiceInstance(&closeOnlyA{})
			},
			name: "InstanceShapes",
			want: "gas: *gas_test.closeOnlyB declares Close",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seen := map[string]int{}
			for range runs {
				c := gas.NewServiceContainer()
				tc.register(c)
				err := c.BuildAll(context.Background())
				if err == nil {
					t.Fatal("BuildAll succeeded with two broken registrations")
				}
				seen[err.Error()]++
			}
			if len(seen) != 1 {
				var b strings.Builder
				for msg, n := range seen {
					_, _ = fmt.Fprintf(&b, "\n  %d/%d: %s", n, runs, msg)
				}
				t.Fatalf(
					"BuildAll returned %d distinct errors over %d runs:%s",
					len(seen),
					runs,
					b.String(),
				)
			}
			for msg := range seen {
				if !strings.Contains(msg, tc.want) {
					t.Fatalf("BuildAll() = %q, want the first registered (%q)", msg, tc.want)
				}
			}
		})
	}
}

// EachInstance calls fn without holding the container's lock, so fn may
// resolve from the container. Holding the lock across fn deadlocks here.
func TestEachInstance_FnMayResolve(t *testing.T) {
	c := gas.NewServiceContainer()
	c.RegisterServiceInstance(&testDep{})

	done := make(chan struct{})
	go func() {
		defer close(done)
		c.EachInstance(func(reflect.Value) {
			if !c.CanResolve[*testDep]() {
				t.Error("CanResolve[*testDep]() = false inside EachInstance")
			}
			if _, err := c.Resolve[*testDep](); err != nil {
				t.Errorf("Resolve[*testDep]() inside EachInstance = %v", err)
			}
		})
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("EachInstance did not return: fn resolving from the container deadlocked")
	}
}

func TestServiceContainer_ConstructorError(t *testing.T) {
	c := gas.NewServiceContainer()
	type Svc struct{}
	c.RegisterService[*Svc](func() (*Svc, error) {
		return nil, errors.New("ctor failed")
	}, gas.ServiceLifetimeSingleton)

	err := c.BuildAll(context.Background())
	if err == nil || !strings.Contains(err.Error(), "ctor failed") {
		t.Fatalf("expected ctor failure, got: %v", err)
	}
}

func TestServiceContainer_InitError(t *testing.T) {
	c := gas.NewServiceContainer()
	svc := &testService{name: "failing", initErr: errors.New("init failed")}
	c.RegisterServiceInstance[*testService](svc)

	// BuildAll doesn't call Init on RegisterInstance.
	// We need to use RegisterCtor or a scenario where Init is called.
	c = gas.NewServiceContainer()
	c.RegisterService[*testService](func() *testService {
		return svc
	}, gas.ServiceLifetimeSingleton)

	err := c.BuildAll(context.Background())
	if err == nil || !strings.Contains(err.Error(), "init failed") {
		t.Fatalf("expected init failure, got: %v", err)
	}
}

// Scope.Close had the identical defect on the per-request path: it ranged over
// the scope's resolved map, so scoped services were torn down in a random
// order and one could be closed before a scoped dependency it uses in Close.
func TestScopeClosesInReverseResolutionOrder(t *testing.T) {
	const repeats = 20

	wantClose := "S2 S1 S0"

	for run := range repeats {
		log := &ordLog{}
		c := gas.NewServiceContainer()

		c.RegisterService[*ordService[ordTag0]](func() *ordService[ordTag0] {
			return &ordService[ordTag0]{log: log, name: "S0"}
		}, gas.ServiceLifetimeScoped)
		c.RegisterService[*ordService[ordTag1]](func(*ordService[ordTag0]) *ordService[ordTag1] {
			return &ordService[ordTag1]{log: log, name: "S1"}
		}, gas.ServiceLifetimeScoped)
		c.RegisterService[*ordService[ordTag2]](func(*ordService[ordTag1]) *ordService[ordTag2] {
			return &ordService[ordTag2]{log: log, name: "S2"}
		}, gas.ServiceLifetimeScoped)

		scope := c.NewScope(context.Background())
		if _, err := scope.Resolve[*ordService[ordTag2]](); err != nil {
			t.Fatalf("run %d: resolve: %v", run, err)
		}
		if err := scope.Close(context.Background()); err != nil {
			t.Fatalf("run %d: scope close: %v", run, err)
		}

		inits, closes := log.snapshot()
		if got := strings.Join(inits, " "); got != "S0 S1 S2" {
			t.Fatalf("run %d: init order = [%s], want [S0 S1 S2]", run, got)
		}
		if got := strings.Join(closes, " "); got != wantClose {
			t.Fatalf("run %d: scope close order = [%s], want [%s]", run, got, wantClose)
		}
	}
}

// namer is the alias interface most alias tests resolve through.
type namer interface{ Name() string }

// countedService counts its Init and Close calls.
type countedService struct {
	inits, closes atomic.Int32
}

func (s *countedService) Name() string                { return "counted" }
func (s *countedService) Init(context.Context) error  { s.inits.Add(1); return nil }
func (s *countedService) Close(context.Context) error { s.closes.Add(1); return nil }

// labeled is a plain transient value, not a Service.
type labeled struct{ label string }

func (l *labeled) Name() string { return l.label }

// namedSvc is an interface target: an interface that implements namer.
type namedSvc interface {
	namer
	Kind() string
}

type kindService struct{}

func (kindService) Name() string { return "kind" }
func (kindService) Kind() string { return "service" }

// needsNamer is a singleton that depends on the namer alias.
type needsNamer struct{ n namer }

func newNeedsNamer(n namer) *needsNamer { return &needsNamer{n: n} }

// A singleton resolved through its alias is the same instance as through its
// target, and the Worker initializes and closes it once.
func TestServiceAlias_SingletonSharesInstance(t *testing.T) {
	t.Parallel()

	svc := &countedService{}
	w := gas.NewWorker(
		gas.WithSingletonService[*countedService](func() *countedService { return svc }),
		gas.WithServiceAlias[*countedService, namer](),
	)
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	c := w.ServiceContainer()
	if got := c.MustResolve[namer](); got != namer(svc) {
		t.Errorf("Resolve[namer]() = %p, want the *countedService %p", got, svc)
	}
	if got := c.MustResolve[*countedService](); got != svc {
		t.Errorf("Resolve[*countedService]() = %p, want %p", got, svc)
	}
	if err := w.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if i, cl := svc.inits.Load(), svc.closes.Load(); i != 1 || cl != 1 {
		t.Errorf("Init ran %d times and Close %d times, want 1 each", i, cl)
	}
}

// A scoped target resolved through its alias is the scope's one instance,
// and Scope.Close closes it once.
func TestServiceAlias_ScopedSharesInstance(t *testing.T) {
	t.Parallel()

	c := gas.NewServiceContainer()
	c.RegisterScopedService[*countedService](func() *countedService { return &countedService{} })
	c.RegisterServiceAlias[*countedService, namer]()
	if err := c.BuildAll(t.Context()); err != nil {
		t.Fatalf("BuildAll: %v", err)
	}

	scope := c.NewScope(t.Context())
	viaAlias := scope.MustResolve[namer]()
	viaTarget := scope.MustResolve[*countedService]()
	if viaAlias != namer(viaTarget) {
		t.Fatalf("Resolve[namer]() = %p, want the scope's *countedService %p", viaAlias, viaTarget)
	}
	if err := scope.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if i, cl := viaTarget.inits.Load(), viaTarget.closes.Load(); i != 1 || cl != 1 {
		t.Errorf("Init ran %d times and Close %d times, want 1 each", i, cl)
	}
	if _, err := c.Resolve[namer](); !errors.Is(err, gas.ErrScopeRequired) {
		t.Errorf("container Resolve[namer]() = %v, want ErrScopeRequired for a scoped target", err)
	}
}

// A transient target resolved through its alias is built fresh each time.
func TestServiceAlias_Transient(t *testing.T) {
	t.Parallel()

	c := gas.NewServiceContainer()
	c.RegisterTransientService[*labeled](func() *labeled { return &labeled{label: "fresh"} })
	c.RegisterServiceAlias[*labeled, namer]()

	a, b := c.MustResolve[namer](), c.MustResolve[namer]()
	if a == b {
		t.Error("two resolutions of a transient alias returned the same instance")
	}
	if a.Name() != "fresh" {
		t.Errorf("Name() = %q, want %q", a.Name(), "fresh")
	}
}

// The target may be an interface, as long as it implements the alias.
func TestServiceAlias_InterfaceTarget(t *testing.T) {
	t.Parallel()

	c := gas.NewServiceContainer()
	c.RegisterSingletonService[namedSvc](func() namedSvc { return kindService{} })
	c.RegisterServiceAlias[namedSvc, namer]()
	if err := c.BuildAll(t.Context()); err != nil {
		t.Fatalf("BuildAll: %v", err)
	}
	if got := c.MustResolve[namer]().Name(); got != "kind" {
		t.Errorf("Resolve[namer]().Name() = %q, want %q", got, "kind")
	}
}

// A singleton that depends on an alias is checked against the target's
// lifetime, and is built after the target.
func TestServiceAlias_CaptiveDependency(t *testing.T) {
	t.Parallel()

	for _, lifetime := range []gas.ServiceLifetime{gas.ServiceLifetimeScoped, gas.ServiceLifetimeTransient} {
		t.Run(lifetime.String(), func(t *testing.T) {
			t.Parallel()
			c := gas.NewServiceContainer()
			c.RegisterService[*labeled](func() *labeled { return &labeled{} }, lifetime)
			c.RegisterServiceAlias[*labeled, namer]()
			c.RegisterSingletonService[*needsNamer](newNeedsNamer)
			err := c.BuildAll(t.Context())
			if !errors.Is(err, gas.ErrCaptiveDependency) {
				t.Fatalf("BuildAll = %v, want ErrCaptiveDependency", err)
			}
			if !strings.Contains(err.Error(), "*gas_test.labeled") {
				t.Errorf("error %q does not name the target", err)
			}
		})
	}

	t.Run("singleton", func(t *testing.T) {
		t.Parallel()
		c := gas.NewServiceContainer()
		// The dependent is registered first, so it builds only if the
		// sort sees the edge through the alias.
		c.RegisterSingletonService[*needsNamer](newNeedsNamer)
		c.RegisterServiceAlias[*labeled, namer]()
		c.RegisterSingletonService[*labeled](func() *labeled { return &labeled{label: "dep"} })
		if err := c.BuildAll(t.Context()); err != nil {
			t.Fatalf("BuildAll: %v", err)
		}
		if got := c.MustResolve[*needsNamer]().n; got != namer(c.MustResolve[*labeled]()) {
			t.Errorf("needsNamer got %v, want the *labeled singleton", got)
		}
	})
}

// An alias may be registered before its target. A target that is never
// registered fails BuildAll and Resolve with ErrNotRegistered, and
// CanResolve reports false until it is there.
func TestServiceAlias_MissingTarget(t *testing.T) {
	t.Parallel()

	c := gas.NewServiceContainer()
	c.RegisterServiceAlias[*labeled, namer]()
	if c.CanResolve[namer]() {
		t.Error("CanResolve[namer]() = true with no target registered")
	}
	err := c.BuildAll(t.Context())
	if !errors.Is(err, gas.ErrNotRegistered) {
		t.Fatalf("BuildAll = %v, want ErrNotRegistered", err)
	}
	if !strings.Contains(err.Error(), "*gas_test.labeled") ||
		!strings.Contains(err.Error(), "gas_test.namer") {
		t.Errorf("error %q does not name the alias and its target", err)
	}
	if _, err := c.Resolve[namer](); !errors.Is(err, gas.ErrNotRegistered) {
		t.Errorf("Resolve[namer]() = %v, want ErrNotRegistered", err)
	}
	if _, err := c.NewScope(t.Context()).Resolve[namer](); !errors.Is(err, gas.ErrNotRegistered) {
		t.Errorf("Scope.Resolve[namer]() = %v, want ErrNotRegistered", err)
	}

	c.RegisterSingletonService[*labeled](func() *labeled { return &labeled{label: "late"} })
	if !c.CanResolve[namer]() {
		t.Error("CanResolve[namer]() = false once the target is registered")
	}
	if err := c.BuildAll(t.Context()); err != nil {
		t.Fatalf("BuildAll after registering the target: %v", err)
	}
	if got := c.MustResolve[namer]().Name(); got != "late" {
		t.Errorf("Resolve[namer]().Name() = %q, want %q", got, "late")
	}
}

// An instance target resolves through its alias too.
func TestServiceAlias_InstanceTarget(t *testing.T) {
	t.Parallel()

	inst := &labeled{label: "instance"}
	c := gas.NewServiceContainer()
	c.RegisterServiceInstance(inst)
	c.RegisterServiceAlias[*labeled, namer]()
	if !c.CanResolve[namer]() {
		t.Error("CanResolve[namer]() = false for an instance target")
	}
	if got := c.MustResolve[namer](); got != namer(inst) {
		t.Errorf("Resolve[namer]() = %p, want the instance %p", got, inst)
	}
}

type otherNamer interface{ Name() string }

type notANamer struct{}

func TestServiceAlias_Panics(t *testing.T) {
	t.Parallel()

	assertPanics(t, "a non-interface alias", func() {
		gas.NewServiceContainer().RegisterServiceAlias[*labeled, *labeled]()
	})
	assertPanics(t, "a target that does not implement the alias", func() {
		gas.NewServiceContainer().RegisterServiceAlias[*notANamer, namer]()
	})
	assertPanics(t, "an interface aliased to itself", func() {
		gas.NewServiceContainer().RegisterServiceAlias[namer, namer]()
	})
	assertPanics(t, "an alias already registered as a service", func() {
		c := gas.NewServiceContainer()
		c.RegisterSingletonService[namer](func() namer { return &labeled{} })
		c.RegisterServiceAlias[*labeled, namer]()
	})
	assertPanics(t, "an alias already registered as an instance", func() {
		c := gas.NewServiceContainer()
		c.RegisterServiceInstance[namer](&labeled{})
		c.RegisterServiceAlias[*labeled, namer]()
	})
	assertPanics(t, "a service registered under an alias", func() {
		c := gas.NewServiceContainer()
		c.RegisterServiceAlias[*labeled, namer]()
		c.RegisterSingletonService[namer](func() namer { return &labeled{} })
	})
	assertPanics(t, "an instance registered under an alias", func() {
		c := gas.NewServiceContainer()
		c.RegisterServiceAlias[*labeled, namer]()
		c.RegisterServiceInstance[namer](&labeled{})
	})
	assertPanics(t, "an alias whose target is an alias", func() {
		c := gas.NewServiceContainer()
		c.RegisterServiceAlias[*labeled, namer]()
		c.RegisterServiceAlias[namer, otherNamer]()
	})
	assertPanics(t, "an alias that is another alias's target", func() {
		c := gas.NewServiceContainer()
		c.RegisterServiceAlias[namer, otherNamer]()
		c.RegisterServiceAlias[*labeled, namer]()
	})
}

// A panic message names the types with %v, so a pointer type is not printed
// as an empty name.
func TestServiceAlias_PanicNamesTypes(t *testing.T) {
	t.Parallel()

	defer func() {
		msg := fmt.Sprint(recover())
		if !strings.Contains(msg, "*gas_test.notANamer") ||
			!strings.Contains(msg, "gas_test.namer") {
			t.Errorf("panic %q does not name both types", msg)
		}
	}()
	gas.NewServiceContainer().RegisterServiceAlias[*notANamer, namer]()
}

// Worker.RegisterServiceAlias is the imperative form of WithServiceAlias.
func TestServiceAlias_WorkerRegister(t *testing.T) {
	t.Parallel()

	w := gas.NewWorker()
	w.RegisterServiceAlias[*labeled, namer]()
	w.RegisterSingletonService[*labeled](func() *labeled { return &labeled{label: "worker"} })
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
	if got := w.ServiceContainer().MustResolve[namer]().Name(); got != "worker" {
		t.Errorf("Resolve[namer]().Name() = %q, want %q", got, "worker")
	}
}

// Registering an alias while other goroutines resolve through it is safe:
// the alias map is guarded like the registrations. Run with -race.
func TestServiceAlias_RegisterDuringResolve(t *testing.T) {
	t.Parallel()

	c := gas.NewServiceContainer()
	c.RegisterSingletonService[*labeled](func() *labeled { return &labeled{} })
	c.RegisterServiceAlias[*labeled, namer]()

	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 200 {
				if _, err := c.Resolve[namer](); err != nil {
					t.Errorf("Resolve[namer]() = %v", err)
					return
				}
				_ = c.CanResolve[otherNamer]()
				_, _ = c.NewScope(context.Background()).Resolve[otherNamer]()
			}
		})
	}
	wg.Go(func() {
		for range 200 {
			c.RegisterServiceAlias[*labeled, otherNamer]()
		}
	})
	wg.Wait()

	if !c.CanResolve[otherNamer]() {
		t.Error("CanResolve[otherNamer]() = false after registering it")
	}
}
