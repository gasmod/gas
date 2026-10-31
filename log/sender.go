package log

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"
)

// sender queues records and posts them in batches from a goroutine of its
// own, as described in the package documentation under Shipping. It is
// shared by a ShippingHandler and every handler derived from it.
type sender struct {
	// marshaler encodes the batches.
	marshaler Marshaler
	// level is the lowest level shipped.
	level slog.Leveler
	// client posts the batches.
	client *http.Client
	// reporter delivers the error reports.
	reporter *reporter
	// run is the running sender, or nil before start and after close.
	run *run
	// last is the run closed last, whose goroutine may still be running;
	// start waits for it, so two goroutines never post at once.
	last *run
	// template is the request each post clones: the endpoint, the
	// Content-Type the Marshaler names and the headers given to WithHeader.
	template *http.Request
	// shown is the endpoint as errors name it.
	shown string
	// batchSize, queueSize and flushInterval are as their options say.
	batchSize     int
	queueSize     int
	flushInterval time.Duration
	// dropped counts the records that were not delivered.
	dropped atomic.Uint64
	// mu guards run, last and started. enqueue holds it shared while it
	// queues, so once close holds it, no record enters the closing run's
	// queue.
	mu sync.RWMutex
	// started is set by the first start.
	started bool
}

// run is one start of the sender, until its close.
type run struct {
	// ctx bounds every post of the run; close cancels it.
	ctx context.Context //nolint:containedctx // the run's lifetime, canceled by close.
	// closeCtx is the context of the close, set before stop is closed.
	closeCtx context.Context //nolint:containedctx // handed from close to the goroutine.
	// cancel cancels ctx.
	cancel context.CancelFunc
	// full tells the goroutine that the queue holds a full batch.
	full chan struct{}
	// flushes carries the flush requests.
	flushes chan flushRequest
	// stop is closed by close.
	stop chan struct{}
	// done is closed when the goroutine returns.
	done chan struct{}
	// result receives the error of the final drain.
	result chan error
	// queue holds the records waiting to be sent, oldest first; its
	// capacity is the queue size.
	queue []Record
	// inflight counts the records taken from the queue and not yet
	// settled: being posted, or posted by a drain that has not sent its
	// result yet, so a late Flush or Close still counts them.
	inflight int
	// mu guards queue and inflight.
	mu sync.Mutex
}

// flushRequest asks the goroutine to post what is queued.
type flushRequest struct {
	// ctx is the context of the flush.
	ctx context.Context //nolint:containedctx // handed from Flush to the goroutine.
	// result receives the error of the flush.
	result chan error
}

// newSender returns a stopped sender posting clones of template.
func newSender(template *http.Request, shown string, m Marshaler, c *shippingConfig) *sender {
	template.Header.Set("Content-Type", m.ContentType())

	for _, h := range c.headers {
		template.Header.Set(h.key, h.value)
	}

	return &sender{
		marshaler:     m,
		level:         c.level,
		client:        c.client,
		reporter:      newReporter(c.onError, c.flushInterval),
		template:      template,
		shown:         shown,
		batchSize:     c.batchSize,
		queueSize:     c.queueSize,
		flushInterval: c.flushInterval,
	}
}

// start starts the goroutine unless it runs. When the goroutine of the
// last close still runs, it waits for it to return first, and returns an
// error wrapping ErrStillClosing and ctx's error when ctx ends before.
func (s *sender) start(ctx context.Context) error {
	for {
		s.mu.Lock()

		if s.run != nil {
			s.mu.Unlock()

			return nil
		}

		last := s.last
		if last == nil || isClosed(last.done) {
			s.startLocked()
			s.mu.Unlock()

			return nil
		}

		s.mu.Unlock()

		select {
		case <-last.done:
		case <-ctx.Done():
			return fmt.Errorf("%s: %w: %w", errPrefix, ErrStillClosing, ctx.Err())
		}
	}
}

// startLocked starts a run. The caller holds s.mu.
func (s *sender) startLocked() {
	ctx, cancel := context.WithCancel(context.Background())
	r := &run{
		ctx:     ctx,
		cancel:  cancel,
		queue:   make([]Record, 0, s.queueSize),
		full:    make(chan struct{}, 1),
		flushes: make(chan flushRequest),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
		result:  make(chan error, 1),
	}

	s.run, s.last, s.started = r, nil, true

	go s.loop(r)
}

