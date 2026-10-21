package gas_test

import (
	"context"
	"errors"
	"slices"
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
				c.RegisterService[greeter](newConcreteGreeter, gas.ServiceLifetimeSingleton)
			},
		},
		{
			name: "concrete type returning itself",
			register: func(c *gas.ServiceContainer) {
				c.RegisterService[*concreteGreeter](
					newConcreteGreeter,
					gas.ServiceLifetimeSingleton,
				)
			},
		},
		{
			name: "two results with error",
			register: func(c *gas.ServiceContainer) {
				c.RegisterService[greeter](newGreeterWithError, gas.ServiceLifetimeSingleton)
			},
		},
		{
			name: "constructor with dependencies",
			register: func(c *gas.ServiceContainer) {
				c.RegisterService[*plainDep](newPlainDep, gas.ServiceLifetimeSingleton)
				c.RegisterService[greeter](func(_ *plainDep) *concreteGreeter {
					return newConcreteGreeter()
				}, gas.ServiceLifetimeSingleton)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := gas.NewServiceContainer()
			tt.register(c)

			if err := c.BuildAll(context.Background()); err != nil {
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
	c.RegisterService[greeter](newConcreteGreeter, gas.ServiceLifetimeSingleton)

	if err := c.BuildAll(context.Background()); err != nil {
		t.Fatalf("BuildAll: %v", err)
	}

	g, err := c.Resolve[greeter]()
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
				c.RegisterService[greeter](nil, gas.ServiceLifetimeSingleton)
			},
		},
		{
			name:  "non-function constructor",
			wants: []string{"want a function"},
			register: func(c *gas.ServiceContainer) {
				c.RegisterService[*concreteGreeter](
					&concreteGreeter{},
					gas.ServiceLifetimeSingleton,
				)
			},
		},
		{
			name:  "variadic constructor",
			wants: []string{"variadic"},
			register: func(c *gas.ServiceContainer) {
				c.RegisterService[greeter](func(_ ...*plainDep) *concreteGreeter {
					return newConcreteGreeter()
				}, gas.ServiceLifetimeSingleton)
			},
		},
		{
			name:  "no results",
			wants: []string{"returns 0 values"},
			register: func(c *gas.ServiceContainer) {
				c.RegisterService[greeter](func() {}, gas.ServiceLifetimeSingleton)
			},
		},
		{
			// invoke only checks the error when NumOut is 2, so this used to
			// register, build, and silently drop the constructor's error.
			name:  "three results",
			wants: []string{"returns 3 values"},
			register: func(c *gas.ServiceContainer) {
				c.RegisterService[greeter](func() (*concreteGreeter, error, error) {
					return nil, nil, errors.New("dropped")
				}, gas.ServiceLifetimeSingleton)
			},
		},
		{
			name:  "second result is not error",
			wants: []string{"second result", "want error"},
			register: func(c *gas.ServiceContainer) {
				c.RegisterService[greeter](func() (*concreteGreeter, notAnError) {
					return newConcreteGreeter(), notAnError{}
				}, gas.ServiceLifetimeSingleton)
			},
		},
		{
			name:  "result does not implement the registered interface",
			wants: []string{"not assignable to"},
			register: func(c *gas.ServiceContainer) {
				c.RegisterService[greeter](
					func() *unrelated { return &unrelated{} },
					gas.ServiceLifetimeSingleton,
				)
			},
		},
		{
			name:  "result is not the registered concrete type",
			wants: []string{"not assignable to"},
			register: func(c *gas.ServiceContainer) {
				c.RegisterService[*concreteGreeter](newPlainDep, gas.ServiceLifetimeSingleton)
			},
		},
		{
			name:  "scoped registration is validated too",
			wants: []string{"returns 0 values"},
			register: func(c *gas.ServiceContainer) {
				c.RegisterService[greeter](func() {}, gas.ServiceLifetimeScoped)
			},
		},
		{
			name:  "transient registration is validated too",
			wants: []string{"returns 0 values"},
			register: func(c *gas.ServiceContainer) {
				c.RegisterService[*plainDep](func() {}, gas.ServiceLifetimeTransient)
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
		c.RegisterService[greeter](func() {}, gas.ServiceLifetimeSingleton)
	})

	if err := c.BuildAll(context.Background()); err != nil {
		t.Fatalf("BuildAll after a rejected registration: %v", err)
	}

	if _, err := c.Resolve[greeter](); err == nil {
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
		c.RegisterService[*testService](func() *testService {
			return &testService{name: "transient"}
		}, gas.ServiceLifetimeTransient)
	})
}

