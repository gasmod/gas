package productiondeployment_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"reflect"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"testing/fstest"
	"time"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/config/providers"
	"github.com/gasmod/gas/docs/snippets/claims/internal/testport"
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

// svc is a Service distinct per kind K. Its hooks are optional.
type svc[K any] struct {
	rec     *recorder
	onInit  func(ctx context.Context) error
	onClose func(ctx context.Context) error
}

func (s *svc[K]) Name() string { return reflect.TypeFor[K]().Name() }

func (s *svc[K]) Init(ctx context.Context) error {
	if s.rec != nil {
		s.rec.add("init %s", s.Name())
	}
	if s.onInit != nil {
		return s.onInit(ctx)
	}
	return nil
}

func (s *svc[K]) Close(ctx context.Context) error {
	if s.rec != nil {
		s.rec.add("close %s", s.Name())
	}
	if s.onClose != nil {
		return s.onClose(ctx)
	}
	return nil
}

type (
	kA struct{}
	kB struct{}
	kC struct{}
	kD struct{}
)

// cfgFrom returns a loaded config whose only provider holds values, plus
// any extensions.
func cfgFrom(t *testing.T, values map[string]any, exts ...config.Extension) *config.Config {
	t.Helper()
	raw, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	opts := []config.Option{config.WithProvider(providers.NewJSONProvider(
		providers.WithJSONFilePath("config.json"),
		providers.WithJSONFileFS(fstest.MapFS{"config.json": {Data: raw}}),
	))}
	for _, e := range exts {
		opts = append(opts, config.WithExtension(e))
	}
	cfg := config.New(opts...)
	if err := cfg.Load(); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// serverValues are config values that put the server on 127.0.0.1:port,
// with extra server keys merged in.
func serverValues(port int, extra map[string]any) map[string]any {
	server := map[string]any{"host": "127.0.0.1", "port": port}
	for k, v := range extra {
		server[k] = v
	}
	return map[string]any{"server": server}
}

// serverCfg is a config that puts the server on 127.0.0.1:port.
func serverCfg(t *testing.T, port int, extra map[string]any) *config.Config {
	t.Helper()
	return cfgFrom(t, serverValues(port, extra))
}

// logSink captures the JSON lines a slog-backed gas.Logger writes.
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
	text := s.buf.String()
	s.mu.Unlock()
	var out []map[string]any
	sc := bufio.NewScanner(strings.NewReader(text))
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

// find returns the records with msg whose attributes include attrs.
func (s *logSink) find(t *testing.T, msg string, attrs map[string]string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, r := range s.records(t) {
		if r["msg"] == msg && hasAttrs(r, attrs) {
			out = append(out, r)
		}
	}
	return out
}

// index returns the position of the first record with msg and attrs, or -1.
// A msg ending in "*" matches as a prefix.
func (s *logSink) index(t *testing.T, msg string, attrs map[string]string) int {
	t.Helper()
	for i, r := range s.records(t) {
		m, _ := r["msg"].(string)
		match := m == msg
		if p, ok := strings.CutSuffix(msg, "*"); ok {
			match = strings.HasPrefix(m, p)
		}
		if match && hasAttrs(r, attrs) {
			return i
		}
	}
	return -1
}

func hasAttrs(r map[string]any, attrs map[string]string) bool {
	for k, v := range attrs {
		if fmt.Sprint(r[k]) != v {
			return false
		}
	}
	return true
}

// logger returns a sink and a gas.Logger option writing JSON lines to it.
func logger() (*logSink, gas.WorkerOption) {
	sink := &logSink{}
	l := slog.New(slog.NewJSONHandler(sink, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return sink, gas.WithSingletonService[gas.Logger](gaslog.NewSlogLogger(gaslog.WithSlogInstance(l)))
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
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}

// guardSignals keeps a signal that lands before Run installs its handler
// from killing the test process.
func guardSignals(t *testing.T) {
	t.Helper()
	requireSignals(t)
	guard := make(chan os.Signal, 8)
	signal.Notify(guard, syscall.SIGINT, syscall.SIGTERM)
	t.Cleanup(func() { signal.Stop(guard) })
}

// running is a Run call in the background.
type running struct {
	t    *testing.T
	done chan error
}

// runInBackground calls run in the background, after guarding signals.
func runInBackground(t *testing.T, run func() error) *running {
	t.Helper()
	guardSignals(t)
	r := &running{t: t, done: make(chan error, 1)}
	go func() { r.done <- run() }()
	return r
}

// runApp calls a.Run in the background and waits until it serves on port.
func runApp(t *testing.T, a *gas.App, port int) *running {
	t.Helper()
	r := runInBackground(t, a.Run)
	if !waitFor(10*time.Second, func() bool { return accepting(port) }) {
		select {
		case err := <-r.done:
			t.Fatalf("Run returned before serving: %v", err)
		default:
			t.Fatal("Run did not start serving")
		}
	}
	return r
}

// stop sends sig until Run returns, and returns Run's error.
func (r *running) stop(sig syscall.Signal) error {
	r.t.Helper()
	deadline := time.After(15 * time.Second)
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	if err := sendSignal(sig); err != nil {
		r.t.Fatal(err)
	}
	for {
		select {
		case err := <-r.done:
			return err
		case <-tick.C:
			if err := sendSignal(sig); err != nil {
				r.t.Fatal(err)
			}
		case <-deadline:
			r.t.Fatalf("Run did not return after %v", sig)
			return nil
		}
	}
}

// serveApp starts a, serves it in the background on port, and returns the
// channel Serve's result arrives on.
func serveApp(t *testing.T, a *gas.App, port int) chan error {
	t.Helper()
	if err := a.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- a.Serve() }()
	if !waitFor(10*time.Second, func() bool { return accepting(port) }) {
		t.Fatal("Serve did not start listening")
	}
	return done
}
