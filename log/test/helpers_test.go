package log_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/log"
)

// endpoint is the endpoint of the tests that never reach a server.
const endpoint = "https://collector.example:4318/v1/logs"

// errFake is the error the fakes return.
var errFake = errors.New("fake failure")

// The errors of the documented messages and of the field tests.
var (
	errDiskFull    = errors.New("disk full")
	errUnsupported = errors.New("unsupported value")
	errRefused     = errors.New("connection refused")
	errBoom        = errors.New("boom")
	errBang        = errors.New("bang")
)

// capture is a Marshaler that keeps a copy of every batch it gets. It
// fails with err, or panics, when set.
type capture struct {
	// err is returned by Marshal when not nil.
	err error
	// batches are copies of the batches, in order.
	batches [][]log.Record
	// mu guards batches.
	mu sync.Mutex
	// panics makes Marshal panic.
	panics bool
}

// Marshal records a copy of records and returns their messages, one per
// line.
func (c *capture) Marshal(records []log.Record) ([]byte, error) {
	if c.panics {
		panic("marshal " + records[0].Message)
	}

	if c.err != nil {
		return nil, c.err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.batches = append(c.batches, slices.Clone(records))

	var out []byte

	for _, r := range records {
		out = append(out, r.Message+"\n"...)
	}

	return out, nil
}

// ContentType returns a test content type.
func (*capture) ContentType() string { return "application/x-test" }

// records returns every record captured so far, in order.
func (c *capture) records() []log.Record {
	c.mu.Lock()
	defer c.mu.Unlock()

	var out []log.Record

	for _, b := range c.batches {
		out = append(out, b...)
	}

	return out
}

// sizes returns the size of each batch captured so far.
func (c *capture) sizes() []int {
	c.mu.Lock()
	defer c.mu.Unlock()

	out := make([]int, 0, len(c.batches))
	for _, b := range c.batches {
		out = append(out, len(b))
	}

	return out
}

// messages returns the message of every record captured so far.
func (c *capture) messages() []string {
	recs := c.records()
	out := make([]string, 0, len(recs))

	for _, r := range recs {
		out = append(out, r.Message)
	}

	return out
}

// roundTrip is an http.RoundTripper made of a function.
type roundTrip func(*http.Request) (*http.Response, error)

// RoundTrip calls f.
func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// clientOf returns a client whose transport is f.
func clientOf(f roundTrip) *http.Client { return &http.Client{Transport: f} }

// status returns a client every request of which gets code.
func status(code int) *http.Client {
	return clientOf(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: code,
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    r,
		}, nil
	})
}

// failing returns a client every request of which fails with err.
func failing(err error) *http.Client {
	return clientOf(func(*http.Request) (*http.Response, error) { return nil, err })
}

// blocking returns a client whose requests wait for their context to end,
// and a channel that receives each request as it starts.
func blocking() (*http.Client, chan *http.Request) {
	started := make(chan *http.Request, 100)

	return clientOf(func(r *http.Request) (*http.Response, error) {
		started <- r

		<-r.Context().Done()

		return nil, r.Context().Err()
	}), started
}

// gate is a Marshaler whose Marshal blocks until release is closed, and
// that notes whether two calls ever overlapped.
type gate struct {
	// entered receives a value as each Marshal starts.
	entered chan struct{}
	// release is closed by open.
	release chan struct{}
	// once guards the close of release.
	once sync.Once
	// active counts the calls running.
	active atomic.Int32
	// overlapped is set when a call starts while another runs.
	overlapped atomic.Bool
}

// newGate returns a closed gate, opened when the test ends.
func newGate(t *testing.T) *gate {
	t.Helper()

	g := &gate{entered: make(chan struct{}, 100), release: make(chan struct{})}

	t.Cleanup(g.open)

	return g
}

// Marshal waits for open and returns a fixed body.
func (g *gate) Marshal([]log.Record) ([]byte, error) {
	if g.active.Add(1) > 1 {
		g.overlapped.Store(true)
	}

	defer g.active.Add(-1)

	g.entered <- struct{}{}

	<-g.release

	return []byte("gated"), nil
}

// ContentType returns a test content type.
func (*gate) ContentType() string { return "application/x-test" }

// open lets every Marshal, waiting or to come, return.
func (g *gate) open() { g.once.Do(func() { close(g.release) }) }

// ok returns a client every request of which gets 204.
func ok() *http.Client { return status(http.StatusNoContent) }

// reports collects what the error handler gets.
type reports struct {
	// ch receives each report.
	ch chan error
}

// newReports returns an empty collector.
func newReports() *reports { return &reports{ch: make(chan error, 1000)} }

// handler is the error handler.
func (r *reports) handler(err error) { r.ch <- err }

// next returns the next report, failing the test after five seconds.
func (r *reports) next(t *testing.T) error {
	t.Helper()

	select {
	case err := <-r.ch:
		return err
	case <-time.After(5 * time.Second):
		require.FailNow(t, "no report")

		return nil
	}
}

// noReportWindow is how long none waits for a report that must not come.
const noReportWindow = 20 * time.Millisecond