// missingClose implements Name and Init but forgets Close.
type missingClose struct{}

func (s *missingClose) Name() string { return "missing-close" }

func (s *missingClose) Init(context.Context) error { return nil }

// wrongInitSig has all three method names but Init returns nothing.
type wrongInitSig struct{}

func (s *wrongInitSig) Name() string { return "wrong-init-sig" }

func (s *wrongInitSig) Init() {}

func (s *wrongInitSig) Close(context.Context) error { return nil }

// valueService is a complete Service, but only through its pointer.
type valueService struct{}

func (s *valueService) Name() string { return "value-service" }

func (s *valueService) Init(context.Context) error { return nil }

func (s *valueService) Close(context.Context) error { return nil }

// closeOnly implements Close and nothing else. Its Close would never fire, so
// whatever it holds leaks.
type closeOnly struct{ closed bool }

func (s *closeOnly) Close(context.Context) error { s.closed = true; return nil }

// nameAndClose forgets Init — the service registers, never initializes, and
// still looks complete at a glance.
type nameAndClose struct{}

func (s *nameAndClose) Name() string { return "name-and-close" }

func (s *nameAndClose) Close(context.Context) error { return nil }

// plainDep declares neither Init nor Close and is not a service. The same
// registration options carry these, so they must keep working untouched.
// Name alone is deliberately allowed: gas/config registers several such types.
type plainDep struct{ id string }

func (d *plainDep) ID() string { return d.id }

func (d *plainDep) Name() string { return d.id }

// shapeGreeter is an interface a half-written service might be registered
// under, so the concrete type is only knowable after construction.
type shapeGreeter interface{ Greet() string }

type halfGreeter struct{}

func (g *halfGreeter) Greet() string { return "hi" }

func (g *halfGreeter) Init(context.Context) error { return nil }

func requireErrContains(t *testing.T, err error, want ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	for _, w := range want {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("error %q does not mention %q", err.Error(), w)
		}
	}
}

func TestRegisterRejectsInitOnlyType(t *testing.T) {
	svc := &initOnly{}
	w := gas.NewWorker(gas.WithServiceInstance[*initOnly](svc))

	err := w.InitServices(context.Background())
	requireErrContains(t, err, "*gas_test.initOnly", "does not implement gas.Service",
		"missing Name() string", "missing Close(context.Context) error")

	if svc.inited {
		t.Error("Init ran on a rejected type")
	}
}

func TestRegisterRejectsServiceMissingClose(t *testing.T) {
	w := gas.NewWorker(gas.WithSingletonService[*missingClose](func() *missingClose {
		return &missingClose{}
	}))

	err := w.InitServices(context.Background())
	requireErrContains(t, err, "missingClose", "missing Close(context.Context) error")
	if strings.Contains(err.Error(), "missing Name") {
		t.Errorf("error should not report Name as missing: %v", err)
	}
}

func TestRegisterRejectsWrongInitSignature(t *testing.T) {
	w := gas.NewWorker(gas.WithSingletonService[*wrongInitSig](func() *wrongInitSig {
		return &wrongInitSig{}
	}))

	err := w.InitServices(context.Background())
	requireErrContains(
		t,
		err,
		"wrongInitSig",
		"Init has signature",
		"want Init(context.Context) error",
	)
}

