package moduleslogging_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/rs/zerolog"

	"github.com/gasmod/gas"
	gaslog "github.com/gasmod/gas/log"
)

// logSink captures JSON log lines from several goroutines.
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

// withMsg returns the captured records whose message is msg, under either
// slog's "msg" or zerolog's "message" key.
func (s *logSink) withMsg(t *testing.T, msg string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, r := range s.records(t) {
		if r["msg"] == msg || r["message"] == msg {
			out = append(out, r)
		}
	}
	return out
}

// line returns the one captured record whose message is msg.
func (s *logSink) line(t *testing.T, msg string) map[string]any {
	t.Helper()
	lines := s.withMsg(t, msg)
	if len(lines) != 1 {
		t.Fatalf("found %d %q lines, want 1; log:\n%s", len(lines), msg, s.String())
	}
	return lines[0]
}

// slogSink returns a sink and a slog-backed gas.Logger writing JSON lines to
// it at debug level and above.
func slogSink(t *testing.T) (*logSink, gas.Logger) {
	t.Helper()
	sink := &logSink{}
	l, err := gaslog.NewSlogLogger(gaslog.WithSlogInstance(slog.New(
		slog.NewJSONHandler(sink, &slog.HandlerOptions{Level: slog.LevelDebug}))))()
	if err != nil {
		t.Fatal(err)
	}
	return sink, l
}

// slogSinkCtor is slogSink as a constructor for the container.
func slogSinkCtor() (*logSink, gaslog.SlogLoggerCtor) {
	sink := &logSink{}
	return sink, gaslog.NewSlogLogger(gaslog.WithSlogInstance(slog.New(
		slog.NewJSONHandler(sink, &slog.HandlerOptions{Level: slog.LevelDebug}))))
}

// zerologSink returns a sink and a zerolog-backed gas.Logger writing to it
// at trace level and above.
func zerologSink(t *testing.T) (*logSink, gas.Logger) {
	t.Helper()
	sink := &logSink{}
	zl := zerolog.New(sink).Level(zerolog.TraceLevel)
	l, err := gaslog.NewZeroLogLogger(gaslog.WithZeroLogInstance(&zl))()
	if err != nil {
		t.Fatal(err)
	}
	return sink, l
}

func start(t *testing.T, app *gas.App) {
	t.Helper()
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
}

func withID(path, id string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set(middleware.RequestIDHeader, id)
	return req
}

// shipped is one batch request received by a shipServer.
type shipped struct {
	header http.Header
	body   []byte
}

// shipServer collects the batches a shipping logger posts. Requests wait
// while the gate is closed.
type shipServer struct {
	*httptest.Server
	mu      sync.Mutex
	batches []shipped
	status  int
	gate    chan struct{}
	arrived chan struct{}
}

func newShipServer(t *testing.T) *shipServer {
	t.Helper()
	s := &shipServer{status: http.StatusOK, arrived: make(chan struct{}, 4096)}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		gate := s.gate
		s.mu.Unlock()
		s.arrived <- struct{}{}
		if gate != nil {
			<-gate
		}
		s.mu.Lock()
		s.batches = append(s.batches, shipped{header: r.Header.Clone(), body: body})
		status := s.status
		s.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(s.Close)
	return s
}

// hold makes later requests wait until the returned release is called.
func (s *shipServer) hold() (release func()) {
	gate := make(chan struct{})
	s.mu.Lock()
	s.gate = gate
	s.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			s.gate = nil
			s.mu.Unlock()
			close(gate)
		})
	}
}

func (s *shipServer) setStatus(code int) {
	s.mu.Lock()
	s.status = code
	s.mu.Unlock()
}

// waitArrived waits for one request to reach the handler.
func (s *shipServer) waitArrived(t *testing.T) {
	t.Helper()
	select {
	case <-s.arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("no batch request arrived")
	}
}

func (s *shipServer) snapshot() []shipped {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]shipped(nil), s.batches...)
}

// otlpBatch is the part of an OTLP logs request the tests look at.
type otlpBatch struct {
	ResourceLogs []struct {
		Resource struct {
			Attributes []otlpKV `json:"attributes"`
		} `json:"resource"`
		ScopeLogs []struct {
			Scope struct {
				Name string `json:"name"`
			} `json:"scope"`
			LogRecords []otlpRecord `json:"logRecords"`
		} `json:"scopeLogs"`
	} `json:"resourceLogs"`
}

type otlpRecord struct {
	TimeUnixNano   string   `json:"timeUnixNano"`
	SeverityText   string   `json:"severityText"`
	SeverityNumber int      `json:"severityNumber"`
	Body           otlpVal  `json:"body"`
	Attributes     []otlpKV `json:"attributes"`
}

type otlpKV struct {
	Key   string  `json:"key"`
	Value otlpVal `json:"value"`
}

type otlpVal struct {
	StringValue *string  `json:"stringValue"`
	IntValue    *string  `json:"intValue"`
	BoolValue   *bool    `json:"boolValue"`
	DoubleValue *float64 `json:"doubleValue"`
}

func decodeOTLP(t *testing.T, body []byte) otlpBatch {
	t.Helper()
	var b otlpBatch
	if err := json.Unmarshal(body, &b); err != nil {
		t.Fatalf("decode OTLP body %s: %v", body, err)
	}
	return b
}

// records returns every log record in the batch.
func (b otlpBatch) records() []otlpRecord {
	var out []otlpRecord
	for _, rl := range b.ResourceLogs {
		for _, sl := range rl.ScopeLogs {
			out = append(out, sl.LogRecords...)
		}
	}
	return out
}

// shippedRecords decodes every OTLP batch the server received.
func (s *shipServer) shippedRecords(t *testing.T) []otlpRecord {
	t.Helper()
	var out []otlpRecord
	for _, b := range s.snapshot() {
		out = append(out, decodeOTLP(t, b.body).records()...)
	}
	return out
}

func (s *shipServer) messages(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, r := range s.shippedRecords(t) {
		if r.Body.StringValue != nil {
			out = append(out, *r.Body.StringValue)
		}
	}
	return out
}

func attr(r otlpRecord, key string) (otlpVal, bool) {
	for _, a := range r.Attributes {
		if a.Key == key {
			return a.Value, true
		}
	}
	return otlpVal{}, false
}

// errorLog collects the errors passed to a WithErrorHandler callback.
type errorLog struct {
	mu   sync.Mutex
	errs []error
}

func (e *errorLog) handle(err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.errs = append(e.errs, err)
}

func (e *errorLog) list() []error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]error(nil), e.errs...)
}

// shipper builds a shipping logger with no local handler and a long flush
// interval, so batches go out only when a test asks.
func shipper(t *testing.T, endpoint string, opts ...gaslog.ShippingOption) *gaslog.ShippingLogger {
	t.Helper()
	base := []gaslog.ShippingOption{gaslog.WithoutLocalHandler(), gaslog.WithFlushInterval(time.Hour)}
	l, err := gaslog.NewShippingLogger(endpoint, gaslog.NewOTLPMarshaler(), append(base, opts...)...)()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close(context.Background()) })
	return l
}

func sendN(l gas.Logger, n int, prefix string) {
	for i := range n {
		l.Info(fmt.Sprintf("%s-%d", prefix, i)).Send()
	}
}

// eventually polls cond until it holds or the timeout passes.
func eventually(t *testing.T, timeout time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}
