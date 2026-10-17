//nolint:revive // intentional package name
package log

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gasmod/gas"
)

// recordingServer captures the request bodies and headers it receives.
type recordingServer struct {
	headers []http.Header
	bodies  [][]byte
	mu      sync.Mutex
}

func (r *recordingServer) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.bodies = append(r.bodies, body)
		r.headers = append(r.headers, req.Header.Clone())
		r.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}
}

func (r *recordingServer) snapshot() ([][]byte, []http.Header) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.bodies, r.headers
}

func TestShippingLogger_EndToEnd(t *testing.T) {
	rs := &recordingServer{}
	srv := httptest.NewServer(rs.handler())
	defer srv.Close()

	ctor := NewShippingLogger(
		srv.URL,
		NewOTLPMarshaler(WithServiceName("due-api")),
		WithoutLocalHandler(),
		WithHeader("X-API-Key", "secret"),
		WithBatchSize(10),
		WithFlushInterval(time.Hour),
	)
	logger := ctor()

	logger.Info("hello").Str("k", "v").Send()
	logger.Error("boom").Int("code", 42).Send()
	logger.Flush()

	bodies, headers := rs.snapshot()
	if len(bodies) != 1 {
		t.Fatalf("server got %d requests, want 1 batched", len(bodies))
	}
	if headers[0].Get("X-API-Key") != "secret" {
		t.Errorf("missing API key header: %v", headers[0].Get("X-API-Key"))
	}
	if ct := headers[0].Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q", ct)
	}

	var out decodedOTLP
	if err := json.Unmarshal(bodies[0], &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	recs := out.ResourceLogs[0].ScopeLogs[0].LogRecords
	if len(recs) != 2 {
		t.Fatalf("shipped %d records, want 2", len(recs))
	}
	if recs[0].Body.StringValue == nil || *recs[0].Body.StringValue != "hello" {
		t.Errorf("record 0 body = %+v", recs[0].Body)
	}
	if recs[1].SeverityText != "ERROR" {
		t.Errorf("record 1 severity = %q", recs[1].SeverityText)
	}

	if err := logger.Close(context.Background()); err != nil {
		t.Errorf("close: %v", err)
	}
}

func TestShippingLogger_ClosingDrainsWithoutFlush(t *testing.T) {
	rs := &recordingServer{}
	srv := httptest.NewServer(rs.handler())
	defer srv.Close()

	logger := NewShippingLogger(
		srv.URL,
		NewOTLPMarshaler(WithServiceName("svc")),
		WithoutLocalHandler(),
		WithBatchSize(100),
		WithFlushInterval(time.Hour),
	)()

	logger.Info("only-on-close").Send()
	if err := logger.Close(context.Background()); err != nil {
		t.Fatalf("close: %v", err)
	}

	bodies, _ := rs.snapshot()
	if len(bodies) != 1 || !bytes.Contains(bodies[0], []byte("only-on-close")) {
		t.Errorf("close did not drain the buffered record: %d bodies", len(bodies))
	}
}

func TestShippingLogger_LocalTee(t *testing.T) {
	rs := &recordingServer{}
	srv := httptest.NewServer(rs.handler())
	defer srv.Close()

	var buf bytes.Buffer
	local := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})

	logger := NewShippingLogger(
		srv.URL,
		NewOTLPMarshaler(WithServiceName("svc")),
		WithLocalHandler(local),
		WithFlushInterval(time.Hour),
	)()

	logger.Info("teed").Str("side", "local").Send()
	logger.Flush()

	if !strings.Contains(buf.String(), "teed") {
		t.Errorf("local handler did not receive the record: %q", buf.String())
	}
	bodies, _ := rs.snapshot()
	if len(bodies) != 1 || !bytes.Contains(bodies[0], []byte("teed")) {
		t.Errorf("remote sink did not receive the record")
	}
	_ = logger.Close(context.Background())
}

func TestShippingLogger_ImplementsService(t *testing.T) {
	logger := NewShippingLogger(
		"http://example.invalid",
		NewOTLPMarshaler(),
		WithoutLocalHandler(),
		WithName("central-logs"),
	)()
	defer func() { _ = logger.Close(context.Background()) }()

	var svc gas.Service = logger
	if svc.Name() != "central-logs" {
		t.Errorf("name = %q", svc.Name())
	}
	if err := svc.Init(t.Context()); err != nil {
		t.Errorf("init: %v", err)
	}
}

func TestShippingLogger_DeliveryErrorReported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	var mu sync.Mutex
	var errs []error
	logger := NewShippingLogger(
		srv.URL,
		NewOTLPMarshaler(WithServiceName("svc")),
		WithoutLocalHandler(),
		WithFlushInterval(time.Hour),
		WithErrorHandler(func(err error) {
			mu.Lock()
			errs = append(errs, err)
			mu.Unlock()
		}),
	)()

	logger.Info("will-fail").Send()
	logger.Flush()
	_ = logger.Close(context.Background())

	mu.Lock()
	defer mu.Unlock()
	if len(errs) == 0 {
		t.Error("expected a delivery error to be reported for a 500 response")
	}
}

func TestNewShippingHandler_SetDefault(t *testing.T) {
	rs := &recordingServer{}
	srv := httptest.NewServer(rs.handler())
	defer srv.Close()

	h, closer := NewShippingHandler(
		srv.URL,
		NewOTLPMarshaler(WithServiceName("svc")),
		WithoutLocalHandler(),
		WithFlushInterval(time.Hour),
	)
	logger := slog.New(h)
	logger.Info("via-default", slog.String("k", "v"))

	if err := closer.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	bodies, _ := rs.snapshot()
	if len(bodies) != 1 || !bytes.Contains(bodies[0], []byte("via-default")) {
		t.Errorf("handler did not ship the record: %d bodies", len(bodies))
	}
}

// A sub-logger derived with With, as services hold, must flush through the
// shipping sender, and so must a sub-logger of a sub-logger.
func TestShippingLogger_SubLoggerFlushDelivers(t *testing.T) {
	rs := &recordingServer{}
	srv := httptest.NewServer(rs.handler())
	defer srv.Close()

	logger := NewShippingLogger(
		srv.URL,
		NewOTLPMarshaler(WithServiceName("svc")),
		WithoutLocalHandler(),
		WithFlushInterval(time.Hour),
	)()
	t.Cleanup(func() { _ = logger.Close(context.Background()) })

	sub := logger.With().Str("service", "a").Logger()
	sub.Info("from sub").Send()
	sub.Flush()

	bodies, _ := rs.snapshot()
	if len(bodies) != 1 {
		t.Fatalf("after sub.Flush the server got %d batches, want 1", len(bodies))
	}

	subsub := sub.With().Str("part", "b").Logger()
	subsub.Info("from sub-sub").Send()
	subsub.Flush()

	bodies, _ = rs.snapshot()
	if len(bodies) != 2 {
		t.Fatalf("after subsub.Flush the server got %d batches, want 2", len(bodies))
	}
}

// A plain SlogLogger and its sub-loggers keep a Flush that does nothing.
func TestSlogLogger_SubLoggerFlushIsNoop(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	l := NewSlogLogger(WithSlogInstance(slog.New(slog.NewTextHandler(&buf, nil))))()
	sub := l.With().Str("k", "v").Logger()
	sub.Info("x").Send()
	sub.Flush()
	l.Flush()

	if !strings.Contains(buf.String(), "k=v") {
		t.Errorf("log output %q missing the sub-logger field", buf.String())
	}
}
