package gas_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

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
