package gas_test

import (
	"strings"
	"sync"
	"testing"

	"github.com/gasmod/gas"
)

// Worker.Shutdown documents that it "closes all services in reverse
// initialization order", which is what makes it safe for a service to use its
// dependencies during Close. It does not do that.
//
// InitServices builds w.serviceOrder by iterating EachInstance, which ranges
// over ServiceContainer.instances — a map[reflect.Type]reflect.Value. Go
// randomizes map iteration order, so serviceOrder is a random permutation of
// the services rather than the order they were initialized in, and reversing a
// random permutation is still random. Construction order is correct (BuildAll
// walks the topological sort); only the close order is lost.
//
// Consequence: a service can be closed AFTER a dependency it uses inside
// Close(), so Close() runs against an already-torn-down dependency. Which
// services are affected changes from process to process, so this surfaces as
// an intermittent shutdown failure in production rather than a reproducible
// one.
//
// The chain below is S0 <- S1 <- ... <- S5 (each depends on the previous), so
// the only correct close order is S5, S4, S3, S2, S1, S0.

type ordTag0 struct{}
type ordTag1 struct{}
type ordTag2 struct{}
type ordTag3 struct{}
type ordTag4 struct{}
type ordTag5 struct{}

// ordLog records init and close calls in the order they happen.
type ordLog struct {
	mu     sync.Mutex
	inits  []string
	closes []string
}

func (l *ordLog) init(name string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.inits = append(l.inits, name)
}

func (l *ordLog) close(name string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closes = append(l.closes, name)
}

func (l *ordLog) snapshot() (inits, closes []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.inits...), append([]string(nil), l.closes...)
}

// ordService is a complete gas.Service. The phantom type parameter K gives each
// link in the chain a distinct type for the container to key on.
type ordService[K any] struct {
	log  *ordLog
	name string
}

func (s *ordService[K]) Name() string { return s.name }
func (s *ordService[K]) Init() error  { s.log.init(s.name); return nil }
func (s *ordService[K]) Close() error { s.log.close(s.name); return nil }

func newOrdWorker(log *ordLog) *gas.Worker {
	return gas.NewWorker(
		gas.WithSingletonService[gas.Logger](gas.NewNopLogger()),
		gas.WithSingletonService[*ordService[ordTag0]](func() *ordService[ordTag0] {
			return &ordService[ordTag0]{log: log, name: "S0"}
		}),
		gas.WithSingletonService[*ordService[ordTag1]](func(*ordService[ordTag0]) *ordService[ordTag1] {
			return &ordService[ordTag1]{log: log, name: "S1"}
		}),
		gas.WithSingletonService[*ordService[ordTag2]](func(*ordService[ordTag1]) *ordService[ordTag2] {
			return &ordService[ordTag2]{log: log, name: "S2"}
		}),
		gas.WithSingletonService[*ordService[ordTag3]](func(*ordService[ordTag2]) *ordService[ordTag3] {
			return &ordService[ordTag3]{log: log, name: "S3"}
		}),
		gas.WithSingletonService[*ordService[ordTag4]](func(*ordService[ordTag3]) *ordService[ordTag4] {
			return &ordService[ordTag4]{log: log, name: "S4"}
		}),
		gas.WithSingletonService[*ordService[ordTag5]](func(*ordService[ordTag4]) *ordService[ordTag5] {
			return &ordService[ordTag5]{log: log, name: "S5"}
		}),
	)
}

// TestShutdownClosesServicesInReverseInitOrder is the contract Shutdown
// documents. It is repeated because the failure is driven by Go's randomized
// map iteration: a single run can land on the correct permutation by chance
// (1 in 720 for six services), so a one-shot test would be flaky. Every
// repetition must satisfy the contract.
func TestShutdownClosesServicesInReverseInitOrder(t *testing.T) {
	const repeats = 20

	wantInit := "S0 S1 S2 S3 S4 S5"
	wantClose := "S5 S4 S3 S2 S1 S0"

	for run := range repeats {
		log := &ordLog{}
		w := newOrdWorker(log)

		if err := w.InitServices(); err != nil {
			t.Fatalf("run %d: InitServices: %v", run, err)
		}
		if err := w.Shutdown(); err != nil {
			t.Fatalf("run %d: Shutdown: %v", run, err)
		}

		inits, closes := log.snapshot()
		gotInit := strings.Join(inits, " ")
		gotClose := strings.Join(closes, " ")

		// Construction order is derived from the topological sort and is
		// expected to hold; asserting it isolates the defect to the close path.
		if gotInit != wantInit {
			t.Fatalf("run %d: init order = [%s], want [%s]", run, gotInit, wantInit)
		}
		if gotClose != wantClose {
			t.Fatalf("run %d: close order = [%s], want [%s]\n"+
				"Shutdown must close services in reverse initialization order so a "+
				"service can still use its dependencies inside Close()", run, gotClose, wantClose)
		}
	}
}

// TestShutdownNeverClosesADependencyFirst states the safety property the
// ordering exists to guarantee, independent of the exact permutation: when
// Close() runs for a service, every dependency it was built from must still be
// open.
func TestShutdownNeverClosesADependencyFirst(t *testing.T) {
	const repeats = 20

	// index in the chain: S0 is the deepest dependency, S5 the outermost.
	depth := map[string]int{"S0": 0, "S1": 1, "S2": 2, "S3": 3, "S4": 4, "S5": 5}

	for run := range repeats {
		log := &ordLog{}
		w := newOrdWorker(log)

		if err := w.InitServices(); err != nil {
			t.Fatalf("run %d: InitServices: %v", run, err)
		}
		if err := w.Shutdown(); err != nil {
			t.Fatalf("run %d: Shutdown: %v", run, err)
		}

		_, closes := log.snapshot()
		closed := map[string]bool{}
		for _, name := range closes {
			// Everything this service depends on is shallower in the chain.
			for dep, d := range depth {
				if d < depth[name] && closed[dep] {
					t.Fatalf("run %d: %s closed after its dependency %s (order: [%s])",
						run, name, dep, strings.Join(closes, " "))
				}
			}
			closed[name] = true
		}
	}
}

// Scope.Close had the identical defect on the per-request path: it ranged over
// the scope's resolved map, so scoped services were torn down in a random
// order and one could be closed before a scoped dependency it uses in Close().
func TestScopeClosesInReverseResolutionOrder(t *testing.T) {
	const repeats = 20

	wantClose := "S2 S1 S0"

	for run := range repeats {
		log := &ordLog{}
		c := gas.NewServiceContainer()

		gas.RegisterCtor[*ordService[ordTag0]](c, func() *ordService[ordTag0] {
			return &ordService[ordTag0]{log: log, name: "S0"}
		}, gas.ServiceLifetimeScoped)
		gas.RegisterCtor[*ordService[ordTag1]](c, func(*ordService[ordTag0]) *ordService[ordTag1] {
			return &ordService[ordTag1]{log: log, name: "S1"}
		}, gas.ServiceLifetimeScoped)
		gas.RegisterCtor[*ordService[ordTag2]](c, func(*ordService[ordTag1]) *ordService[ordTag2] {
			return &ordService[ordTag2]{log: log, name: "S2"}
		}, gas.ServiceLifetimeScoped)

		scope := c.NewScope()
		if _, err := gas.Resolve[*ordService[ordTag2]](scope); err != nil {
			t.Fatalf("run %d: resolve: %v", run, err)
		}
		if err := scope.Close(); err != nil {
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
