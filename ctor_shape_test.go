package gas_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/gasmod/gas"
)

// A constructor registered with a signature the container cannot call used to
// register cleanly and blow up much later, inside invoke: reflect panicked out
// of BuildAll naming neither the constructor nor the type it was registered
// for. The three-result case was worse than a panic — invoke only checks the
// error when NumOut is 2, so the error was dropped and the service built
// anyway. Registration now rejects the signature at the call site.

// greeter is the interface half of the dominant registration pattern:
// registered as an interface, constructed as a concrete pointer.
type greeter interface {
	Greet() string
}

type concreteGreeter struct{ prefix string }

func (g *concreteGreeter) Greet() string { return g.prefix + " hello" }

func newConcreteGreeter() *concreteGreeter { return &concreteGreeter{prefix: "gas:"} }

// newGreeterWithError is the two-result form of the same constructor.
func newGreeterWithError() (*concreteGreeter, error) { return newConcreteGreeter(), nil }

// newPlainDep constructs the non-Service dependency from service_shape_test.go,
// the other common registration shape.
func newPlainDep() *plainDep { return &plainDep{id: "dep"} }

// unrelated is returned by constructors that do not satisfy the registered type.
type unrelated struct{}

// notAnError is a non-error second result, which invoke's results[1].IsNil()
// could not have handled.
type notAnError struct{}

func requirePanic(t *testing.T, wantSubstrings []string, fn func()) {
	t.Helper()

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected registration to panic, it did not")
		}

		msg, ok := r.(string)
		if !ok {
			t.Fatalf("expected a string panic value, got %T: %v", r, r)
		}

		if !strings.HasPrefix(msg, "gas: ") {
			t.Errorf("panic message should be caller-facing and gas-prefixed, got %q", msg)
		}

		for _, want := range wantSubstrings {
			if !strings.Contains(msg, want) {
				t.Errorf("panic message %q does not mention %q", msg, want)
			}
		}
	}()

	fn()
}

// TestRegisterCtor_AcceptsValidShapes guards the signatures that must keep
// working. The interface case is the one to watch: a constructor returning
// *concreteGreeter is not assignable to greeter, only implements it, so a
// naive AssignableTo check would break nearly every real registration.
func TestRegisterCtor_AcceptsValidShapes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		register func(*gas.ServiceContainer)
		name     string
	}{
		{
			name: "interface registered with concrete pointer constructor",
			register: func(c *gas.ServiceContainer) {
				gas.RegisterCtor[greeter](c, newConcreteGreeter, gas.ServiceLifetimeSingleton)
			},
		},
		{
			name: "concrete type returning itself",
			register: func(c *gas.ServiceContainer) {
				gas.RegisterCtor[*concreteGreeter](c, newConcreteGreeter, gas.ServiceLifetimeSingleton)
			},
		},
		{
			name: "two results with error",
			register: func(c *gas.ServiceContainer) {
				gas.RegisterCtor[greeter](c, newGreeterWithError, gas.ServiceLifetimeSingleton)
			},
		},
		{
			name: "constructor with dependencies",
			register: func(c *gas.ServiceContainer) {
				gas.RegisterCtor[*plainDep](c, newPlainDep, gas.ServiceLifetimeSingleton)
				gas.RegisterCtor[greeter](c, func(_ *plainDep) *concreteGreeter {
					return newConcreteGreeter()
				}, gas.ServiceLifetimeSingleton)
			},
		},
		{
			name: "reflection-based entry point",
			register: func(c *gas.ServiceContainer) {
				c.RegisterSingletonService(gas.TypePtr[*concreteGreeter](), newConcreteGreeter)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := gas.NewServiceContainer()
			tt.register(c)

			if err := c.BuildAll(); err != nil {
				t.Fatalf("BuildAll: %v", err)
			}
		})
	}
}

// TestRegisterCtor_InterfaceRegistrationResolves confirms the accepted
// interface shape is not merely registerable but still builds and resolves,
// so the validator is not passing something invoke would reject.
func TestRegisterCtor_InterfaceRegistrationResolves(t *testing.T) {
	t.Parallel()

	c := gas.NewServiceContainer()
	gas.RegisterCtor[greeter](c, newConcreteGreeter, gas.ServiceLifetimeSingleton)

	if err := c.BuildAll(); err != nil {
		t.Fatalf("BuildAll: %v", err)
	}

	g, err := gas.Resolve[greeter](c)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if got := g.Greet(); got != "gas: hello" {
		t.Errorf("Greet() = %q, want %q", got, "gas: hello")
	}
}

