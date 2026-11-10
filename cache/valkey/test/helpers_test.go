package valkey_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"maps"
	"net"
	"os"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/cache/valkey"
)

// TestMain runs the tests, then terminates the shared containers.
func TestMain(m *testing.M) {
	code := m.Run()

	if shared.terminate != nil {
		shared.terminate()
	}

	if sharedCluster.terminate != nil {
		sharedCluster.terminate()
	}

	os.Exit(code)
}

// prefixSeq keeps the key prefixes of the tests apart, across tests and
// repeated runs that share the container.
var prefixSeq atomic.Int64

// uniquePrefix returns a key prefix no other call returns.
func uniquePrefix() string {
	return "test:" + strconv.FormatInt(prefixSeq.Add(1), 10) + ":"
}

// config returns DefaultConfig pointed at addr, with a key prefix of its
// own.
func config(addr string) *valkey.Config {
	cfg := valkey.DefaultConfig()

	cfg.Cache.Addr = addr
	cfg.Cache.KeyPrefix = uniquePrefix()

	return cfg
}

// build returns a store built with opts and a recording logger, not
// initialized, closed when the test ends.
func build(t *testing.T, opts ...valkey.Option) (*valkey.Service, *recorder) {
	t.Helper()

	log := newRecorder()

	s, err := valkey.New(opts...)(nil, log)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close(context.Background())) })

	return s, log
}

// open returns a store with cfg and opts, initialized, closed when the
// test ends.
func open(t *testing.T, cfg *valkey.Config, opts ...valkey.Option) *valkey.Service {
	t.Helper()

	s, _ := build(t, append([]valkey.Option{valkey.WithConfig(cfg)}, opts...)...)
	require.NoError(t, s.Init(t.Context()))

	return s
}

// initErr returns the error of Init of a store built with opts.
func initErr(t *testing.T, opts ...valkey.Option) error {
	t.Helper()

	s, _ := build(t, opts...)

	return s.Init(t.Context())
}

// closedAddr returns a loopback address with nothing listening on it.
func closedAddr(t *testing.T) string {
	t.Helper()

	l, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	addr := l.Addr().String()
	require.NoError(t, l.Close())

	return addr
}

// blackhole returns the address of a loopback listener that accepts
// connections and never answers, closed when the test ends.
func blackhole(t *testing.T) string {
	t.Helper()

	l, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	var (
		mu    sync.Mutex
		conns []net.Conn
		wg    sync.WaitGroup
	)

	wg.Go(func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}

			mu.Lock()

			conns = append(conns, c)
			mu.Unlock()
		}
	})

	t.Cleanup(func() {
		_ = l.Close()

		wg.Wait()
		mu.Lock()
		defer mu.Unlock()

		for _, c := range conns {
			_ = c.Close()
		}
	})

	return l.Addr().String()
}

// proxy forwards TCP connections to a target, and can stall them. It is
// safe for concurrent use.
type proxy struct {
	// ln accepts the clients.
	ln net.Listener
	// target is the address forwarded to.
	target string
	// links are the open connection pairs.
	links []net.Conn
	// wg counts the proxy's goroutines.
	wg sync.WaitGroup
	// mu guards links.
	mu sync.Mutex
	// stalled makes every connection, open or new, swallow what the
	// client sends, so that no command gets an answer.
	stalled atomic.Bool
}

// newProxy returns a proxy to target, closed with its connections when
// the test ends.
func newProxy(t *testing.T, target string) *proxy {
	t.Helper()

	l, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	p := &proxy{ln: l, target: target}
	p.wg.Go(p.serve)

	t.Cleanup(func() {
		_ = l.Close()

		p.mu.Lock()

		for _, c := range p.links {
			_ = c.Close()
		}

		p.mu.Unlock()
		p.wg.Wait()
	})

	return p
}

// addr returns the proxy's address.
func (p *proxy) addr() string { return p.ln.Addr().String() }

// stall makes every connection swallow what the client sends.
func (p *proxy) stall() { p.stalled.Store(true) }

