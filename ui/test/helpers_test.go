package ui_test

import (
	"bufio"
	"context"
	"errors"
	"maps"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/template/memory"
	"github.com/gasmod/gas/ui"
)

// The errors the tests' fakes return.
var (
	errStoreDown  = errors.New("store down")
	errBoom       = errors.New("boom")
	errClientGone = errors.New("client gone")
	errDisk       = errors.New("disk on fire")
	errSecretFail = errors.New("db password=hunter2 failed")
)

// files maps template names to their content.
type files = map[string]string

// newStore returns a memory store holding fs.
func newStore(tb testing.TB, fs files) *memory.Store {
	tb.Helper()

	return newStoreExt(tb, fs, ui.DefaultExtension)
}

// newStoreExt returns a memory store of the extensions exts holding fs.
func newStoreExt(tb testing.TB, fs files, exts ...string) *memory.Store {
	tb.Helper()

	st, err := memory.NewStore(memory.WithExtensions(exts...))()
	require.NoError(tb, err)

	for _, name := range slices.Sorted(maps.Keys(fs)) {
		require.NoError(tb, st.Register(tb.Context(), name, []byte(fs[name])))
	}

	return st
}

// cfgWith returns DefaultConfig without a static directory, changed by
// edit.
func cfgWith(edit func(*ui.Settings)) *ui.Config {
	cfg := ui.DefaultConfig()

	cfg.UI.StaticDir = ""

	if edit != nil {
		edit(&cfg.UI)
	}

	return cfg
}

// build returns a service over store, with no router, built with opts
// and a recording logger, not initialized.
func build(tb testing.TB, store gas.TemplateProvider, opts ...ui.Option) (*ui.Service, *recorder) {
	tb.Helper()

	log := newRecorder()

	s, err := ui.New[gas.TemplateProvider](opts...)(store, nil, nil, log)
	require.NoError(tb, err)

	return s, log
}

// initialized returns a service over a memory store holding fs, with the
// settings of cfgWith(edit) and opts, initialized and closed when the test
// ends.
func initialized(t *testing.T, fs files, edit func(*ui.Settings), opts ...ui.Option) *ui.Service {
	t.Helper()

	s, _ := initializedLog(t, newStore(t, fs), edit, opts...)

	return s
}

// initializedLog returns a service over store, with the settings of
// cfgWith(edit) and opts, initialized and closed when the test ends, and
// its logger.
func initializedLog(
	tb testing.TB,
	store gas.TemplateProvider,
	edit func(*ui.Settings),
	opts ...ui.Option,
) (*ui.Service, *recorder) {
	tb.Helper()

	s, log := build(tb, store, append([]ui.Option{ui.WithConfig(cfgWith(edit))}, opts...)...)
	require.NoError(tb, s.Init(tb.Context()))
	tb.Cleanup(func() { require.NoError(tb, s.Close(context.Background())) })

	return s, log
}

// render renders name with Render and returns the recorder and the error.
func render(
	t *testing.T,
	p ui.Provider,
	name string,
	data any,
	opts ...ui.RenderOption,
) (*httptest.ResponseRecorder, error) {
	t.Helper()

	w := httptest.NewRecorder()

	return w, p.Render(t.Context(), w, name, data, opts...)
}

// fragment renders name with RenderFragment and returns the recorder and
// the error.
func fragment(
	t *testing.T,
	p ui.Provider,
	name string,
	data any,
	opts ...ui.RenderOption,
) (*httptest.ResponseRecorder, error) {
	t.Helper()

	w := httptest.NewRecorder()

	return w, p.RenderFragment(t.Context(), w, name, data, opts...)
}

// body renders name with Render, requires no error, and returns the body.
func body(t *testing.T, p ui.Provider, name string, data any, opts ...ui.RenderOption) string {
	t.Helper()

	w, err := render(t, p, name, data, opts...)
	require.NoError(t, err)

	return w.Body.String()
}

// fragmentBody renders name with RenderFragment, requires no error, and
// returns the body.
func fragmentBody(
	t *testing.T,
	p ui.Provider,
	name string,
	data any,
	opts ...ui.RenderOption,
) string {
	t.Helper()

	w, err := fragment(t, p, name, data, opts...)
	require.NoError(t, err)

	return w.Body.String()
}

// docPath is the package's doc.go, from the tests' directory.
const docPath = "../doc.go"

// docLines returns the code lines of the doc comments in the package's
// doc.go, without the comment marker and the tab, that start with prefix.
func docLines(t *testing.T, prefix string) []string {
	t.Helper()

	f, err := os.Open(docPath)
	require.NoError(t, err)

	defer func() { require.NoError(t, f.Close()) }()

	var out []string

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		code, ok := strings.CutPrefix(sc.Text(), "//\t")
		if ok && strings.HasPrefix(code, prefix) {
			out = append(out, code)
		}
	}

	require.NoError(t, sc.Err())

	return out
}

// entry is one log event a recorder received.
type entry struct {
	// fields are the event's fields, the logger's own included.
	fields map[string]any
	// level is `info`, `warn` and so on.
	level string
	// msg is the event's message.
	msg string
}

