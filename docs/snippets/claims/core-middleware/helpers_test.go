package coremiddleware_test

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
	"testing"

	"github.com/gasmod/gas"
	gaslog "github.com/gasmod/gas/log"
)

// svc is a singleton service whose Init runs init. Each K is its own
// registration type, so tests can close and restart one by type.
type svc[K any] struct {
	router *gas.Router
	init   func(s *svc[K])
	name   string
}

func (s *svc[K]) Name() string { return s.name }

func (s *svc[K]) Init(context.Context) error {
	if s.init != nil {
		s.init(s)
	}
	return nil
}

func (s *svc[K]) Close(context.Context) error { return nil }

// withSvc registers a *svc[K] named name that takes the router from the
// container and runs init during Init.
func withSvc[K any](name string, init func(s *svc[K])) gas.WorkerOption {
	return gas.WithSingletonService[*svc[K]](func(r *gas.Router) *svc[K] {
		return &svc[K]{router: r, init: init, name: name}
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

// guard rejects every request with 401, standing in for an auth check.
func guard(http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
}

func tags(rr *httptest.ResponseRecorder) []string { return rr.Header().Values("X-Mw") }

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
