package conceptsoverview_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config/configtest"
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

	mu         sync.Mutex
	initCount  int
	closeCount int
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
		return s.onInit(ctx)
	}
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
)

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

// records returns every captured line decoded as a JSON object.
func (s *logSink) records(t *testing.T) []map[string]any {
	t.Helper()
	var out []map[string]any
	sc := bufio.NewScanner(strings.NewReader(s.String()))
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

// find returns the captured records whose msg contains substr.
func (s *logSink) find(t *testing.T, substr string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, r := range s.records(t) {
		if msg, _ := r["msg"].(string); strings.Contains(msg, substr) {
			out = append(out, r)
		}
	}
	return out
}

// hasValue reports whether any attribute of rec, other than time and msg,
// renders as want.
func hasValue(rec map[string]any, want string) bool {
	for k, v := range rec {
		if k == "time" || k == "msg" {
			continue
		}
		if fmt.Sprint(v) == want {
			return true
		}
	}
	return false
}

// newSink returns a capturing sink and a gas.Logger constructor writing JSON
// lines to it at debug level and above.
func newSink() (*logSink, gaslog.SlogLoggerCtor) {
	sink := &logSink{}
	l := slog.New(slog.NewJSONHandler(sink, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return sink, gaslog.NewSlogLogger(gaslog.WithSlogInstance(l))
}

func mustConfig(t *testing.T, values map[string]any) gas.ConfigProvider {
	t.Helper()
	cfg, err := configtest.NewMockConfigWithValues(values)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	return cfg
}
