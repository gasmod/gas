package gas_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gasmod/gas"
)

// Each DI failure is reachable through the public API and classifiable with
// errors.Is, and keeps the message that names the types involved.

type (
	diA      struct{}
	diB      struct{}
	diMiss   struct{}
	diScoped struct{}
)

// diValueShape declares the Service methods on its pointer, so registering
// the value type is a shape error.
type diValueShape struct{}

func (*diValueShape) Name() string                { return "di-value-shape" }
func (*diValueShape) Init(context.Context) error  { return nil }
func (*diValueShape) Close(context.Context) error { return nil }

func requireDIError(t *testing.T, err, sentinel error, wantText string) {
	t.Helper()
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want one wrapping %v", err, sentinel)
	}
	if !strings.Contains(err.Error(), wantText) {
		t.Errorf("error %q no longer mentions %q", err, wantText)
	}
}

func TestDIError_NotRegistered(t *testing.T) {
	t.Run("container", func(t *testing.T) {
		c := gas.NewServiceContainer()
		_, err := c.Resolve[*diMiss]()
		requireDIError(t, err, gas.ErrNotRegistered, "no registration for *gas_test.diMiss")
	})
	t.Run("scope", func(t *testing.T) {
		c := gas.NewServiceContainer()
		_, err := c.NewScope(context.Background()).Resolve[*diMiss]()
		requireDIError(t, err, gas.ErrNotRegistered, "no registration for *gas_test.diMiss")
	})
	t.Run("missing constructor dependency at startup", func(t *testing.T) {
		w := gas.NewWorker(gas.WithSingletonService[*diA](func(*diMiss) *diA { return &diA{} }))
		err := w.InitServices(context.Background())
		requireDIError(t, err, gas.ErrNotRegistered, "no registration for *gas_test.diMiss")
	})
}

func TestDIError_CircularDependency(t *testing.T) {
	c := gas.NewServiceContainer()
	c.RegisterService[*diA](func(*diB) *diA { return &diA{} }, gas.ServiceLifetimeSingleton)
	c.RegisterService[*diB](func(*diA) *diB { return &diB{} }, gas.ServiceLifetimeSingleton)
	requireDIError(
		t,
		c.BuildAll(context.Background()),
		gas.ErrCircularDependency,
		"circular dependency detected",
	)
}

func TestDIError_CaptiveDependency(t *testing.T) {
	for name, lifetime := range map[string]gas.ServiceLifetime{
		"scoped":    gas.ServiceLifetimeScoped,
		"transient": gas.ServiceLifetimeTransient,
	} {
		t.Run(name, func(t *testing.T) {
			c := gas.NewServiceContainer()
			c.RegisterService[*diB](func() *diB { return &diB{} }, lifetime)
			c.RegisterService[*diA](func(*diB) *diA { return &diA{} }, gas.ServiceLifetimeSingleton)
			requireDIError(t, c.BuildAll(context.Background()), gas.ErrCaptiveDependency,
				"captive dependency: singleton *gas_test.diA depends on "+name)
		})
	}
}

func TestDIError_ScopeRequired(t *testing.T) {
	c := gas.NewServiceContainer()
	c.RegisterScopedService[*diScoped](func() *diScoped { return &diScoped{} })
	if err := c.BuildAll(context.Background()); err != nil {
		t.Fatalf("BuildAll: %v", err)
	}
	_, err := c.Resolve[*diScoped]()
	requireDIError(t, err, gas.ErrScopeRequired, "cannot be resolved outside a scope")

	if _, err := c.NewScope(context.Background()).Resolve[*diScoped](); err != nil {
		t.Errorf("Resolve from a scope: %v", err)
	}
}

func TestDIError_InvalidLifetime(t *testing.T) {
	const bogus = gas.ServiceLifetime(99)
	t.Run("container", func(t *testing.T) {
		c := gas.NewServiceContainer()
		c.RegisterService[*diA](func() *diA { return &diA{} }, bogus)
		_, err := c.Resolve[*diA]()
		requireDIError(t, err, gas.ErrInvalidLifetime, "unknown lifetime for *gas_test.diA")
	})
	t.Run("scope", func(t *testing.T) {
		c := gas.NewServiceContainer()
		c.RegisterService[*diA](func() *diA { return &diA{} }, bogus)
		_, err := c.NewScope(context.Background()).Resolve[*diA]()
		requireDIError(t, err, gas.ErrInvalidLifetime, "unknown lifetime for *gas_test.diA")
	})
}

func TestDIError_InvalidServiceShape(t *testing.T) {
	t.Run("partial service", func(t *testing.T) {
		w := gas.NewWorker(gas.WithServiceInstance[*initOnly](&initOnly{}))
		requireDIError(t, w.InitServices(context.Background()), gas.ErrInvalidServiceShape,
			"does not implement gas.Service")
	})
	t.Run("value type with pointer methods", func(t *testing.T) {
		w := gas.NewWorker(gas.WithSingletonService[diValueShape](func() diValueShape {
			return diValueShape{}
		}))
		requireDIError(
			t,
			w.InitServices(context.Background()),
			gas.ErrInvalidServiceShape,
			"register it as *",
		)
	})
}