// isClosed reports whether ch is closed.
func isClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// enqueue queues rec without blocking. When the queue is full, rec is
// counted as dropped and reported. When the sender is stopped, rec is
// counted as dropped, and reported if the sender never started.
func (s *sender) enqueue(rec Record) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	r := s.run
	if r == nil {
		s.dropped.Add(1)

		if !s.started {
			s.reporter.report(ErrNotRunning, errNotRunning)
		}

		return
	}

	r.mu.Lock()

	if len(r.queue) >= s.queueSize {
		r.mu.Unlock()
		s.dropped.Add(1)
		s.reporter.report(ErrQueueFull, errQueueFull)

		return
	}

	r.queue = append(r.queue, rec)

	full := len(r.queue) >= s.batchSize

	r.mu.Unlock()

	if full {
		select {
		case r.full <- struct{}{}:
		default:
		}
	}
}

// flush posts what is queued and waits for it, as described for
// ShippingHandler.Flush.
func (s *sender) flush(ctx context.Context) error {
	s.mu.RLock()

	r := s.run
	s.mu.RUnlock()

	if r == nil {
		return nil
	}

	err := ctx.Err()
	if err != nil {
		return notDelivered(r.pending(), err)
	}

	req := flushRequest{ctx: ctx, result: make(chan error, 1)}

	select {
	case r.flushes <- req:
	case <-r.done:
		return nil
	case <-ctx.Done():
		return notDelivered(r.pending(), ctx.Err())
	}

	select {
	case err := <-req.result:
		return err
	case <-ctx.Done():
		return r.late(req.result, ctx.Err())
	}
}

// close stops accepting records and posts what is queued within ctx, as
// described for ShippingHandler.Close.
func (s *sender) close(ctx context.Context) error {
	s.mu.Lock()

	r := s.run
	if r == nil {
		s.mu.Unlock()

		return nil
	}

	s.run, s.last = nil, r
	r.closeCtx = ctx
	close(r.stop)

	s.mu.Unlock()

	defer r.cancel()

	select {
	case err := <-r.result:
		return err
	case <-ctx.Done():
		return r.late(r.result, ctx.Err())
	}
}

// pending returns how many records are queued or being posted.
func (r *run) pending() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()

	return int64(len(r.queue) + r.inflight)
}

// late returns the error of a drain whose caller's context ended with
// ctxErr: the drain's result when it is in, or else the records still
// pending. The pending count is read first: a drain sends its result
// once it is done with its records, so a count read before a missing
// result still holds the drain's records.
func (r *run) late(result <-chan error, ctxErr error) error {
	pending := r.pending()

	select {
	case err := <-result:
		return err
	default:
		return notDelivered(pending, ctxErr)
	}
}

// take moves the oldest n records of the queue into batch, which it
// returns, and counts them in flight until settle.
func (r *run) take(batch []Record, n int) []Record {
	r.mu.Lock()
	defer r.mu.Unlock()

	n = min(n, len(r.queue))
	batch = append(batch[:0], r.queue[:n]...)

	left := copy(r.queue, r.queue[n:])
	clear(r.queue[left:])

	r.queue = r.queue[:left]

	r.inflight += n

	return batch
}

// putBack returns batch, which was taken, to the front of the queue, and
// reports whether there was room for it. A batch put back is no longer in
// flight.
func (r *run) putBack(batch []Record, queueSize int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	n := len(r.queue)
	if n+len(batch) > queueSize {
		return false
	}

	r.queue = r.queue[:n+len(batch)]
	copy(r.queue[len(batch):], r.queue[:n])
	copy(r.queue, batch)

	r.inflight -= len(batch)

	return true
}

// settle stops counting n records in flight.
func (r *run) settle(n int) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.inflight -= n
}

// dropQueued empties the queue, adds how many records it held to dropped,
// and counts them in flight until settle, and returns their number.
func (r *run) dropQueued(dropped *atomic.Uint64) int {
	r.mu.Lock()
	defer r.mu.Unlock()

	n := len(r.queue)

	dropped.Add(uint64(len(r.queue)))
	clear(r.queue)

	r.queue = r.queue[:0]

	r.inflight += n

	return n
}

// queued returns how many records are queued.
func (r *run) queued() int {
	r.mu.Lock()
	defer r.mu.Unlock()

	return len(r.queue)
}

// loop is the goroutine of r: it posts a batch when one is full, what is
// queued when the flush interval passes, on a flush, and on close.
func (s *sender) loop(r *run) {
	defer close(r.done)

	ticker := time.NewTicker(s.flushInterval)
	defer ticker.Stop()

	batch := make([]Record, 0, s.batchSize)

	for {
		select {
		case <-r.full:
			for r.ctx.Err() == nil && r.queued() >= s.batchSize {
				batch = r.take(batch, s.batchSize)
				s.post(r.ctx, r, batch, false)
				clear(batch)
				r.settle(len(batch))
			}
		case <-ticker.C:
			// The result is the run's own; failures were reported.
			held, _ := s.drain(r.ctx, r, batch, false)
			r.settle(held)
		case req := <-r.flushes:
			held, err := s.drain(req.ctx, r, batch, true)
			req.result <- err

			r.settle(held)
		case <-r.stop:
			// No record enters the queue once stop is closed.
			held, err := s.drain(r.closeCtx, r, batch, false)
			r.result <- err

			r.settle(held)

			return
		}
	}
}