func TestRegisterCtor_RejectsBadShapes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		register func(*gas.ServiceContainer)
		name     string
		wants    []string
	}{
		{
			name:  "nil constructor",
			wants: []string{"want a function", "nil"},
			register: func(c *gas.ServiceContainer) {
				gas.RegisterCtor[greeter](c, nil, gas.ServiceLifetimeSingleton)
			},
		},
		{
			name:  "non-function constructor",
			wants: []string{"want a function"},
			register: func(c *gas.ServiceContainer) {
				gas.RegisterCtor[*concreteGreeter](c, &concreteGreeter{}, gas.ServiceLifetimeSingleton)
			},
		},
		{
			name:  "variadic constructor",
			wants: []string{"variadic"},
			register: func(c *gas.ServiceContainer) {
				gas.RegisterCtor[greeter](c, func(_ ...*plainDep) *concreteGreeter {
					return newConcreteGreeter()
				}, gas.ServiceLifetimeSingleton)
			},
		},
		{
			name:  "no results",
			wants: []string{"returns 0 values"},
			register: func(c *gas.ServiceContainer) {
				gas.RegisterCtor[greeter](c, func() {}, gas.ServiceLifetimeSingleton)
			},
		},
		{
			// invoke only checks the error when NumOut is 2, so this used to
			// register, build, and silently drop the constructor's error.
			name:  "three results",
			wants: []string{"returns 3 values"},
			register: func(c *gas.ServiceContainer) {
				gas.RegisterCtor[greeter](c, func() (*concreteGreeter, error, error) {
					return nil, nil, errors.New("dropped")
				}, gas.ServiceLifetimeSingleton)
			},
		},
		{
			name:  "second result is not error",
			wants: []string{"second result", "want error"},
			register: func(c *gas.ServiceContainer) {
				gas.RegisterCtor[greeter](c, func() (*concreteGreeter, notAnError) {
					return newConcreteGreeter(), notAnError{}
				}, gas.ServiceLifetimeSingleton)
			},
		},
		{
			name:  "result does not implement the registered interface",
			wants: []string{"not assignable to"},
			register: func(c *gas.ServiceContainer) {
				gas.RegisterCtor[greeter](c, func() *unrelated { return &unrelated{} }, gas.ServiceLifetimeSingleton)
			},
		},
		{
			name:  "result is not the registered concrete type",
			wants: []string{"not assignable to"},
			register: func(c *gas.ServiceContainer) {
				gas.RegisterCtor[*concreteGreeter](c, newPlainDep, gas.ServiceLifetimeSingleton)
			},
		},
		{
			name:  "reflection-based entry point is validated too",
			wants: []string{"want a function"},
			register: func(c *gas.ServiceContainer) {
				c.RegisterSingletonService(gas.TypePtr[*concreteGreeter](), "not a constructor")
			},
		},
		{
			name:  "scoped registration is validated too",
			wants: []string{"returns 0 values"},
			register: func(c *gas.ServiceContainer) {
				gas.RegisterCtor[greeter](c, func() {}, gas.ServiceLifetimeScoped)
			},
		},
		{
			name:  "transient registration is validated too",
			wants: []string{"returns 0 values"},
			register: func(c *gas.ServiceContainer) {
				gas.RegisterCtor[*plainDep](c, func() {}, gas.ServiceLifetimeTransient)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := gas.NewServiceContainer()
			requirePanic(t, tt.wants, func() { tt.register(c) })
		})
	}
}

// TestRegisterCtor_RejectedCtorIsNotRegistered checks the panic happens before
// anything lands in the container, so a recovered panic does not leave a
// half-registered type that fails later.
func TestRegisterCtor_RejectedCtorIsNotRegistered(t *testing.T) {
	t.Parallel()

	c := gas.NewServiceContainer()

	requirePanic(t, []string{"returns 0 values"}, func() {
		gas.RegisterCtor[greeter](c, func() {}, gas.ServiceLifetimeSingleton)
	})

	if err := c.BuildAll(); err != nil {
		t.Fatalf("BuildAll after a rejected registration: %v", err)
	}

	if _, err := gas.Resolve[greeter](c); err == nil {
		t.Fatal("expected greeter to be unregistered after the rejected registration")
	}
}

// TestRegisterCtor_TransientServiceCheckStillApplies guards the ordering of the
// two panics: a transient Service has a valid signature, so it must still be
// caught by the lifetime check rather than shadowed by the shape check.
func TestRegisterCtor_TransientServiceCheckStillApplies(t *testing.T) {
	t.Parallel()

	c := gas.NewServiceContainer()

	requirePanic(t, []string{"transient service", "Singleton or Scoped"}, func() {
		gas.RegisterCtor[*testService](c, func() *testService {
			return &testService{name: "transient"}
		}, gas.ServiceLifetimeTransient)
	})
}
