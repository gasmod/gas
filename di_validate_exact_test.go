package gas_test

import (
	"context"
	"strings"
	"testing"

	"github.com/gasmod/gas"
)

// A handler depending on an interface that only a registered concrete type
// implements cannot be served, because resolution is by exact type, so boot
// validation rejects it instead of letting every request fail.
func TestDIHandler_BootValidation_InterfaceNotRegistered(t *testing.T) {
	app := gas.NewApp(
		gas.WithService[*helloGreeter](newHelloGreeter, gas.ServiceLifetimeSingleton),
	)

	app.Router().Handle(nil, "GET", "/greet", func(ctx gas.Context, g Greeter) error {
		return nil
	})

	err := app.InitServices(context.Background())
	if err == nil {
		t.Fatal("InitServices() = nil, want an error for the unregistered Greeter")
	}
	if !strings.Contains(err.Error(), "Greeter") {
		t.Errorf("InitServices() error = %v, want it to name Greeter", err)
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

// A DI-aware NotFound handler's dependencies are validated at boot like a
// route handler's.
func TestDIHandler_BootValidation_NotFound(t *testing.T) {
	app := gas.NewApp()

	app.Router().NotFound(nil, func(ctx gas.Context, dep *testDep) error {
		return nil
	})

	err := app.InitServices(context.Background())
	if err == nil {
		t.Fatal("InitServices() = nil, want an error for the NotFound handler's missing dependency")
	}
	if !strings.Contains(err.Error(), "testDep") {
		t.Errorf("InitServices() error = %v, want it to name testDep", err)
	}
}

// The same holds for a NotFound handler registered on a sub-router.
func TestDIHandler_BootValidation_NotFoundInRoute(t *testing.T) {
	app := gas.NewApp()

	app.Router().Route("/api", func(sub *gas.Router) {
		sub.NotFound(nil, func(ctx gas.Context, dep *testDep) error {
			return nil
		})
	})

	err := app.InitServices(context.Background())
	if err == nil {
		t.Fatal("InitServices() = nil, want an error for the NotFound handler's missing dependency")
	}
	if !strings.Contains(err.Error(), "testDep") {
		t.Errorf("InitServices() error = %v, want it to name testDep", err)
	}
}
