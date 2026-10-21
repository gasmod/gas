package coreservices_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config/configtest"
	"github.com/gasmod/gas/docs/snippets/claims/internal/testport"
)

// recorder collects events from several goroutines in the order they happen.
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

// svc is a Service whose type is distinct per kind K, so several of them can
// be registered in one container. Its Name is the kind's type name unless
// name is set.
type svc[K any] struct {
	rec     *recorder
	name    string
	onInit  func(ctx context.Context) error
	onClose func(ctx context.Context) error

	mu         sync.Mutex
	initCount  int
	closeCount int
}

func (s *svc[K]) Name() string {
	if s.name != "" {
		return s.name
	}
	return reflect.TypeFor[K]().Name()
}

func (s *svc[K]) Init(ctx context.Context) error {
	if s.rec != nil {
		s.rec.add("init %s", reflect.TypeFor[K]().Name())
	}
	s.mu.Lock()
	s.initCount++
	s.mu.Unlock()
	if s.onInit != nil {
		return s.onInit(ctx)
	}
	return nil
}

func (s *svc[K]) Close(ctx context.Context) error {
	if s.rec != nil {
		s.rec.add("close %s", reflect.TypeFor[K]().Name())
	}
	s.mu.Lock()
	s.closeCount++
	s.mu.Unlock()
	if s.onClose != nil {
		return s.onClose(ctx)
	}
	return nil
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
	kA struct{}
	kB struct{}
	kC struct{}
	kD struct{}
)

func mustConfig(t *testing.T, values map[string]any) gas.ConfigProvider {
	t.Helper()
	cfg, err := configtest.NewMockConfigWithValues(values)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	return cfg
}

// serverConfig is a config provider that binds the server to 127.0.0.1 on
// port.
func serverConfig(t *testing.T, port int) gas.ConfigProvider {
	t.Helper()
	return mustConfig(t, map[string]any{"server": map[string]any{"host": "127.0.0.1", "port": port}})
}

// freePort returns a port reserved for this test process; see
// testport.Free for why a port-0 listener is not enough.
func freePort(t *testing.T) int {
	t.Helper()
	return testport.Free(t)
}

// accepting reports whether something accepts TCP connections on port.
func accepting(port int) bool {
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

func get(h http.Handler, path string) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
	return rr
}

// mustPanic runs fn and fails unless it panics.
func mustPanic(t *testing.T, what string, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Errorf("%s: no panic", what)
		}
	}()
	fn()
}

// recovered runs fn and returns what it panicked with, or nil.
func recovered(fn func()) (r any) {
	defer func() { r = recover() }()
	fn()
	return nil
}
