package gas

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"reflect"
	"runtime/debug"
	"sync/atomic"

	"github.com/go-playground/validator/v10"
	"github.com/gorilla/schema"
)

// Errors of handlers, as described under Handlers in the
// package documentation.
var (
	// ErrInvalidHandler indicates a handler passed to Handle or NotFound
	// whose shape is none of those Handle takes. Registration panics with
	// an error wrapping it.
	ErrInvalidHandler = errors.New("gas: invalid handler")

	// ErrPanic indicates a panic that gas recovered, in a handler, a
	// middleware, an event subscriber or a health or readiness check. The
	// error names where, and holds the panic value.
	ErrPanic = errors.New("gas: panic")
)

// ErrorHandler turns an error a handler returned, or a recovered panic,
// into a response. The default logs the error, as described under
// Handlers and errors in the package documentation, and writes it with
// WriteError.
type ErrorHandler func(ctx Context, err error)

// defaultErrorHandler logs err, unless RequestLogger will log it with the
// request, and writes it with WriteError. An error after the response was
// committed is logged, never written.
func defaultErrorHandler(ctx Context, err error) {
	e := coerceError(err)
	r := ctx.Request()

	wErr := WriteError(ctx.ResponseWriter(), r, e)
	if errors.Is(wErr, ErrResponseCommitted) {
		logger := requestLogger(r)
		if logger == nil {
			logger = fallbackLogger(r)
		}

		if logger != nil {
			logger.Error("error after the response was committed").Err("error", err).Send()
		}

		return
	}

	logRequestError(r, err, normalizeStatus(e.Status) >= http.StatusInternalServerError, wErr)
}

// logRequestError logs err, unless RequestLogger will log it with the
// request, and wErr, the error of writing it: through the request's
// logger, or, for a server fault, which is logged even with no Logger
// registered, through the App's.
func logRequestError(r *http.Request, err error, serverFault bool, wErr error) {
	logger := requestLogger(r)
	if logger == nil && serverFault {
		logger = fallbackLogger(r)
	}

	if logger == nil {
		return
	}

	if !recordRequestError(r, err) {
		// A 4xx is the client's mistake, not a server fault.
		if serverFault {
			logger.Error("request error").Err("error", err).Send()
		} else {
			logger.Warn("request error").Err("error", err).Send()
		}
	}

	if wErr != nil {
		logger.Error("failed to write error response").Err("error", wErr).Send()
	}
}

// requestLogger returns the logger for r: the one RequestLogger put on its
// context, or the Logger of its request scope, or nil when it has
// neither.
func requestLogger(r *http.Request) Logger {
	if l := LoggerFromContext(r.Context()); l != nil {
		return l
	}

	scope, ok := r.Context().Value(requestScopeKey{}).(*Scope)
	if !ok {
		return nil
	}

	logger, err := resolveFrom[Logger](r.Context(), scope)
	if err != nil {
		return nil
	}

	return logger
}

// fallbackLogger returns the logger of r's container for a request with
// no Logger of its own: an App's logger, over slog.Default when no Logger
// is registered. It returns nil for a request with no request scope.
func fallbackLogger(r *http.Request) Logger {
	scope, ok := r.Context().Value(requestScopeKey{}).(*Scope)
	if !ok || scope.container.fallbackLogger == nil {
		return nil
	}

	return scope.container.fallbackLogger()
}

// handlerMeta holds the reflection data of a DI-aware handler.
type handlerMeta struct {
	// fn is the handler.
	fn reflect.Value
	// depTypes are its parameter types after the Context.
	depTypes []reflect.Type
}

