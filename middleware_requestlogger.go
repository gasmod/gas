package gas

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"sync"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)

// requestLoggerOptions configures RequestLogger.
type requestLoggerOptions struct {
	// builder customizes the per-request logger, or is nil.
	builder func(Logger, *http.Request) Logger
	// appendRequestID adds chi's request ID as `request_id`.
	appendRequestID bool
}

// derive returns the per-request logger RequestLogger derives from
// logger for r.
func (o *requestLoggerOptions) derive(logger Logger, r *http.Request) Logger {
	if o.appendRequestID {
		logger = logger.With().
			Str("request_id", middleware.GetReqID(r.Context())).
			Logger()
	}

	if o.builder != nil {
		logger = o.builder(logger, r)
	}

	return logger
}

// RequestLoggerOption configures RequestLogger.
type RequestLoggerOption func(*requestLoggerOptions)

// WithRequestLoggerAppendRequestID sets whether the per-request logger
// carries chi's request ID as `request_id`, which needs chi's RequestID
// middleware upstream.
//
// Default: true.
func WithRequestLoggerAppendRequestID(val bool) RequestLoggerOption {
	return func(opt *requestLoggerOptions) { opt.appendRequestID = val }
}

// WithRequestLoggerBuilder sets a function that derives the per-request
// logger from the resolved one, after the request ID is added, such as to
// add fields. A nil function adds nothing.
//
// Default: nil.
func WithRequestLoggerBuilder(fn func(Logger, *http.Request) Logger) RequestLoggerOption {
	return func(opt *requestLoggerOptions) { opt.builder = fn }
}

// requestErrors collects the errors the default ErrorHandler handled for
// one request, so RequestLogger logs them on the request's line.
type requestErrors struct {
	// err holds them, joined; guarded by mu.
	err error
	mu  sync.Mutex
}

// requestErrorsKey is the context key of a request's requestErrors.
type requestErrorsKey struct{}

// recordRequestError adds err to the request's requestErrors and reports
// whether there were any, which is when RequestLogger logs it.
func recordRequestError(r *http.Request, err error) bool {
	re, ok := r.Context().Value(requestErrorsKey{}).(*requestErrors)
	if !ok {
		return false
	}

	re.mu.Lock()

	re.err = errors.Join(re.err, err)
	re.mu.Unlock()

	return true
}

// RequestLogger is middleware that logs each request, as described under
// Middleware in the package documentation. It resolves T from the request
// scope, derives a per-request logger from it with With, and puts that on
// the request context with WithLogger, so handlers, later middleware and
// the default ErrorHandler log through it. The resolved logger itself is
// never changed, so a singleton is safe.
//
// When T is registered scoped, the per-request logger also takes T's
// place in the request scope, so what resolves T later in the request
// gets it. A singleton T, an alias T, and a scoped T whose instance is a
// Service are left in place. A scoped T the per-request logger is not
// assignable to is left in place too, and a warning says so once.
//
// When the request ends it logs one line, `request`, with the method,
// path, status, bytes written, duration and remote address, and the
// errors the default ErrorHandler handled: at info level below 400, warn
// below 500, and error from 500. When T cannot be resolved, it passes the
// request on and logs nothing.
func RequestLogger[T Logger](opt ...RequestLoggerOption) func(next http.Handler) http.Handler {
	options := &requestLoggerOptions{appendRequestID: true}
	for _, o := range opt {
		o(options)
	}

	t := reflect.TypeFor[T]()

	var warnOnce sync.Once

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			resolved, err := ResolveFromRequestScope[T](r)
			if err != nil {
				next.ServeHTTP(w, r)

				return
			}

			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			start := time.Now()

			logger := options.derive(resolved, r)

			if t := trackerOf(w); t != nil {
				t.logger = logger
			}

			errs := &requestErrors{}
			ctx := context.WithValue(WithLogger(r.Context(), logger), requestErrorsKey{}, errs)

			r = r.WithContext(ctx)

			if RequestScope(r).replaceScoped(t, reflect.ValueOf(logger)) == replaceNotAssignable {
				warnOnce.Do(func() {
					logger.Warn("request logger cannot replace the scoped logger type").
						Str("type", t.String()).
						Send()
				})
			}

			defer func() {
				rec := recover()
				if rec == nil {
					logRequest(logger, r, ww.Status(), ww, start, errs)

					return
				}

				// A panic below is answered with a 500 by the router, which
				// recovers it once this returns.
				logRequest(logger, r, http.StatusInternalServerError, ww, start, errs)
				panic(rec)
			}()

			next.ServeHTTP(ww, r)
		})
	}
}

// logRequest logs the line RequestLogger writes when a request ends.
func logRequest(
	logger Logger,
	r *http.Request,
	status int,
	ww middleware.WrapResponseWriter,
	start time.Time,
	errs *requestErrors,
) {
	var ev LogEvent

	switch {
	case status >= http.StatusInternalServerError:
		ev = logger.Error("request")
	case status >= http.StatusBadRequest:
		ev = logger.Warn("request")
	default:
		ev = logger.Info("request")
	}

	ev = ev.Str("method", r.Method).
		Str("path", r.URL.Path).
		Int("status", status).
		Int("bytes", ww.BytesWritten()).
		Duration("duration", time.Since(start)).
		Str("remote", r.RemoteAddr)

	errs.mu.Lock()

	err := errs.err
	errs.mu.Unlock()

	if err != nil {
		ev = ev.Err("error", err)
	}

	ev.Send()
}
