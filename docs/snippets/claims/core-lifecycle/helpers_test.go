package corelifecycle_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config/configtest"
	"github.com/gasmod/gas/docs/snippets/claims/internal/testport"
	gaslog "github.com/gasmod/gas/log"
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

func (s *svc[K]) Name() string { return reflect.TypeFor[K]().Name() }

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
	kA  struct{}
	kB  struct{}
	kC  struct{}
	kSc struct{}
)

type ctxKey struct{}

var errBoom = errors.New("boom")

func mustConfig(t *testing.T, values map[string]any) gas.ConfigProvider {
	t.Helper()
	cfg, err := configtest.NewMockConfigWithValues(values)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	return cfg
}

// serverConfig is a config provider that binds the server to 127.0.0.1 on
// port, with any extra server keys.
func serverConfig(t *testing.T, port int, extra map[string]any) gas.ConfigProvider {
	t.Helper()
	server := map[string]any{"host": "127.0.0.1", "port": port}
	for k, v := range extra {
		server[k] = v
	}
	return mustConfig(t, map[string]any{"server": server})
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

// waitFor polls cond until it holds or the timeout passes.
func waitFor(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

// waitUp waits until GET base+path answers.
func waitUp(t *testing.T, url string) {
	t.Helper()
	if !waitFor(10*time.Second, func() bool {
		resp, err := http.Get(url)
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return true
	}) {
		t.Fatalf("%s never answered", url)
	}
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

// sameHandler compares two values that may hold uncomparable dynamic types.
func sameHandler(a, b any) (same bool) {
	defer func() {
		if recover() != nil {
			same = reflect.ValueOf(a).Pointer() == reflect.ValueOf(b).Pointer()
		}
	}()
	return a == b
}

// logSink captures JSON log lines written by a slog-backed gas.Logger.
type logSink struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *logSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *logSink) records(t *testing.T) []map[string]any {
	t.Helper()
	s.mu.Lock()
	raw := s.buf.String()
	s.mu.Unlock()
	var out []map[string]any
	sc := bufio.NewScanner(strings.NewReader(raw))
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("decode log line %q: %v", sc.Text(), err)
		}
		out = append(out, m)
	}
	return out
}

// indexOf returns the index of the first record whose msg is msg, or -1.
func indexOf(recs []map[string]any, msg string) int {
	return slices.IndexFunc(recs, func(r map[string]any) bool { return r["msg"] == msg })
}

// newSink returns a capturing sink and a gas.Logger constructor writing JSON
// lines to it at debug level and above.
func newSink() (*logSink, gaslog.SlogLoggerCtor) {
	sink := &logSink{}
	l := slog.New(slog.NewJSONHandler(sink, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return sink, gaslog.NewSlogLogger(gaslog.WithSlogInstance(l))
}
