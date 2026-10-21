package modulesui_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config/configtest"
	gaslog "github.com/gasmod/gas/log"
	"github.com/gasmod/gas/template/memory"
	"github.com/gasmod/gas/ui"
)

// countingStore wraps a memory store, counts reads, and fails the
// operations a test asks it to.
type countingStore struct {
	*memory.Store
	lists   atomic.Int32
	gets    atomic.Int32
	failMu  sync.Mutex
	failGet string
	failAll bool
}

func newStore(t *testing.T, templates map[string]string) *countingStore {
	t.Helper()
	s := &countingStore{Store: memory.NewStore()}
	for name, body := range templates {
		s.put(t, name, body)
	}
	return s
}

func (s *countingStore) put(t *testing.T, name, body string) {
	t.Helper()
	if err := s.Register(context.Background(), name, []byte(body)); err != nil {
		t.Fatal(err)
	}
}

var errStore = errors.New("store down")

func (s *countingStore) List(ctx context.Context) ([]string, error) {
	s.lists.Add(1)
	s.failMu.Lock()
	fail := s.failAll
	s.failMu.Unlock()
	if fail {
		return nil, errStore
	}
	return s.Store.List(ctx)
}

func (s *countingStore) Get(ctx context.Context, name string) ([]byte, error) {
	s.gets.Add(1)
	s.failMu.Lock()
	fail := s.failGet == name
	s.failMu.Unlock()
	if fail {
		return nil, errStore
	}
	return s.Store.Get(ctx, name)
}

func (s *countingStore) reads() int32 { return s.lists.Load() + s.gets.Load() }

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

func (s *logSink) records(t *testing.T) []map[string]any {
	t.Helper()
	s.mu.Lock()
	text := s.buf.String()
	s.mu.Unlock()
	var out []map[string]any
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("decode %q: %v", sc.Text(), err)
		}
		out = append(out, m)
	}
	return out
}

// warnings returns the messages logged at WARN.
func (s *logSink) warnings(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, r := range s.records(t) {
		if r["level"] == "WARN" {
			msg, _ := r["msg"].(string)
			out = append(out, msg)
		}
	}
	return out
}

// sinkLogger is a gas.Logger writing JSON lines to sink at debug level.
func sinkLogger(sink *logSink) gas.Logger {
	l, _ := gaslog.NewSlogLogger(gaslog.WithSlogInstance(slog.New(slog.NewJSONHandler(sink, &slog.HandlerOptions{Level: slog.LevelDebug}))))()
	return l
}

// uiApp is a started app with a ui service over store.
type uiApp struct {
	app  *gas.App
	ui   gas.UIProvider
	svc  *ui.Service
	logs *logSink
}

type appOpts struct {
	values map[string]any
	cfg    gas.ConfigProvider
	ui     []ui.Option
	extra  []gas.Option
}

func startUI(t *testing.T, store gas.TemplateProvider, o appOpts) *uiApp {
	t.Helper()
	a, err := buildUI(t, store, o)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	return a
}

func buildUI(t *testing.T, store gas.TemplateProvider, o appOpts) (*uiApp, error) {
	t.Helper()
	values := o.values
	if values == nil {
		values = map[string]any{}
	}
	cfg := o.cfg
	if cfg == nil {
		mock, err := configtest.NewMockConfigWithValues(values)
		if err != nil {
			t.Fatal(err)
		}
		cfg = mock
	}
	sink := &logSink{}
	logger := gaslog.NewSlogLogger(gaslog.WithSlogInstance(slog.New(slog.NewJSONHandler(sink, &slog.HandlerOptions{Level: slog.LevelDebug}))))
	uiOpts := o.ui
	if uiOpts == nil {
		uiOpts = []ui.Option{ui.WithStaticFS(fstest.MapFS{})}
	}
	opts := append([]gas.Option{
		gas.WithServiceInstance[gas.ConfigProvider](cfg),
		gas.WithSingletonService[gas.Logger](logger),
		gas.WithServiceInstance[gas.TemplateProvider](store),
		gas.WithSingletonService[gas.UIProvider](ui.New[gas.TemplateProvider](uiOpts...)),
	}, o.extra...)
	app := gas.NewApp(opts...)
	if err := app.Start(t.Context()); err != nil {
		return nil, err
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	provider := app.ServiceContainer().MustResolve[gas.UIProvider]()
	svc, _ := provider.(*ui.Service)
	return &uiApp{app: app, ui: provider, svc: svc, logs: sink}, nil
}

// layoutStore is a store with the page's base layout and a few pages.
func layoutStore(t *testing.T) *countingStore {
	t.Helper()
	return newStore(t, map[string]string{
		"layouts/base.html": `{{define "base"}}<html>{{block "content" .}}{{end}}</html>{{end}}`,
		"partials/nav.html": `{{define "nav"}}<nav>{{.}}</nav>{{end}}`,
		"home.html":         `{{define "content"}}home {{template "nav" "N"}}{{end}}`,
		"notes/list.html":   `{{define "content"}}notes{{end}}`,
		"404.html":          `{{define "content"}}missing{{end}}`,
		"broken/exec.html":  `{{define "content"}}{{index .Items 5}}{{end}}`,
	})
}