// none checks that no report arrives within noReportWindow.
func (r *reports) none(t *testing.T) {
	t.Helper()

	select {
	case err := <-r.ch:
		require.FailNow(t, "unexpected report", "%v", err)
	case <-time.After(noReportWindow):
	}
}

// quiet are the options of a shipping logger that writes nothing locally
// and flushes only when asked.
func quiet(opts ...log.ShippingOption) []log.ShippingOption {
	return append([]log.ShippingOption{
		log.WithoutLocalHandler(),
		log.WithFlushInterval(time.Hour),
	}, opts...)
}

// shippingLogger returns an initialized ShippingLogger on m, closed when
// the test ends. Its HTTP client answers 204 unless opts give another.
func shippingLogger(t *testing.T, m log.Marshaler, opts ...log.ShippingOption) *log.ShippingLogger {
	t.Helper()

	l, err := log.NewShippingLogger(
		endpoint,
		m,
		append([]log.ShippingOption{log.WithHTTPClient(ok())}, opts...)...)()
	require.NoError(t, err)
	require.NoError(t, l.Init(t.Context()))

	t.Cleanup(func() { closeQuickly(l.Close) })

	return l
}

// shippingHandler returns a ShippingHandler on m, closed when the test
// ends. Its HTTP client answers 204 unless opts give another.
func shippingHandler(
	t *testing.T,
	m log.Marshaler,
	opts ...log.ShippingOption,
) *log.ShippingHandler {
	t.Helper()

	h, err := log.NewShippingHandler(
		endpoint,
		m,
		append([]log.ShippingOption{log.WithHTTPClient(ok())}, opts...)...)
	require.NoError(t, err)

	t.Cleanup(func() { closeQuickly(h.Close) })

	return h
}

// closeQuickly calls a Close with a second to finish; a test that cares
// about the result has closed already.
func closeQuickly(closeFn func(context.Context) error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_ = closeFn(ctx)
}

// attrs returns the attributes of r by key.
func attrs(r log.Record) map[string]slog.Value {
	out := map[string]slog.Value{}
	for _, a := range r.Attrs {
		out[a.Key] = a.Value
	}

	return out
}

// lockedBuffer is a bytes.Buffer safe for concurrent use.
type lockedBuffer struct {
	// buf holds what was written.
	buf bytes.Buffer
	// mu guards buf.
	mu sync.Mutex
}

// Write appends p.
func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

// lines decodes each line written as a JSON object.
func (b *lockedBuffer) lines(t *testing.T) []map[string]any {
	t.Helper()

	b.mu.Lock()
	defer b.mu.Unlock()

	var out []map[string]any

	for line := range strings.Lines(b.buf.String()) {
		var m map[string]any

		require.NoError(t, json.Unmarshal([]byte(line), &m))

		out = append(out, m)
	}

	return out
}

// jsonLogger returns a slog.Logger writing JSON to buf at every level.
func jsonLogger(buf io.Writer, source bool) *slog.Logger {
	return slog.New(
		slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: log.LevelTrace, AddSource: source}),
	)
}

// collector is an httptest server that keeps every body posted to it.
type collector struct {
	// srv is the server.
	srv *httptest.Server
	// bodies are the bodies posted, in order.
	bodies [][]byte
	// headers are the headers of each request.
	headers []http.Header
	// mu guards bodies and headers.
	mu sync.Mutex
}

// newCollector returns a running collector, closed when the test ends.
func newCollector(t *testing.T) *collector {
	t.Helper()

	c := &collector{}

	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)

			return
		}

		c.mu.Lock()

		c.bodies = append(c.bodies, body)
		c.headers = append(c.headers, r.Header.Clone())
		c.mu.Unlock()

		w.WriteHeader(http.StatusOK)
	}))

	t.Cleanup(c.srv.Close)

	return c
}

// posted returns the bodies posted so far.
func (c *collector) posted() [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()

	return slices.Clone(c.bodies)
}

// stderrLines runs fn with os.Stderr writing to a pipe, and returns the
// first n lines written to it, by fn or by goroutines fn started, failing
// the test when they do not arrive within five seconds.
func stderrLines(t *testing.T, fn func(), n int) []string {
	t.Helper()

	r, w, err := os.Pipe()
	require.NoError(t, err)

	prev := os.Stderr

	os.Stderr = w

	defer func() {
		os.Stderr = prev

		require.NoError(t, w.Close())
		require.NoError(t, r.Close())
	}()

	lines := make(chan string, n)

	go func() {
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			lines <- sc.Text()
		}

		close(lines)
	}()

	fn()

	out := make([]string, 0, n)

	for len(out) < n {
		select {
		case line, ok := <-lines:
			require.True(t, ok, "standard error closed after %q", out)

			out = append(out, line)
		case <-time.After(5 * time.Second):
			require.FailNow(t, "missing lines on standard error", "got %q", out)
		}
	}

	return out
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
		code, found := strings.CutPrefix(sc.Text(), "//\t")
		if found && strings.HasPrefix(code, prefix) {
			out = append(out, code)
		}
	}

	require.NoError(t, sc.Err())

	return out
}
