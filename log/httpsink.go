//nolint:revive // intentional package name
package log

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// Record is a backend-neutral log record handed to a [Marshaler]. It carries
// the level, timestamp, message, and the fully-qualified attributes captured
// from an slog event.
type Record struct {
	Time    time.Time
	Message string
	Attrs   []slog.Attr
	Level   slog.Level
}

// Marshaler encodes a batch of records into a single HTTP request body. The
// wire shape (OTLP, ECS, a custom schema, ...) is entirely the marshaler's
// concern, which is what keeps the transport reusable across backends.
type Marshaler interface {
	// Marshal encodes the batch into one request body.
	Marshal(records []Record) ([]byte, error)
	// ContentType is the value sent in the Content-Type header.
	ContentType() string
}

// Handler is an [slog.Handler] that captures records and hands them to a
// batching HTTP sender. Construct it via [NewShippingLogger]; it is exported
// only so it can be composed into custom handler chains.
type Handler struct {
	sender *sender
	level  slog.Leveler
	group  string
	attrs  []slog.Attr
}

var _ slog.Handler = (*Handler)(nil)

// Enabled reports whether an event at the given level should be handled.
func (h *Handler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level.Level()
}

// Handle captures the record's fields and enqueues it for delivery.
func (h *Handler) Handle(_ context.Context, r slog.Record) error {
	rec := Record{
		Time:    r.Time,
		Level:   r.Level,
		Message: r.Message,
		Attrs:   make([]slog.Attr, 0, len(h.attrs)+r.NumAttrs()),
	}
	rec.Attrs = append(rec.Attrs, h.attrs...)
	r.Attrs(func(a slog.Attr) bool {
		rec.Attrs = append(rec.Attrs, h.qualify(a))
		return true
	})
	h.sender.enqueue(rec)
	return nil
}

// WithAttrs returns a handler that prepends the given attributes to every record.
func (h *Handler) WithAttrs(as []slog.Attr) slog.Handler {
	nh := *h
	nh.attrs = make([]slog.Attr, 0, len(h.attrs)+len(as))
	nh.attrs = append(nh.attrs, h.attrs...)
	for _, a := range as {
		nh.attrs = append(nh.attrs, h.qualify(a))
	}
	return &nh
}

// WithGroup returns a handler that qualifies subsequent attribute keys with name.
func (h *Handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	nh := *h
	if h.group != "" {
		nh.group = h.group + "." + name
	} else {
		nh.group = name
	}
	return &nh
}

// qualify prefixes an attribute key with the active group path.
func (h *Handler) qualify(a slog.Attr) slog.Attr {
	if h.group == "" {
		return a
	}
	return slog.Attr{Key: h.group + "." + a.Key, Value: a.Value}
}

// flushReq is a synchronous flush request; the sender closes done once the
// current batch has been posted.
type flushReq struct {
	done chan struct{}
}

// sender owns the delivery goroutine: it batches records and posts them to the
// endpoint, and coordinates flush and shutdown.
type sender struct {
	client     *http.Client
	marshaler  Marshaler
	onError    func(error)
	ch         chan Record
	flushCh    chan flushReq
	stopped    chan struct{}
	headers    map[string]string
	endpoint   string
	batchSize  int
	flushEvery time.Duration
	closeOnce  sync.Once
	wg         sync.WaitGroup
}

// newSender starts the delivery goroutine and returns the running sender.
func newSender(endpoint string, marshaler Marshaler, cfg shippingConfig) *sender {
	s := &sender{
		client:     cfg.client,
		marshaler:  marshaler,
		onError:    cfg.onError,
		ch:         make(chan Record, cfg.queueSize),
		flushCh:    make(chan flushReq),
		stopped:    make(chan struct{}),
		headers:    cfg.headers,
		endpoint:   endpoint,
		batchSize:  cfg.batchSize,
		flushEvery: cfg.flushEvery,
	}
	s.wg.Add(1)
	go s.run()
	return s
}

// enqueue adds a record without blocking; it drops the record if the queue is
// full or the sender is shutting down, so logging never stalls the caller.
func (s *sender) enqueue(r Record) {
	select {
	case s.ch <- r:
	case <-s.stopped:
	default:
	}
}