// adaptHandler checks a DI-aware handler's signature and returns an
// http.HandlerFunc that resolves its dependencies from the request scope
// on each call, and the dependency types for Start to check:
//
//	func(gas.Context) error
//	func(gas.Context, Dep1, Dep2, ...) error
//
// It panics with an error wrapping ErrInvalidHandler for another shape.
func adaptHandler(
	handler any,
	getErrorHandler func() ErrorHandler,
	fallback func() Logger,
	validate *validator.Validate,
	formDecoder *schema.Decoder,
	maxJSONBody func() int64,
	maxFormBody func() int64,
) (http.HandlerFunc, []reflect.Type) {
	err := checkHandler(handler)
	if err != nil {
		panic(err)
	}

	handlerVal := reflect.ValueOf(handler)
	handlerType := handlerVal.Type()

	depTypes := make([]reflect.Type, handlerType.NumIn()-1)
	for i := range depTypes {
		depTypes[i] = handlerType.In(i + 1)
	}

	meta := &handlerMeta{fn: handlerVal, depTypes: depTypes}

	return adaptedHandler(
		getErrorHandler,
		fallback,
		validate,
		formDecoder,
		maxJSONBody,
		maxFormBody,
		meta,
	), depTypes
}

// checkHandler returns an error wrapping ErrInvalidHandler when handler is
// not a func(Context, Deps...) returning one value that implements
// error.
func checkHandler(handler any) error {
	t := reflect.TypeOf(handler)
	if t == nil || t.Kind() != reflect.Func {
		return fmt.Errorf("%w: %T is not a function", ErrInvalidHandler, handler)
	}

	if t.IsVariadic() {
		return fmt.Errorf("%w: %v is variadic", ErrInvalidHandler, t)
	}

	if t.NumIn() < 1 || t.In(0) != reflect.TypeFor[Context]() {
		return fmt.Errorf("%w: %v does not take gas.Context first", ErrInvalidHandler, t)
	}

	if t.NumOut() != 1 || !t.Out(0).Implements(reflect.TypeFor[error]()) {
		return fmt.Errorf("%w: %v does not return only error", ErrInvalidHandler, t)
	}

	return nil
}

// handlePanic turns a value recovered from a handler or middleware into a
// response through the error handler, after logging it with its stack:
// through the request's logger, else fallback's, else to stderr.
// http.ErrAbortHandler is panicked again, so net/http aborts the request.
func handlePanic(
	rec any,
	ctx Context,
	r *http.Request,
	getErrorHandler func() ErrorHandler,
	fallback func() Logger,
) {
	recErr, isErr := rec.(error)
	if isErr && errors.Is(recErr, http.ErrAbortHandler) {
		panic(rec)
	}

	err := fmt.Errorf("%w in handler: %v", ErrPanic, rec)
	stack := debug.Stack()

	logger := requestLogger(r)
	if logger == nil && fallback != nil {
		logger = fallback()
	}

	if logger != nil {
		logger.Error("handler panic").Err("error", err).Str("stack", string(stack)).Send()
	} else {
		_, _ = os.Stderr.Write(stack)
	}

	getErrorHandler()(ctx, err)
}

// recoverPlain wraps a plain http.HandlerFunc with the panic recovery a
// DI-aware handler gets. The Context for the error handler is built only
// when the handler panics.
func recoverPlain(
	h http.HandlerFunc,
	getErrorHandler func() ErrorHandler,
	fallback func() Logger,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		//nolint:contextcheck // the Context is built from the request's own context
		defer func() {
			if rec := recover(); rec != nil {
				ctx := NewContext(r.Context(), w, r)
				handlePanic(rec, ctx, r, getErrorHandler, fallback)
			}
		}()

		h(w, r)
	}
}

