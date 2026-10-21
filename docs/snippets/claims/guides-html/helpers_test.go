package guideshtml_test

import (
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/template/memory"
	"github.com/gasmod/gas/ui"
)

// logRecord is one log event a captureLogger saw sent.
type logRecord struct {
	level  string
	msg    string
	fields map[string]string
}

// captureLogger is a gas.Logger that records every sent event, so a test can
// assert what a service logged.
type captureLogger struct {
	mu   *sync.Mutex
	recs *[]logRecord
}

func newCaptureLogger() *captureLogger {
	return &captureLogger{mu: &sync.Mutex{}, recs: &[]logRecord{}}
}

func (l *captureLogger) event(level, msg string) gas.LogEvent {
	return &captureEvent{l: l, rec: logRecord{level: level, msg: msg, fields: map[string]string{}}}
}

func (l *captureLogger) Trace(msg string) gas.LogEvent { return l.event("trace", msg) }
func (l *captureLogger) Debug(msg string) gas.LogEvent { return l.event("debug", msg) }
func (l *captureLogger) Info(msg string) gas.LogEvent  { return l.event("info", msg) }
func (l *captureLogger) Warn(msg string) gas.LogEvent  { return l.event("warn", msg) }
func (l *captureLogger) Error(msg string) gas.LogEvent { return l.event("error", msg) }
func (l *captureLogger) With() gas.LoggerContext       { return &captureContext{l: l} }
func (l *captureLogger) Flush()                        {}

func (l *captureLogger) records() []logRecord {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]logRecord(nil), *l.recs...)
}

type captureEvent struct {
	l   *captureLogger
	rec logRecord
}

func (e *captureEvent) set(k string, v any) gas.LogEvent {
	e.rec.fields[k] = fmt.Sprint(v)
	return e
}

func (e *captureEvent) Str(k, v string) gas.LogEvent                    { return e.set(k, v) }
func (e *captureEvent) Int(k string, v int) gas.LogEvent                { return e.set(k, v) }
func (e *captureEvent) Int64(k string, v int64) gas.LogEvent            { return e.set(k, v) }
func (e *captureEvent) Float64(k string, v float64) gas.LogEvent        { return e.set(k, v) }
func (e *captureEvent) Bool(k string, v bool) gas.LogEvent              { return e.set(k, v) }
func (e *captureEvent) Err(k string, v error) gas.LogEvent              { return e.set(k, v) }
func (e *captureEvent) Duration(k string, v time.Duration) gas.LogEvent { return e.set(k, v) }
func (e *captureEvent) Any(k string, v any) gas.LogEvent                { return e.set(k, v) }
func (e *captureEvent) Send() {
	e.l.mu.Lock()
	defer e.l.mu.Unlock()
	*e.l.recs = append(*e.l.recs, e.rec)
}

// captureContext drops persistent fields and hands back the same recorder.
type captureContext struct{ l *captureLogger }

func (c *captureContext) Str(string, string) gas.LoggerContext             { return c }
func (c *captureContext) Int(string, int) gas.LoggerContext                { return c }
func (c *captureContext) Int64(string, int64) gas.LoggerContext            { return c }
func (c *captureContext) Float64(string, float64) gas.LoggerContext        { return c }
func (c *captureContext) Bool(string, bool) gas.LoggerContext              { return c }
func (c *captureContext) Err(string, error) gas.LoggerContext              { return c }
func (c *captureContext) Duration(string, time.Duration) gas.LoggerContext { return c }
func (c *captureContext) Any(string, any) gas.LoggerContext                { return c }
func (c *captureContext) Logger() gas.Logger                               { return c.l }

// startApp builds an App from opts, starts it, and stops it at cleanup.
func startApp(t *testing.T, opts ...gas.Option) *gas.App {
	t.Helper()
	app := gas.NewApp(opts...)
	if err := app.Start(context.Background()); err != nil {
		t.Fatalf("app.Start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	return app
}

// uiApp starts an App wired the way the page's wiring snippet is: a config
// provider, a logger, a gas.TemplateProvider, and ui.New registered under
// gas.UIProvider. It returns the App and the resolved gas.UIProvider.
func uiApp(t *testing.T, store gas.TemplateProvider, log gas.Logger, extra []gas.Option, uiOpts ...ui.Option) (*gas.App, gas.UIProvider) {
	t.Helper()
	cfg := config.New()
	if log == nil {
		log = newCaptureLogger()
	}
	opts := []gas.Option{
		gas.WithServiceInstance[gas.ConfigProvider](cfg),
		gas.WithServiceInstance[gas.Logger](log),
		gas.WithServiceInstance[gas.TemplateProvider](store),
		gas.WithSingletonService[gas.UIProvider](ui.New[gas.TemplateProvider](uiOpts...)),
	}
	opts = append(opts, extra...)
	app := startApp(t, opts...)
	p, err := app.ServiceContainer().Resolve[gas.UIProvider]()
	if err != nil {
		t.Fatalf("resolve gas.UIProvider: %v", err)
	}
	return app, p
}

// emptyStaticFS is passed with ui.WithStaticFS where a test is not about
// static files, so startup does not depend on a ./static directory.
func emptyStaticFS() fs.FS { return fstest.MapFS{} }

// memStore returns a memory store holding the given name to content pairs.
func memStore(t *testing.T, files map[string]string) *memory.Store {
	t.Helper()
	s := memory.NewStore()
	for name, content := range files {
		if err := s.Register(context.Background(), name, []byte(content)); err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
	}
	return s
}

// render calls fn against a recorder and returns the status and body.
func render(t *testing.T, fn func(w http.ResponseWriter) error) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	if err := fn(rec); err != nil {
		t.Fatalf("render: %v", err)
	}
	return rec.Code, rec.Body.String()
}

// get sends a GET for path through the App's handler.
func get(t *testing.T, app *gas.App, path string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec.Code, rec.Body.String()
}
