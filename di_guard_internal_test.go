package gas

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type (
	cycleSelf  struct{}
	cycleInit  struct{ c *ServiceContainer }
	cycleTrans struct{}
	lazyOnce   struct{ n int }
	waitA      struct{}
	waitB      struct{}
)

func (*cycleInit) Name() string { return "cycle-init" }

// Init resolves the type being built through the captured container.
func (s *cycleInit) Init(context.Context) error {
	//nolint:contextcheck // the cycle under test goes through Resolve, which takes no context
	_, err := s.c.Resolve[*cycleInit]()
	return err
}

func (*cycleInit) Close(context.Context) error { return nil }

// KI-005: a constructor that resolves its own type through a captured
// container used to recurse until the process died with a stack overflow.
// It must return ErrCircularDependency naming the cycle.
func TestDI_CapturedContainerSelfCycle(t *testing.T) {
	t.Parallel()

	c := NewServiceContainer()
	c.RegisterSingletonService[*cycleSelf](func() (*cycleSelf, error) {
		_, err := c.Resolve[*cycleSelf]()
		return &cycleSelf{}, err
	})

	err := c.BuildAll(context.Background())
	if !errors.Is(err, ErrCircularDependency) {
		t.Fatalf("BuildAll() = %v, want %v", err, ErrCircularDependency)
	}
	if err == nil || !strings.Contains(err.Error(), "cycleSelf -> ") {
		t.Errorf("BuildAll() error %q does not name the cycle", err)
	}
}

// An Init that resolves its own type through a captured container is the
// same cycle.
func TestDI_InitSelfCycle(t *testing.T) {
	t.Parallel()

	c := NewServiceContainer()
	c.RegisterSingletonService[*cycleInit](func() *cycleInit { return &cycleInit{c: c} })

	if _, err := c.Resolve[*cycleInit](); !errors.Is(err, ErrCircularDependency) {
		t.Fatalf("Resolve() = %v, want %v", err, ErrCircularDependency)
	}
}

// A transient that resolves itself through a captured container is caught
// too, not only singletons.
func TestDI_TransientSelfCycle(t *testing.T) {
	t.Parallel()

	c := NewServiceContainer()
	c.RegisterTransientService[*cycleTrans](func() (*cycleTrans, error) {
		_, err := c.Resolve[*cycleTrans]()
		return &cycleTrans{}, err
	})

	if _, err := c.Resolve[*cycleTrans](); !errors.Is(err, ErrCircularDependency) {
		t.Fatalf("Resolve() = %v, want %v", err, ErrCircularDependency)
	}
}

// KI-011: concurrent first resolutions of a lazy singleton build it once;
// every caller gets that one instance, with no data race.
func TestDI_ConcurrentLazySingletonBuildsOnce(t *testing.T) {
	t.Parallel()

	var built atomic.Int32
	c := NewServiceContainer()
	c.RegisterSingletonService[*lazyOnce](func() *lazyOnce {
		built.Add(1)
		time.Sleep(20 * time.Millisecond)
		return &lazyOnce{n: 1}
	})

	const callers = 8
	got := make([]*lazyOnce, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Go(func() {
			v, err := c.Resolve[*lazyOnce]()
			if err != nil {
				t.Errorf("Resolve() error: %v", err)
			}
			got[i] = v
		})
	}
	wg.Wait()

	if n := built.Load(); n != 1 {
		t.Errorf("constructor ran %d times, want 1", n)
	}
	for i, v := range got {
		if v != got[0] {
			t.Errorf("caller %d got a different instance", i)
		}
	}
}

// Two goroutines each building one singleton that resolves the other would
// wait on each other forever. One of them must get ErrCircularDependency.
func TestDI_CrossGoroutineWaitCycle(t *testing.T) {
	t.Parallel()

	aStarted := make(chan struct{})
	bStarted := make(chan struct{})
	c := NewServiceContainer()
	c.RegisterSingletonService[*waitA](func() (*waitA, error) {
		close(aStarted)
		<-bStarted
		_, err := c.Resolve[*waitB]()
		return &waitA{}, err
	})
	c.RegisterSingletonService[*waitB](func() (*waitB, error) {
		close(bStarted)
		<-aStarted
		_, err := c.Resolve[*waitA]()
		return &waitB{}, err
	})

	errs := make(chan error, 2)
	go func() { _, err := c.Resolve[*waitA](); errs <- err }()
	go func() { _, err := c.Resolve[*waitB](); errs <- err }()

	var cycle bool
	for range 2 {
		select {
		case err := <-errs:
			if errors.Is(err, ErrCircularDependency) {
				cycle = true
			}
		case <-time.After(5 * time.Second):
			t.Fatal("resolutions deadlocked")
		}
	}
	if !cycle {
		t.Error("neither resolution reported ErrCircularDependency")
	}
}

// A chain of distinct types deeper than the resolution limit fails with an
// error instead of exhausting the stack.
func TestDI_DepthLimit(t *testing.T) {
	t.Parallel()

	c := NewServiceContainer()
	const depth = maxResolveDepth + 10
	types := make([]reflect.Type, depth)
	for i := range types {
		types[i] = reflect.ArrayOf(i+1, reflect.TypeFor[byte]())
	}
	for i, typ := range types {
		var in []reflect.Type
		if i+1 < depth {
			in = []reflect.Type{types[i+1]}
		}
		fnType := reflect.FuncOf(in, []reflect.Type{typ}, false)
		ctor := reflect.MakeFunc(fnType, func([]reflect.Value) []reflect.Value {
			return []reflect.Value{reflect.New(typ).Elem()}
		})
		c.registrations.Set(typ, registration{
			ctor:     ctor.Interface(),
			lifetime: ServiceLifetimeTransient,
		})
	}

	_, err := c.resolveType(context.Background(), types[0])
	if !errors.Is(err, ErrCircularDependency) {
		t.Fatalf("resolveType() = %v, want %v", err, ErrCircularDependency)
	}
	if !strings.Contains(err.Error(), "deeper than") {
		t.Errorf("resolveType() error %q does not mention the depth limit", err)
	}
}

// curGoroutineID depends on the runtime.Stack header format; pin it.
func TestCurGoroutineID(t *testing.T) {
	t.Parallel()

	a, b := curGoroutineID(), curGoroutineID()
	if a == 0 || a != b {
		t.Fatalf("curGoroutineID() = %d then %d, want the same non-zero ID", a, b)
	}
	other := make(chan uint64)
	go func() { other <- curGoroutineID() }()
	if o := <-other; o == 0 || o == a {
		t.Errorf("another goroutine's ID = %d, want non-zero and different from %d", o, a)
	}
}