// recorder is a gas.Logger that records every event it sends. It is safe
// for concurrent use.
type recorder struct {
	// fields are the fields of the logger, from With.
	fields map[string]any
	// shared holds the events of the recorder and the loggers derived
	// from it.
	shared *recorded
}

// recorded is the event list a recorder and its derived loggers share.
type recorded struct {
	// entries are the events sent, in order.
	entries []entry
	// mu guards entries.
	mu sync.Mutex
}

var _ gas.Logger = (*recorder)(nil)

// newRecorder returns an empty recorder.
func newRecorder() *recorder { return &recorder{shared: &recorded{}} }

// Trace starts a trace event.
func (r *recorder) Trace(msg string) gas.LogEvent { return r.event("trace", msg) }

// Debug starts a debug event.
func (r *recorder) Debug(msg string) gas.LogEvent { return r.event("debug", msg) }

// Info starts an info event.
func (r *recorder) Info(msg string) gas.LogEvent { return r.event("info", msg) }

// Warn starts a warn event.
func (r *recorder) Warn(msg string) gas.LogEvent { return r.event("warn", msg) }

// Error starts an error event.
func (r *recorder) Error(msg string) gas.LogEvent { return r.event("error", msg) }

// With returns a context that derives a logger sharing r's events.
func (r *recorder) With() gas.LoggerContext {
	fields := map[string]any{}
	maps.Copy(fields, r.fields)

	return &recContext{parent: r, fields: fields}
}

// Flush does nothing.
func (*recorder) Flush() {}

// event returns an event of level and msg, with the logger's fields.
func (r *recorder) event(level, msg string) gas.LogEvent {
	fields := map[string]any{}
	maps.Copy(fields, r.fields)

	return &recEvent{r: r, e: entry{level: level, msg: msg, fields: fields}}
}

// entries returns a copy of the events sent so far.
func (r *recorder) entries() []entry {
	r.shared.mu.Lock()
	defer r.shared.mu.Unlock()

	return slices.Clone(r.shared.entries)
}

// errorMessages returns the messages of the error-level events sent so
// far.
func (r *recorder) errorMessages() []string {
	var out []string

	for _, e := range r.entries() {
		if e.level == "error" {
			out = append(out, e.msg)
		}
	}

	return out
}

// recEvent is an event of a recorder being built.
type recEvent struct {
	// r is the recorder that sends it.
	r *recorder
	// e is the event.
	e entry
}

// Str adds a string field.
func (e *recEvent) Str(k, v string) gas.LogEvent { return e.set(k, v) }

// Int adds an int field.
func (e *recEvent) Int(k string, v int) gas.LogEvent { return e.set(k, v) }

// Int64 adds an int64 field.
func (e *recEvent) Int64(k string, v int64) gas.LogEvent { return e.set(k, v) }

// Float64 adds a float64 field.
func (e *recEvent) Float64(k string, v float64) gas.LogEvent { return e.set(k, v) }

// Bool adds a bool field.
func (e *recEvent) Bool(k string, v bool) gas.LogEvent { return e.set(k, v) }

// Err adds an error field.
func (e *recEvent) Err(k string, v error) gas.LogEvent { return e.set(k, v) }

// Duration adds a duration field.
func (e *recEvent) Duration(k string, v time.Duration) gas.LogEvent { return e.set(k, v) }

// Any adds a field of any type.
func (e *recEvent) Any(k string, v any) gas.LogEvent { return e.set(k, v) }

// Send records the event.
func (e *recEvent) Send() {
	e.r.shared.mu.Lock()

	e.r.shared.entries = append(e.r.shared.entries, e.e)
	e.r.shared.mu.Unlock()
}

// set adds a field.
func (e *recEvent) set(k string, v any) gas.LogEvent {
	e.e.fields[k] = v

	return e
}

// recContext builds a logger derived from a recorder.
type recContext struct {
	// parent is the recorder derived from.
	parent *recorder
	// fields are the fields of the derived logger.
	fields map[string]any
}

// Str adds a string field.
func (c *recContext) Str(k, v string) gas.LoggerContext { return c.set(k, v) }

// Int adds an int field.
func (c *recContext) Int(k string, v int) gas.LoggerContext { return c.set(k, v) }

// Int64 adds an int64 field.
func (c *recContext) Int64(k string, v int64) gas.LoggerContext { return c.set(k, v) }

// Float64 adds a float64 field.
func (c *recContext) Float64(k string, v float64) gas.LoggerContext { return c.set(k, v) }

// Bool adds a bool field.
func (c *recContext) Bool(k string, v bool) gas.LoggerContext { return c.set(k, v) }

// Err adds an error field.
func (c *recContext) Err(k string, v error) gas.LoggerContext { return c.set(k, v) }

// Duration adds a duration field.
func (c *recContext) Duration(k string, v time.Duration) gas.LoggerContext {
	return c.set(k, v)
}

// Any adds a field of any type.
func (c *recContext) Any(k string, v any) gas.LoggerContext { return c.set(k, v) }

// Logger returns the derived logger, which shares the parent's events.
func (c *recContext) Logger() gas.Logger {
	return &recorder{fields: c.fields, shared: c.parent.shared}
}

// set adds a field.
func (c *recContext) set(k string, v any) gas.LoggerContext {
	c.fields[k] = v

	return c
}