// cut closes every open connection and the listener, so that new ones
// are refused.
func (p *proxy) cut() {
	_ = p.ln.Close()

	p.mu.Lock()
	defer p.mu.Unlock()

	for _, c := range p.links {
		_ = c.Close()
	}
}

// serve accepts clients until the listener closes, and forwards each.
func (p *proxy) serve() {
	for {
		c, err := p.ln.Accept()
		if err != nil {
			return
		}

		s, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", p.target)
		if err != nil {
			_ = c.Close()

			continue
		}

		p.mu.Lock()

		p.links = append(p.links, c, s)
		p.mu.Unlock()

		p.wg.Go(func() { p.upstream(c, s) })
		p.wg.Go(func() {
			_, _ = io.Copy(c, s)
			_ = c.Close()
		})
	}
}

// upstream forwards what client c sends to server s, unless stalled.
func (p *proxy) upstream(c, s net.Conn) {
	buf := make([]byte, 32<<10)

	for {
		n, err := c.Read(buf)
		if n > 0 && !p.stalled.Load() {
			_, werr := s.Write(buf[:n])
			if werr != nil {
				return
			}
		}

		if err != nil {
			_ = s.Close()

			return
		}
	}
}

// Distinctive values, which no error or log may contain.
const (
	secretKey   = "session:zqxkey@example.com" //nolint:gosec // G101: test input, not a credential.
	secretValue = "zqxvalue"
)

// assertNoSecrets fails the test when text holds a distinctive value of
// the tests or any of extra, such as an address.
func assertNoSecrets(t *testing.T, text string, extra ...string) {
	t.Helper()

	for _, v := range append([]string{"zqx", "127.0.0.1", "localhost"}, extra...) {
		if strings.Contains(text, v) {
			t.Errorf("%q holds %q", text, v)
		}
	}
}

// port returns the port of addr.
func port(t *testing.T, addr string) string {
	t.Helper()

	_, p, err := net.SplitHostPort(addr)
	require.NoError(t, err)

	return p
}

// nop is a gas.Logger that drops everything.
var nop gas.Logger = &gas.NopLogger{}

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

// text returns the events sent so far as text, fields included.
func (r *recorder) text() string {
	var b strings.Builder

	for _, e := range r.entries() {
		fmt.Fprintf(&b, "%s %s %v\n", e.level, e.msg, e.fields)
	}

	return b.String()
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

// docLines returns the code lines of the doc comments in the Go file at
// path, without the comment marker and the tab, that start with prefix.
func docLines(t *testing.T, path, prefix string) []string {
	t.Helper()

	f, err := os.Open(path) //nolint:gosec // G304: the package's own doc.go.
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

// goroutinesSettle fails the test unless the number of goroutines comes
// back to at most before within a few seconds. It polls on the test's
// own goroutine, since a helper goroutine would count itself.
func goroutinesSettle(t *testing.T, before int) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)

	for runtime.NumGoroutine() > before {
		if time.Now().After(deadline) {
			t.Fatalf("goroutines: before %d, now %d", before, runtime.NumGoroutine())
		}

		time.Sleep(10 * time.Millisecond)
	}
}

// sharedKinds are the kinds whose values a copy shares with the
// original.
var sharedKinds = []reflect.Kind{
	reflect.Slice, reflect.Map, reflect.Pointer, reflect.Interface,
	reflect.Chan, reflect.Func, reflect.UnsafePointer,
}

// assertNoReferences fails the test when typ, named name, holds a value
// of one of sharedKinds: a slice, map, pointer, interface, channel or
// function, which a copy of a value of typ would share with the original.
func assertNoReferences(t *testing.T, typ reflect.Type, name string) {
	t.Helper()

	switch kind := typ.Kind(); {
	case kind == reflect.Struct:
		for f := range typ.Fields() {
			assertNoReferences(t, f.Type, name+"."+f.Name)
		}
	case kind == reflect.Array:
		assertNoReferences(t, typ.Elem(), name+"[]")
	case slices.Contains(sharedKinds, kind):
		t.Errorf("%s is a %s, which a copy shares", name, kind)
	}
}