// run is the delivery loop: it accumulates a batch and posts it on size, on the
// flush interval, on an explicit flush, and once more on shutdown.
func (s *sender) run() {
	defer s.wg.Done()
	ticker := time.NewTicker(s.flushEvery)
	defer ticker.Stop()

	batch := make([]Record, 0, s.batchSize)
	for {
		select {
		case r := <-s.ch:
			if batch = append(batch, r); len(batch) >= s.batchSize {
				s.post(batch)
				batch = batch[:0]
			}
		case <-ticker.C:
			if len(batch) > 0 {
				s.post(batch)
				batch = batch[:0]
			}
		case req := <-s.flushCh:
			batch = s.drain(batch)
			s.post(batch)
			batch = batch[:0]
			close(req.done)
		case <-s.stopped:
			batch = s.drain(batch)
			s.post(batch)
			return
		}
	}
}

// drain appends every immediately-available queued record to batch.
func (s *sender) drain(batch []Record) []Record {
	for {
		select {
		case r := <-s.ch:
			batch = append(batch, r)
		default:
			return batch
		}
	}
}

// post marshals and ships a batch. Delivery failures go to the error handler;
// they never propagate to the logging call site.
func (s *sender) post(batch []Record) {
	if len(batch) == 0 {
		return
	}
	body, err := s.marshaler.Marshal(batch)
	if err != nil {
		s.report(fmt.Errorf("marshal log batch: %w", err))
		return
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, s.endpoint, bytes.NewReader(body)) //nolint:gosec // endpoint is operator-configured, not user input
	if err != nil {
		s.report(fmt.Errorf("build log request: %w", err))
		return
	}
	req.Header.Set("Content-Type", s.marshaler.ContentType())
	for k, v := range s.headers {
		req.Header.Set(k, v)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		s.report(fmt.Errorf("ship logs: %w", err))
		return
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode >= http.StatusMultipleChoices {
		s.report(fmt.Errorf("ship logs: unexpected status %d", resp.StatusCode))
	}
}

// flush posts the current batch and blocks until it has been sent. It is a
// no-op once the sender has been closed.
func (s *sender) flush() {
	req := flushReq{done: make(chan struct{})}
	select {
	case s.flushCh <- req:
		<-req.done
	case <-s.stopped:
	}
}

// close stops the sender, draining and posting any queued records first. It is
// idempotent and safe to call concurrently.
func (s *sender) close() error {
	s.closeOnce.Do(func() {
		close(s.stopped)
		s.wg.Wait()
	})
	return nil
}

// report forwards a delivery error to the configured handler, if any.
func (s *sender) report(err error) {
	if s.onError != nil {
		s.onError(err)
	}
}

// fanoutHandler dispatches each record to every wrapped handler that is enabled
// for its level. It lets a single logger both write locally and ship remotely.
type fanoutHandler []slog.Handler

var _ slog.Handler = fanoutHandler(nil)

// newFanout combines a local handler with the shipping handler. A nil local
// handler yields the shipping handler alone.
func newFanout(local, ship slog.Handler) slog.Handler {
	if local == nil {
		return ship
	}
	return fanoutHandler{local, ship}
}

// Enabled reports whether any wrapped handler is enabled for the level.
func (f fanoutHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range f {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

// Handle dispatches a clone of the record to each enabled handler.
func (f fanoutHandler) Handle(ctx context.Context, r slog.Record) error {
	for _, h := range f {
		if h.Enabled(ctx, r.Level) {
			if err := h.Handle(ctx, r.Clone()); err != nil {
				return fmt.Errorf("fanout handler: %w", err)
			}
		}
	}
	return nil
}

// WithAttrs returns a fanout whose members each carry the given attributes.
func (f fanoutHandler) WithAttrs(as []slog.Attr) slog.Handler {
	out := make(fanoutHandler, len(f))
	for i, h := range f {
		out[i] = h.WithAttrs(as)
	}
	return out
}

// WithGroup returns a fanout whose members each open the given group.
func (f fanoutHandler) WithGroup(name string) slog.Handler {
	out := make(fanoutHandler, len(f))
	for i, h := range f {
		out[i] = h.WithGroup(name)
	}
	return out
}
