package gas

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"sync"
	"time"
)

// BodyLimit is middleware that caps the request body at n bytes, as
// described under Middleware in the package documentation. A request
// whose Content-Length is over n is answered with a 413 *Error with code
// CodePayloadTooLarge in the unified error shape, and its handler does not
// run. Otherwise the body is read through http.MaxBytesReader, so reading
// past n fails with *http.MaxBytesError, which Context.BindJSON and
// Context.BindForm turn into the same 413. An n that is not positive
// panics with an error wrapping ErrInvalidOption.
func BodyLimit(n int64) func(next http.Handler) http.Handler {
	if n <= 0 {
		panic(fmt.Errorf("%w: BodyLimit must be positive", ErrInvalidOption))
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > n {
				_ = WriteError(w, r, NewError(
					http.StatusRequestEntityTooLarge,
					CodePayloadTooLarge,
					"request body is too large",
				))

				return
			}

			r.Body = http.MaxBytesReader(w, r.Body, n)
			next.ServeHTTP(w, r)
		})
	}
}

// DefaultTimeoutMaxBuffer is the most bytes of a response Timeout
// buffers unless WithTimeoutMaxBuffer sets another: 8 MiB.
const DefaultTimeoutMaxBuffer = 8 << 20

// ErrResponseTooLarge indicates a write that would take a response under
// Timeout past its buffer cap. The write fails, and the client gets a 503
// instead of the response.
var ErrResponseTooLarge = errors.New("gas: response too large for the timeout buffer")

// timeoutOptions configures Timeout.
type timeoutOptions struct {
	// maxBuffer caps the buffered response.
	maxBuffer int64
}

// TimeoutOption configures Timeout.
type TimeoutOption func(*timeoutOptions)

// WithTimeoutMaxBuffer sets the most bytes of a response Timeout buffers.
// A value that is not positive makes Timeout panic with an error wrapping
// ErrInvalidOption.
//
// Default: DefaultTimeoutMaxBuffer.
func WithTimeoutMaxBuffer(n int64) TimeoutOption {
	return func(o *timeoutOptions) { o.maxBuffer = n }
}

// Timeout is middleware that gives each request d to be served, as
// described under Middleware in the package documentation. It is for
// handlers with bounded responses, not streams. The handler runs with a
// context that ends after d, and writes into a buffer of at most the
// WithTimeoutMaxBuffer cap that is sent when it returns. When d passes
// first, the client gets a 503 *Error with code CodeTimeout in the unified
// error shape at once, and what the handler writes after is discarded;
// its writes fail with http.ErrHandlerTimeout. When the request's own
// context ends first, the client gets a 503 with code CodeUnavailable. A
// write past the cap fails with ErrResponseTooLarge, and the client gets a
// 503 with code CodeUnavailable in place of the response. Either way the
// request ends only once the handler returns, so the handler must honor
// its context. A panic in the handler is panicked again on the request's
// goroutine, so the router recovers it. Like http.TimeoutHandler, it does
// not support streaming, flushing or hijacking. A d that is not positive
// panics with an error wrapping ErrInvalidOption.
func Timeout(d time.Duration, opt ...TimeoutOption) func(next http.Handler) http.Handler {
	if d <= 0 {
		panic(fmt.Errorf("%w: Timeout must be positive", ErrInvalidOption))
	}

	o := &timeoutOptions{maxBuffer: DefaultTimeoutMaxBuffer}
	for _, apply := range opt {
		apply(o)
	}

	if o.maxBuffer <= 0 {
		panic(fmt.Errorf("%w: WithTimeoutMaxBuffer must be positive", ErrInvalidOption))
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()

			r = r.WithContext(ctx)

			tw := &timeoutWriter{header: make(http.Header), maxBuffer: o.maxBuffer}
			done := make(chan struct{})
			panicked := make(chan any, 1)

			go func() {
				defer func() {
					if rec := recover(); rec != nil {
						panicked <- rec

						return
					}

					close(done)
				}()

				next.ServeHTTP(tw, r)
			}()

			select {
			case rec := <-panicked:
				panic(rec)
			case <-done:
				tw.flushTo(w, r)

				return
			case <-ctx.Done():
				tw.expire(w, r, ctx.Err())
			}

			// The answer is sent; the request ends once the handler
			// returns, as what it holds, such as the request scope, ends
			// with the request.
			select {
			case rec := <-panicked:
				panic(rec)
			case <-done:
			}
		})
	}
}