func TestRegisterRejectsValueTypeWithPointerMethods(t *testing.T) {
	w := gas.NewWorker(gas.WithSingletonService[valueService](func() valueService {
		return valueService{}
	}))

	err := w.InitServices(context.Background())
	requireErrContains(t, err, "methods are declared on *", "register it as *")
}

// A half-written service registered under an interface is only detectable once
// the constructor has run, so the check also runs on the constructed value.
func TestRegisterRejectsHalfServiceBehindInterface(t *testing.T) {
	w := gas.NewWorker(gas.WithSingletonService[shapeGreeter](func() shapeGreeter {
		return &halfGreeter{}
	}))

	err := w.InitServices(context.Background())
	requireErrContains(t, err, "halfGreeter", "does not implement gas.Service")
}

func TestRegisterRejectsScopedHalfService(t *testing.T) {
	w := gas.NewWorker(gas.WithScopedService[*initOnly](func() *initOnly {
		return &initOnly{}
	}))

	err := w.InitServices(context.Background())
	requireErrContains(t, err, "initOnly", "does not implement gas.Service")
}

// The error must reach the caller through the normal startup path, not just
// through InitServices directly.
func TestHalfServiceFailsStart(t *testing.T) {
	w := gas.NewWorker(gas.WithServiceInstance[*initOnly](&initOnly{}))
	requireErrContains(t, w.Start(context.Background()), "does not implement gas.Service")
}

func TestRegisterRejectsCloseOnlyType(t *testing.T) {
	svc := &closeOnly{}
	w := gas.NewWorker(gas.WithServiceInstance[*closeOnly](svc))

	err := w.InitServices(context.Background())
	requireErrContains(
		t,
		err,
		"*gas_test.closeOnly",
		"declares Close",
		"does not implement gas.Service",
		"missing Name() string",
		"missing Init(context.Context) error",
	)
}

func TestRegisterRejectsServiceMissingInit(t *testing.T) {
	w := gas.NewWorker(gas.WithSingletonService[*nameAndClose](func() *nameAndClose {
		return &nameAndClose{}
	}))

	err := w.InitServices(context.Background())
	requireErrContains(t, err, "nameAndClose", "missing Init(context.Context) error")
	if strings.Contains(err.Error(), "missing Name") ||
		strings.Contains(err.Error(), "missing Close") {
		t.Errorf("error should blame only Init: %v", err)
	}
}

// Name alone must stay legal. Six types in gas/config declare a bare
// Name() string and are registered as ordinary dependencies.
func TestBareNameMethodIsNotAService(t *testing.T) {
	w := gas.NewWorker(gas.WithServiceInstance[*plainDep](&plainDep{id: "config-provider"}))
	if err := w.InitServices(context.Background()); err != nil {
		t.Fatalf("a type with only Name() was rejected: %v", err)
	}
}

func TestPlainDependenciesAreUnaffected(t *testing.T) {
	w := gas.NewWorker(
		gas.WithServiceInstance[*plainDep](&plainDep{id: "instance"}),
		gas.WithSingletonService[*plainDep2](func() *plainDep2 { return &plainDep2{} }),
		gas.WithTransientService[*plainDep3](func() *plainDep3 { return &plainDep3{} }),
	)
	if err := w.InitServices(context.Background()); err != nil {
		t.Fatalf("plain dependencies rejected: %v", err)
	}
}

type (
	plainDep2 struct{}
	plainDep3 struct{}
)

// A complete Service still initializes normally.
func TestCompleteServiceStillInitializes(t *testing.T) {
	svc := &testService{name: "complete"}
	w := gas.NewWorker(gas.WithServiceInstance[*testService](svc))
	if err := w.InitServices(context.Background()); err != nil {
		t.Fatalf("InitServices: %v", err)
	}
	if got := w.ActiveServices(); len(got) != 2 || !slices.Contains(got, "complete") {
		t.Errorf("ActiveServices = %v, want [gas/eventbus, complete]", got)
	}
}
