package coreevents_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
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
// is its own registration type.
type svc[K any] struct {
	name    string
	bus     *gas.EventBus
	onInit  func(ctx context.Context, s *svc[K]) error
	onClose func(ctx context.Context, s *svc[K]) error
	closes  atomic.Int32
}

func (s *svc[K]) Name() string { return s.name }

func (s *svc[K]) Init(ctx context.Context) error {
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

// withSvc registers s as *svc[K], handing it the bus from its constructor.
func withSvc[K any](s *svc[K]) gas.WorkerOption {
	return gas.WithSingletonService[*svc[K]](func(b *gas.EventBus) *svc[K] {
		s.bus = b
		return s
	})
}

type (
	kA struct{}
	kB struct{}
)

type userCreatedPayload struct{ Email string }

type userCreated struct{ gas.Event[userCreatedPayload] }

// userInvited has the same payload type as userCreated.
type userInvited struct{ gas.Event[userCreatedPayload] }

// list is a goroutine-safe string slice.
type list struct {
	mu    sync.Mutex
	items []string
}

func (l *list) add(s string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.items = append(l.items, s)
}

func (l *list) get() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.items...)
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

// logSink captures JSON log lines.
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

func newSink() (*logSink, gaslog.SlogLoggerCtor) {
	sink := &logSink{}
	l := slog.New(slog.NewJSONHandler(sink, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return sink, gaslog.NewSlogLogger(gaslog.WithSlogInstance(l))
}

// recordsWithMsg decodes the JSON lines in raw and returns those whose msg
// is msg. Lines that are not JSON are skipped.
func recordsWithMsg(raw, msg string) []map[string]any {
	var out []map[string]any
	sc := bufio.NewScanner(strings.NewReader(raw))
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for sc.Scan() {
		var m map[string]any
		if json.Unmarshal(sc.Bytes(), &m) == nil && m["msg"] == msg {
			out = append(out, m)
		}
	}
	return out
}

// childEnv names the scenario a re-executed test binary runs.
const childEnv = "CORE_EVENTS_CHILD"

// runChild re-runs this test binary with only TestChild, in scenario, and
// returns its stdout and stderr. Scenarios that write to os.Stderr or set
// slog.Default run there, so they do not race with other tests.
func runChild(t *testing.T, scenario string) (stdout, stderr string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), filepath.Clean(exe), "-test.run=^TestChild$", "-test.count=1")
	cmd.Env = append(os.Environ(), childEnv+"="+scenario)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("child %s: %v\nstdout:\n%s\nstderr:\n%s", scenario, err, out.String(), errb.String())
	}
	return out.String(), errb.String()
}
