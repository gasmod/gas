package test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gasmod/gas/log"
)

// captureMarshaler records the batches it is asked to marshal.
type captureMarshaler struct {
	batches [][]log.Record
	mu      sync.Mutex
}

func (m *captureMarshaler) Marshal(records []log.Record) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]log.Record, len(records))
	copy(cp, records)
	m.batches = append(m.batches, cp)
	return []byte("ok"), nil
}

func (m *captureMarshaler) ContentType() string { return "text/plain" }

func (m *captureMarshaler) records() []log.Record {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []log.Record
	for _, b := range m.batches {
		out = append(out, b...)
	}
	return out
}

func (m *captureMarshaler) batchCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.batches)
}

// testSink builds a running sender pointed at a 200-OK test server.
func testSink(t *testing.T, m log.Marshaler, opts ...log.ShippingOption) *log.ShippingLogger {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	opts = append([]log.ShippingOption{
		log.WithoutLocalHandler(),
		log.WithFlushInterval(time.Hour), // never auto-flush during tests
	}, opts...)
	s, err := log.NewShippingLogger(srv.URL, m, opts...)()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	return s
}

func TestSender_FlushDelivers(t *testing.T) {
	m := &captureMarshaler{}
	s := testSink(t, m, log.WithBatchSize(100))

	s.Info("a").Send()
	s.Info("b").Send()
	s.Flush()

	if got := len(m.records()); got != 2 {
		t.Fatalf("delivered %d records, want 2", got)
	}
}

func TestSender_BatchSizeTriggersSend(t *testing.T) {
	m := &captureMarshaler{}
	s := testSink(t, m, log.WithBatchSize(3))

	for i := range 3 {
		s.Info(string(rune('a' + i))).Send()
	}
	// The third record should trip an automatic send without an explicit flush.
	waitFor(t, func() bool { return m.batchCount() == 1 && len(m.records()) == 3 })
}

func TestSender_CloseDrains(t *testing.T) {
	m := &captureMarshaler{}
	s := testSink(t, m, log.WithBatchSize(100))

	s.Info("x").Send()
	s.Info("y").Send()
	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("close: %v", err)
	}
	if got := len(m.records()); got != 2 {
		t.Errorf("close drained %d records, want 2", got)
	}
	// Close is idempotent.
	if err := s.Close(context.Background()); err != nil {
		t.Errorf("second close: %v", err)
	}
}

func TestSender_EnqueueNeverBlocks(t *testing.T) {
	m := &captureMarshaler{}
	s := testSink(t, m,
		log.WithQueueSize(1),
		log.WithBatchSize(100),
	)
	// Far more than the queue can hold; must not block even if some drop.
	done := make(chan struct{})
	go func() {
		for range 1000 {
			s.Info("flood").Send()
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("enqueue blocked under backpressure")
	}
}

func TestHandler_LevelFilter(t *testing.T) {
	h, closer := log.NewShippingHandler(
		"http://127.0.0.1:0",
		&captureMarshaler{},
		log.WithLevel(slog.LevelWarn),
		log.WithoutLocalHandler(),
	)
	t.Cleanup(func() { _ = closer.Close() })
	if h.Enabled(context.Background(), slog.LevelInfo) {
		t.Error("info should be filtered below warn")
	}
	if !h.Enabled(context.Background(), slog.LevelError) {
		t.Error("error should pass warn threshold")
	}
}

func TestHandler_GroupAndAttrsQualify(t *testing.T) {
	m := &captureMarshaler{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	base, closer := log.NewShippingHandler(
		srv.URL,
		m,
		log.WithBatchSize(100),
		log.WithFlushInterval(time.Hour),
		log.WithLevel(slog.LevelInfo),
		log.WithoutLocalHandler(),
	)
	h := base.WithGroup("http").WithAttrs([]slog.Attr{slog.String("scheme", "https")})

	rec := slog.NewRecord(time.Unix(1, 0), slog.LevelInfo, "req", 0)
	rec.AddAttrs(slog.Int("status", 200))
	if err := h.Handle(context.Background(), rec); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if err := closer.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	got := map[string]bool{}
	for _, a := range m.records()[0].Attrs {
		got[a.Key] = true
	}
	if !got["http.scheme"] {
		t.Errorf("WithAttrs under group should qualify to http.scheme; got %v", got)
	}
	if !got["http.status"] {
		t.Errorf("record attr under group should qualify to http.status; got %v", got)
	}
}

// waitFor polls cond for up to a second.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}
