package corerequestscopes_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gasmod/gas"
	gaslog "github.com/gasmod/gas/log"
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

// tracker counts what happens to the instances of one scoped type and keeps
// the contexts their Init and Close received.
type tracker struct {
	built, inits, closes atomic.Int32

	mu       sync.Mutex
	initCtx  context.Context
	closeCtx context.Context
	closeErr error
	rec      *recorder
}

func (t *tracker) ctxs() (initCtx, closeCtx context.Context) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.initCtx, t.closeCtx
}

// scoped is a Service meant for scoped registration. Each K is its own
// registration type.
type scoped[K any] struct {
	tr *tracker
	id int32
}

func (s *scoped[K]) Name() string { return fmt.Sprintf("%T", *new(K)) }

func (s *scoped[K]) Init(ctx context.Context) error {
	s.tr.inits.Add(1)
	s.tr.mu.Lock()
	s.tr.initCtx = ctx
	s.tr.mu.Unlock()
	return nil
}

func (s *scoped[K]) Close(ctx context.Context) error {
	s.tr.closes.Add(1)
	s.tr.mu.Lock()
	s.tr.closeCtx = ctx
	err := s.tr.closeErr
	rec := s.tr.rec
	s.tr.mu.Unlock()
	if rec != nil {
		rec.add("close %s", s.Name())
	}
	return err
}

// newScoped returns a constructor for *scoped[K] that reports to tr.
func newScoped[K any](tr *tracker) func() *scoped[K] {
	return func() *scoped[K] { return &scoped[K]{tr: tr, id: tr.built.Add(1)} }
}

type (
	kA struct{}
	kB struct{}
)

type ctxKey struct{}

func start(t *testing.T, app *gas.App) {
	t.Helper()
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })
}

func serve(h http.Handler, method, path string) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(method, path, nil))
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

func (s *logSink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// find returns every captured record whose msg is msg.
func (s *logSink) find(t *testing.T, msg string) []map[string]any {
	t.Helper()
	var out []map[string]any
	sc := bufio.NewScanner(strings.NewReader(s.String()))
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("decode log line %q: %v", sc.Text(), err)
		}
		if m["msg"] == msg {
			out = append(out, m)
		}
	}
	return out
}

func newSink() (*logSink, gaslog.SlogLoggerCtor) {
	sink := &logSink{}
	l := slog.New(slog.NewJSONHandler(sink, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return sink, gaslog.NewSlogLogger(gaslog.WithSlogInstance(l))
}