// adaptedHandler returns the http.HandlerFunc of a DI-aware handler.
func adaptedHandler(
	getErrorHandler func() ErrorHandler,
	fallback func() Logger,
	validate *validator.Validate,
	formDecoder *schema.Decoder,
	maxJSONBody func() int64,
	maxFormBody func() int64,
	meta *handlerMeta,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := NewContext(r.Context(), w, r,
			WithValidate(validate),
			WithFormDecoder(formDecoder),
			WithMaxJSONBodyBytes(maxJSONBody()),
			WithMaxFormBodyBytes(maxFormBody()),
		)

		defer func() {
			if rec := recover(); rec != nil {
				handlePanic(rec, ctx, r, getErrorHandler, fallback)
			}
		}()

		args := make([]reflect.Value, 1+len(meta.depTypes))

		args[0] = reflect.ValueOf(ctx)

		if len(meta.depTypes) > 0 {
			scope := RequestScope(r)

			for i, depType := range meta.depTypes {
				val, err := scope.resolveType(r.Context(), depType)
				if err != nil {
					getErrorHandler()(ctx, resolveError(depType, err))

					return
				}

				args[i+1] = val
			}
		}

		results := meta.fn.Call(args)

		if errVal := results[0]; !errVal.IsNil() {
			//nolint:forcetypeassert // checkHandler requires the result to implement error
			getErrorHandler()(ctx, errVal.Interface().(error))
		}
	}
}

// resolveError is the error of a handler dependency t that did not
// resolve, wrapped. A service that is not active is a 503, through
// coerceError, as its own routes answer.
func resolveError(t reflect.Type, err error) error {
	return fmt.Errorf("gas: resolve %v: %w", t, err)
}

// responseTracker is the ResponseWriter the Router serves with. It
// records whether the response is committed, so an error after that is
// logged rather than written.
type responseTracker struct {
	http.ResponseWriter

	// logger is the per-request logger RequestLogger derived, for a panic
	// the router recovers outside it; set and read on the request's
	// goroutine.
	logger Logger

	// wrote is set once a status other than 1xx, or a byte, was written.
	wrote atomic.Bool
}

// WriteHeader records a final status and passes it on.
func (t *responseTracker) WriteHeader(code int) {
	if code >= http.StatusOK || code == http.StatusSwitchingProtocols {
		t.wrote.Store(true)
	}

	t.ResponseWriter.WriteHeader(code)
}

// Write records the commit and passes b on.
func (t *responseTracker) Write(b []byte) (int, error) {
	t.wrote.Store(true)

	//nolint:wrapcheck // the writer's error is the caller's, as from any ResponseWriter
	return t.ResponseWriter.Write(b)
}

// Flush records the commit and flushes the writer underneath, when it
// can.
func (t *responseTracker) Flush() {
	t.wrote.Store(true)

	// A writer that cannot flush leaves nothing to do; http.Flusher has no
	// way to report it.
	_ = http.NewResponseController(t.ResponseWriter).Flush()
}

// Hijack records the commit and hijacks the connection, when the writer
// underneath can.
func (t *responseTracker) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	t.wrote.Store(true)

	//nolint:wrapcheck // net/http's error, as from the writer itself
	return http.NewResponseController(t.ResponseWriter).Hijack()
}

// ReadFrom records the commit and copies src to the writer underneath, so
// net/http can still send a file without copying it through memory.
func (t *responseTracker) ReadFrom(src io.Reader) (int64, error) {
	t.wrote.Store(true)

	//nolint:wrapcheck // the copy's error, as from the writer itself
	return io.Copy(t.ResponseWriter, src)
}

// Unwrap returns the writer underneath, for http.ResponseController.
func (t *responseTracker) Unwrap() http.ResponseWriter { return t.ResponseWriter }

// responseCommitted reports whether the response is committed.
func (t *responseTracker) responseCommitted() bool { return t.wrote.Load() }

// trackerOf returns the Router's writer under w, found through Unwrap, or
// nil.
func trackerOf(w http.ResponseWriter) *responseTracker {
	for w != nil {
		if t, ok := w.(*responseTracker); ok {
			return t
		}

		u, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return nil
		}

		w = u.Unwrap()
	}

	return nil
}

// committer is a ResponseWriter that knows whether its response is
// committed.
type committer interface {
	responseCommitted() bool
}

// committed reports whether w's response is committed, as far as a
// writer gas made, found through Unwrap, knows.
func committed(w http.ResponseWriter) bool {
	for w != nil {
		if c, ok := w.(committer); ok {
			return c.responseCommitted()
		}

		u, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return false
		}

		w = u.Unwrap()
	}

	return false
}