// drain posts the records queued now, in batches, with a context that
// ends with ctx or the run, and returns an error for the records it did
// not deliver, wrapping the context's error when the context ended, after
// how many records it left in flight. When
// the context ends, the records not posted stay queued if keep is set,
// and are otherwise dropped, and counted. batch is the memory the batches
// are built in. The caller settles the records left in flight once it has
// sent the error, so a Flush or Close whose context ends meanwhile still
// counts them.
func (s *sender) drain(ctx context.Context, r *run, batch []Record, keep bool) (int, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// The end of the run ends the drain too.
	stop := context.AfterFunc(r.ctx, cancel) //nolint:contextcheck // r.ctx is the run's.
	defer stop()

	var undelivered int64

	held := 0

	for n := r.queued(); n > 0; {
		if ctx.Err() != nil {
			undelivered += int64(n)

			break
		}

		batch = r.take(batch, min(n, s.batchSize))

		n -= len(batch)

		switch s.post(ctx, r, batch, keep) {
		case posted:
			held += len(batch)
		case failed:
			held += len(batch)
			undelivered += int64(len(batch))
		case requeued:
			undelivered += int64(len(batch))
		}

		clear(batch)
	}

	if ctx.Err() != nil && !keep {
		held += r.dropQueued(&s.dropped)
	}

	return held, notDelivered(undelivered, ctx.Err())
}

// outcome is what became of a batch post took.
type outcome int

// The outcomes of post.
const (
	// posted: the batch was delivered.
	posted outcome = iota
	// failed: the batch was dropped.
	failed
	// requeued: the batch went back to the front of the queue.
	requeued
)

// post marshals and posts batch within ctx, and returns what became of
// it. A batch that fails is counted as dropped and its failure reported.
// A batch whose post ctx ends, before or during it, is not reported: it
// goes back to the front of the queue if keep is set and there is room,
// and is otherwise counted as dropped.
func (s *sender) post(ctx context.Context, r *run, batch []Record, keep bool) outcome {
	var err error

	if ctx.Err() == nil {
		body, ok := s.marshal(batch)
		if !ok {
			s.dropped.Add(uint64(len(batch)))

			return failed
		}

		if ctx.Err() == nil {
			err = s.send(ctx, body)
			if err == nil {
				return posted
			}
		}
	}

	if ctx.Err() != nil {
		if keep && r.putBack(batch, s.queueSize) {
			return requeued
		}

		s.dropped.Add(uint64(len(batch)))

		return failed
	}

	class := ErrPost
	if errors.Is(err, ErrStatus) {
		class = ErrStatus
	}

	s.dropped.Add(uint64(len(batch)))
	s.reporter.report(class, err)

	return failed
}

// marshal encodes batch, reporting a Marshaler's error or panic.
func (s *sender) marshal(batch []Record) (body []byte, ok bool) {
	defer func() {
		// The panic value is left out of the report: it can hold the records.
		if recover() != nil {
			body, ok = nil, false

			s.reporter.report(ErrMarshalPanic, errMarshalPanic)
		}
	}()

	body, err := s.marshaler.Marshal(batch)
	if err != nil {
		s.reporter.report(ErrMarshal, fmt.Errorf("%s: %w: %w", errPrefix, ErrMarshal, err))

		return nil, false
	}

	return body, true
}

// send posts body. Its error wraps ErrPost or ErrStatus, and names the
// endpoint as shown: its scheme and host only.
func (s *sender) send(ctx context.Context, body []byte) error {
	req := s.template.Clone(ctx)

	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}

	resp, err := s.client.Do(req)
	if err != nil {
		if uerr, ok := errors.AsType[*url.Error](err); ok {
			uerr.URL = s.shown
		}

		return fmt.Errorf("%s: %w: %w", errPrefix, ErrPost, err)
	}

	// The body is read so the connection can be reused; what it holds, and
	// a failure to read or close it, does not change the outcome.
	_, _ = io.CopyN(io.Discard, resp.Body, maxDiscard)
	_ = resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("%s: %w %d from %s", errPrefix, ErrStatus, resp.StatusCode, s.shown)
	}

	return nil
}
