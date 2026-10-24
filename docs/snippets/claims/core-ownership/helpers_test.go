package coreownership_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config/configtest"
	"github.com/gasmod/gas/docs/snippets/claims/internal/testport"
	gaslog "github.com/gasmod/gas/log"
)

// svc is a singleton service whose Init and Close run the given funcs. Each K
// is its own registration type, so tests can close and restart one by type.
type svc[K any] struct {
	name    string
	router  *gas.Router
	bus     *gas.EventBus
	onInit  func(ctx context.Context, s *svc[K]) error
	onClose func(ctx context.Context, s *svc[K]) error
	inits   atomic.Int32
	closes  atomic.Int32
}

func (s *svc[K]) Name() string { return s.name }

func (s *svc[K]) Init(ctx context.Context) error {
	s.inits.Add(1)
	if s.onInit != nil {
		return s.onInit(ctx, s)
	}
	return nil
}

func (s *svc[K]) Close(ctx context.Context) error {
	s.closes.Add(1)
	if s.onClose != nil {
		return s.onClose(ctx, s)
	}
	return nil
}

// withSvc registers s as *svc[K] in an App, giving it the router and bus.
func withSvc[K any](s *svc[K]) gas.WorkerOption {
	return gas.WithSingletonService[*svc[K]](func(r *gas.Router, b *gas.EventBus) *svc[K] {
		s.router, s.bus = r, b
		return s
	})
}

// withSvcAfter is withSvc for a service whose constructor depends on
// *svc[D], so its Init runs after that service's Init.
func withSvcAfter[K, D any](s *svc[K]) gas.WorkerOption {
	return gas.WithSingletonService[*svc[K]](func(r *gas.Router, b *gas.EventBus, _ *svc[D]) *svc[K] {
		s.router, s.bus = r, b
		return s
	})
}

// withWorkerSvc registers s as *svc[K] in a Worker, giving it the bus.
func withWorkerSvc[K any](s *svc[K]) gas.WorkerOption {
	return gas.WithSingletonService[*svc[K]](func(b *gas.EventBus) *svc[K] {
		s.bus = b
		return s
	})
}

type (
	kA struct{}
	kB struct{}
	kC struct{}
)

// owner is a plain Service used only as a route owner.
type owner string

func (o owner) Name() string              { return string(o) }
func (owner) Init(context.Context) error  { return nil }
func (owner) Close(context.Context) error { return nil }

func start(t *testing.T, app *gas.App) {
	t.Helper()
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })
}

func do(h http.Handler, method, path string) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(method, path, nil))
	return rr
}

func get(h http.Handler, path string) *httptest.ResponseRecorder {
	return do(h, http.MethodGet, path)
}

// text writes body with status 200.
func text(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }
}

func ok(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }

// tagger adds value to the X-Mw response header.
func tagger(value string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Add("X-Mw", value)
			next.ServeHTTP(w, r)
		})
	}
}

// freePort returns a port reserved for this test process; see
// testport.Free for why a port-0 listener is not enough.
func freePort(t *testing.T) int {
	t.Helper()
	return testport.Free(t)
}

func mustConfig(t *testing.T, values map[string]any) gas.ConfigProvider {
	t.Helper()
	cfg, err := configtest.NewMockConfigWithValues(values)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	return cfg
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

func newSink() (*logSink, gaslog.SlogLoggerCtor) {
	sink := &logSink{}
	l := slog.New(slog.NewJSONHandler(sink, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return sink, gaslog.NewSlogLogger(gaslog.WithSlogInstance(l))
}

func withMsg(recs []map[string]any, msg string) []map[string]any {
	var out []map[string]any
	for _, r := range recs {
		if m, _ := r["msg"].(string); m == msg {
			out = append(out, r)
		}
	}
	return out
}

func containingMsg(recs []map[string]any, sub string) []map[string]any {
	var out []map[string]any
	for _, r := range recs {
		if m, _ := r["msg"].(string); strings.Contains(m, sub) {
			out = append(out, r)
		}
	}
	return out
}
