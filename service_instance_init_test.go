package gas_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/gasmod/gas"
)

// A complete Service registered with WithServiceInstance was never initialized.
// Auto-Init lives in ServiceContainer.invoke, and BuildAll skips types already
// present in the instance map, so a pre-built instance never reached it. It was
// still collected into serviceOrder, so the container closed a service it had
// never initialized.
//
// Implementing gas.Service means the container manages the lifecycle. That is
// now enforced at registration (declaring Init or Close commits a type to the
// whole interface), so it has to hold however the instance got there.

type instTagA struct{}
type instTagB struct{}

func TestServiceInstanceIsInitialized(t *testing.T) {
	log := &ordLog{}
	svc := &ordService[instTagA]{log: log, name: "instance"}

	w := gas.NewWorker(
		gas.WithSingletonService[gas.Logger](gas.NewNopLogger()),
		gas.WithServiceInstance[*ordService[instTagA]](svc),
	)
	if err := w.InitServices(); err != nil {
		t.Fatalf("InitServices: %v", err)
	}

	inits, _ := log.snapshot()
	if got := strings.Join(inits, " "); got != "instance" {
		t.Errorf("init calls = [%s], want [instance]", got)
	}
	if got := w.ActiveServices(); len(got) != 1 || got[0] != "instance" {
		t.Errorf("ActiveServices = %v, want [instance]", got)
	}
}

// InitServices is idempotent, so the instance must not be initialized twice.
func TestServiceInstanceInitializedExactlyOnce(t *testing.T) {
	log := &ordLog{}
	svc := &ordService[instTagA]{log: log, name: "instance"}

	w := gas.NewWorker(
		gas.WithSingletonService[gas.Logger](gas.NewNopLogger()),
		gas.WithServiceInstance[*ordService[instTagA]](svc),
	)
	for range 3 {
		if err := w.InitServices(); err != nil {
			t.Fatalf("InitServices: %v", err)
		}
	}

	inits, _ := log.snapshot()
	if len(inits) != 1 {
		t.Errorf("Init ran %d times (%v), want 1", len(inits), inits)
	}
}

// A failing Init must abort startup, exactly as it does for a constructed
// service, rather than leaving a half-built app running.
type failingInstance struct{}

var errInstanceInit = errors.New("instance init failed")

func (s *failingInstance) Name() string { return "failing-instance" }
func (s *failingInstance) Init() error  { return errInstanceInit }
func (s *failingInstance) Close() error { return nil }

func TestServiceInstanceInitErrorAbortsStartup(t *testing.T) {
	w := gas.NewWorker(
		gas.WithSingletonService[gas.Logger](gas.NewNopLogger()),
		gas.WithServiceInstance[*failingInstance](&failingInstance{}),
	)

	err := w.InitServices()
	if err == nil {
		t.Fatal("expected InitServices to fail")
	}
	if !errors.Is(err, errInstanceInit) {
		t.Errorf("error %v does not wrap the service's own error", err)
	}
}

// A pre-registered instance was available before anything the container
// builds, so it must initialize first and close last. Otherwise the init order
// and the reverse-close order disagree.
func TestServiceInstanceInitializesBeforeConstructedServices(t *testing.T) {
	const repeats = 10

	for run := range repeats {
		log := &ordLog{}
		instance := &ordService[instTagA]{log: log, name: "instance"}

		w := gas.NewWorker(
			gas.WithSingletonService[gas.Logger](gas.NewNopLogger()),
			gas.WithServiceInstance[*ordService[instTagA]](instance),
			gas.WithSingletonService[*ordService[instTagB]](func() *ordService[instTagB] {
				return &ordService[instTagB]{log: log, name: "constructed"}
			}),
		)
		if err := w.InitServices(); err != nil {
			t.Fatalf("run %d: InitServices: %v", run, err)
		}
		if err := w.Shutdown(); err != nil {
			t.Fatalf("run %d: Shutdown: %v", run, err)
		}

		inits, closes := log.snapshot()
		if got := strings.Join(inits, " "); got != "instance constructed" {
			t.Fatalf("run %d: init order = [%s], want [instance constructed]", run, got)
		}
		if got := strings.Join(closes, " "); got != "constructed instance" {
			t.Fatalf("run %d: close order = [%s], want [constructed instance]", run, got)
		}
	}
}