// timeoutWriter is the ResponseWriter of a handler under Timeout. It
// buffers the response until the handler returns.
type timeoutWriter struct {
	// header is the handler's header map.
	header http.Header
	// buf is the body written so far.
	buf bytes.Buffer
	// mu guards the fields below and buf.
	mu sync.Mutex
	// maxBuffer caps buf.
	maxBuffer int64
	// code is the status written, if wrote.
	code int
	// wrote is set once a status was written, timedOut once the request
	// ran out of time, and overflowed once a write passed maxBuffer.
	wrote      bool
	timedOut   bool
	overflowed bool
}

// Header returns the handler's header map.
func (tw *timeoutWriter) Header() http.Header { return tw.header }

// Write buffers b, or fails once the request ran out of time.
func (tw *timeoutWriter) Write(b []byte) (int, error) {
	tw.mu.Lock()
	defer tw.mu.Unlock()

	if tw.timedOut {
		return 0, http.ErrHandlerTimeout
	}

	if !tw.wrote {
		tw.code, tw.wrote = http.StatusOK, true
	}

	if tw.overflowed || int64(tw.buf.Len())+int64(len(b)) > tw.maxBuffer {
		tw.overflowed = true

		return 0, ErrResponseTooLarge
	}

	//nolint:wrapcheck // a bytes.Buffer write does not fail
	return tw.buf.Write(b)
}

// WriteHeader records the first final status.
func (tw *timeoutWriter) WriteHeader(code int) {
	tw.mu.Lock()
	defer tw.mu.Unlock()

	if tw.timedOut || tw.wrote || code < http.StatusOK {
		return
	}

	tw.code, tw.wrote = code, true
}

// responseCommitted reports whether the handler wrote a status or a body.
func (tw *timeoutWriter) responseCommitted() bool {
	tw.mu.Lock()
	defer tw.mu.Unlock()

	return tw.wrote
}

// flushTo sends the buffered response to w, or a 503 when it overflowed
// the buffer.
func (tw *timeoutWriter) flushTo(w http.ResponseWriter, r *http.Request) {
	tw.mu.Lock()
	defer tw.mu.Unlock()

	if tw.overflowed {
		_ = WriteError(
			w,
			r,
			ServiceUnavailable("response too large").WithCause(ErrResponseTooLarge),
		)

		return
	}

	dst := w.Header()
	maps.Copy(dst, tw.header)

	if !tw.wrote {
		tw.code = http.StatusOK
	}

	w.WriteHeader(tw.code)

	// A failed write means the client went away; there is no one to
	// tell.
	_, _ = w.Write(tw.buf.Bytes())
}

// expire answers a request whose context ended before its handler
// returned, and discards the handler's later writes.
func (tw *timeoutWriter) expire(w http.ResponseWriter, r *http.Request, cause error) {
	tw.mu.Lock()

	tw.timedOut = true
	tw.mu.Unlock()

	e := ServiceUnavailable("request canceled").WithCause(cause)
	if errors.Is(cause, context.DeadlineExceeded) {
		e = NewError(
			http.StatusServiceUnavailable,
			CodeTimeout,
			"request timed out",
		).WithCause(cause)
	}

	_ = WriteError(w, r, e)

	// Send the answer now: the request goes on until the handler returns.
	// A writer that cannot flush sends it then.
	_ = http.NewResponseController(w).Flush()
}
