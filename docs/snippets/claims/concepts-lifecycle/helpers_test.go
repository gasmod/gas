package conceptslifecycle_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// recorder collects lifecycle events from several goroutines in the order
// they happen.
type recorder struct {
	mu     sync.Mutex
	events []string
}

func (r *recorder) add(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, fmt.Sprintf(format, args...))
}

func (r *recorder) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.events)
}

// only returns the recorded events that start with one of the prefixes.
func (r *recorder) only(prefixes ...string) []string {
	var out []string
	for _, e := range r.list() {
		for _, p := range prefixes {
			if strings.HasPrefix(e, p) {
				out = append(out, e)
				break
			}
		}
	}
	return out
}

// svc is a Service whose type is distinct per kind K, so several of them can
// be registered in one container. Its Name is the kind's type name.
type svc[K any] struct {
	rec     *recorder
	onInit  func(ctx context.Context) error
	onClose func(ctx context.Context) error

	mu          sync.Mutex
	initialized bool
	initCount   int
	closeCount  int
}

func kindName[K any]() string { return reflect.TypeFor[K]().Name() }

func (s *svc[K]) Name() string { return kindName[K]() }

func (s *svc[K]) Init(ctx context.Context) error {
	if s.rec != nil {
		s.rec.add("init %s", s.Name())
	}
	s.mu.Lock()
	s.initCount++
	s.mu.Unlock()
	if s.onInit != nil {
		if err := s.onInit(ctx); err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.initialized = true
	s.mu.Unlock()
	return nil
}

func (s *svc[K]) Close(ctx context.Context) error {
	if s.rec != nil {
		s.rec.add("close %s", s.Name())
	}
	s.mu.Lock()
	s.closeCount++
	s.mu.Unlock()
	if s.onClose != nil {
		return s.onClose(ctx)
	}
	return nil
}

func (s *svc[K]) isInitialized() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.initialized
}

func (s *svc[K]) inits() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.initCount
}

func (s *svc[K]) closes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closeCount
}

type (
	kA    struct{}
	kB    struct{}
	kC    struct{}
	kInst struct{}
	kSc   struct{}
)

type ctxKey struct{}

// freePort returns a TCP port that was free a moment ago.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	return port
}

// waitFor polls cond until it holds or the timeout passes.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

// sameHandler compares two values that may hold uncomparable dynamic types.
func sameHandler(a, b any) (same bool) {
	defer func() {
		if recover() != nil {
			same = reflect.ValueOf(a).Pointer() == reflect.ValueOf(b).Pointer()
		}
	}()
	return a == b
}

var errBoom = errors.New("boom")
