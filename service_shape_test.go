package gas_test

import (
	"strings"
	"testing"

	"github.com/gasmod/gas"
)

// A type that carries an Init method but does not fully implement gas.Service
// used to register fine and then do nothing: the container's auto-Init only
// fires for a complete Service, so Init was never called, Close was never
// called at shutdown, and the kill switch could not see it. Forgetting Name or
// Close while writing a service is the easy way to land there, so registration
// now rejects the type instead.

// initOnly implements Init and nothing else — the reported failure mode.
type initOnly struct{ inited bool }

func (s *initOnly) Init() error { s.inited = true; return nil }

// missingClose implements Name and Init but forgets Close.
type missingClose struct{}

func (s *missingClose) Name() string { return "missing-close" }
func (s *missingClose) Init() error  { return nil }

// wrongInitSig has all three method names but Init returns nothing.
type wrongInitSig struct{}

func (s *wrongInitSig) Name() string { return "wrong-init-sig" }
func (s *wrongInitSig) Init()        {}
func (s *wrongInitSig) Close() error { return nil }

// valueService is a complete Service, but only through its pointer.
type valueService struct{}

func (s *valueService) Name() string { return "value-service" }
func (s *valueService) Init() error  { return nil }
func (s *valueService) Close() error { return nil }

// closeOnly implements Close and nothing else. Its Close would never fire, so
// whatever it holds leaks.
type closeOnly struct{ closed bool }

func (s *closeOnly) Close() error { s.closed = true; return nil }

// nameAndClose forgets Init — the service registers, never initializes, and
// still looks complete at a glance.
type nameAndClose struct{}

func (s *nameAndClose) Name() string { return "name-and-close" }
func (s *nameAndClose) Close() error { return nil }

// plainDep declares neither Init nor Close and is not a service. The same
// registration options carry these, so they must keep working untouched.
// Name alone is deliberately allowed: gas/config registers several such types.
type plainDep struct{ id string }

func (d *plainDep) ID() string   { return d.id }
func (d *plainDep) Name() string { return d.id }

// shapeGreeter is an interface a half-written service might be registered
// under, so the concrete type is only knowable after construction.
type shapeGreeter interface{ Greet() string }

type halfGreeter struct{}

func (g *halfGreeter) Greet() string { return "hi" }
func (g *halfGreeter) Init() error   { return nil }

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

	err := w.InitServices()
	requireErrContains(t, err, "*gas_test.initOnly", "does not implement gas.Service",
		"missing Name() string", "missing Close() error")

	if svc.inited {
		t.Error("Init ran on a rejected type")
	}
}

func TestRegisterRejectsServiceMissingClose(t *testing.T) {
	w := gas.NewWorker(gas.WithSingletonService[*missingClose](func() *missingClose {
		return &missingClose{}
	}))

	err := w.InitServices()
	requireErrContains(t, err, "missingClose", "missing Close() error")
	if strings.Contains(err.Error(), "missing Name") {
		t.Errorf("error should not report Name as missing: %v", err)
	}
}

func TestRegisterRejectsWrongInitSignature(t *testing.T) {
	w := gas.NewWorker(gas.WithSingletonService[*wrongInitSig](func() *wrongInitSig {
		return &wrongInitSig{}
	}))

	err := w.InitServices()
	requireErrContains(t, err, "wrongInitSig", "Init has signature", "want Init() error")
}

func TestRegisterRejectsValueTypeWithPointerMethods(t *testing.T) {
	w := gas.NewWorker(gas.WithSingletonService[valueService](func() valueService {
		return valueService{}
	}))

	err := w.InitServices()
	requireErrContains(t, err, "methods are declared on *", "register it as *")
}

// A half-written service registered under an interface is only detectable once
// the constructor has run, so the check also runs on the constructed value.
func TestRegisterRejectsHalfServiceBehindInterface(t *testing.T) {
	w := gas.NewWorker(gas.WithSingletonService[shapeGreeter](func() shapeGreeter {
		return &halfGreeter{}
	}))

	err := w.InitServices()
	requireErrContains(t, err, "halfGreeter", "does not implement gas.Service")
}

func TestRegisterRejectsScopedHalfService(t *testing.T) {
	w := gas.NewWorker(gas.WithScopedService[*initOnly](func() *initOnly {
		return &initOnly{}
	}))

	err := w.InitServices()
	requireErrContains(t, err, "initOnly", "does not implement gas.Service")
}

// The error must reach the caller through the normal startup path, not just
// through InitServices directly.
func TestHalfServiceFailsStart(t *testing.T) {
	w := gas.NewWorker(gas.WithServiceInstance[*initOnly](&initOnly{}))
	requireErrContains(t, w.Start(), "does not implement gas.Service")
}

func TestRegisterRejectsCloseOnlyType(t *testing.T) {
	svc := &closeOnly{}
	w := gas.NewWorker(gas.WithServiceInstance[*closeOnly](svc))

	err := w.InitServices()
	requireErrContains(t, err, "*gas_test.closeOnly", "declares Close",
		"does not implement gas.Service", "missing Name() string", "missing Init() error")
}

func TestRegisterRejectsServiceMissingInit(t *testing.T) {
	w := gas.NewWorker(gas.WithSingletonService[*nameAndClose](func() *nameAndClose {
		return &nameAndClose{}
	}))

	err := w.InitServices()
	requireErrContains(t, err, "nameAndClose", "missing Init() error")
	if strings.Contains(err.Error(), "missing Name") || strings.Contains(err.Error(), "missing Close") {
		t.Errorf("error should blame only Init: %v", err)
	}
}

// Name alone must stay legal. Six types in gas/config declare a bare
// Name() string and are registered as ordinary dependencies.
func TestBareNameMethodIsNotAService(t *testing.T) {
	w := gas.NewWorker(gas.WithServiceInstance[*plainDep](&plainDep{id: "config-provider"}))
	if err := w.InitServices(); err != nil {
		t.Fatalf("a type with only Name() was rejected: %v", err)
	}
}

func TestPlainDependenciesAreUnaffected(t *testing.T) {
	w := gas.NewWorker(
		gas.WithServiceInstance[*plainDep](&plainDep{id: "instance"}),
		gas.WithSingletonService[*plainDep2](func() *plainDep2 { return &plainDep2{} }),
		gas.WithTransientService[*plainDep3](func() *plainDep3 { return &plainDep3{} }),
	)
	if err := w.InitServices(); err != nil {
		t.Fatalf("plain dependencies rejected: %v", err)
	}
}

type plainDep2 struct{}
type plainDep3 struct{}

// A complete Service still initializes normally.
func TestCompleteServiceStillInitializes(t *testing.T) {
	svc := &testService{name: "complete"}
	w := gas.NewWorker(gas.WithServiceInstance[*testService](svc))
	if err := w.InitServices(); err != nil {
		t.Fatalf("InitServices: %v", err)
	}
	if got := w.ActiveServices(); len(got) != 1 || got[0] != "complete" {
		t.Errorf("ActiveServices = %v, want [complete]", got)
	}
}
