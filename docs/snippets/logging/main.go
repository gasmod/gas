// Package main shows structured logging.
package main

// #region imports
import (
	"net/http"

	"github.com/gasmod/gas"
	gaslog "github.com/gasmod/gas/log"
)

// #endregion imports

// #region register
// gas.Logger is a singleton: services take it in their constructors, and the
// container rejects a singleton that depends on a scoped service. Per-request
// fields go on a separate scoped type derived from it, which the request
// middleware fills with the request's logger.
type RequestLogger interface{ gas.Logger }

func register() []gas.Option {
	return []gas.Option{
		gas.WithSingletonService[gas.Logger](gaslog.NewSlogLogger()),
		gas.WithScopedService[RequestLogger](func(l gas.Logger) RequestLogger {
			return l.With().Logger()
		}),
	}
}

// #endregion register

// #region fluent
func fluent(logger gas.Logger) {
	logger.Info("request handled").
		Str("method", "GET").
		Str("path", "/api/notes").
		Int("status", 200).
		Send()

	// With branches into a new logger carrying persistent fields.
	sub := logger.With().Str("service", "notes").Logger()
	sub.Debug("cache miss").Send()
}

// #endregion fluent

// #region request-fields
// Middleware derives a logger with the request's fields and puts it on the
// request context, so everything downstream that logs through
// gas.LoggerFromContext carries them. The shared logger is never changed.
// Mount it after gas.RequestLogger to build on the logger that carries the
// request ID.
func attachRequestFields(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base := gas.LoggerFromContext(r.Context())
		if base == nil {
			base = gas.MustResolveFromRequestScope[gas.Logger](r)
		}
		logger := base.With().
			Str("user_id", "u-123").
			Str("trace_id", r.Header.Get("X-Trace-Id")).
			Logger()
		next.ServeHTTP(w, r.WithContext(gas.WithLogger(r.Context(), logger)))
	})
}

// handler logs through the request's logger.
func handler(w http.ResponseWriter, r *http.Request) {
	gas.LoggerFromContext(r.Context()).Info("note created").Send()
	w.WriteHeader(http.StatusCreated)
}

// #endregion request-fields

// #region request-logger
// The built-in request logger records method, path, status, bytes, duration
// and remote address. Responses at 400 and above log at error level. It also
// derives a logger carrying the request ID and puts it in the request scope as
// RequestLogger and on the request context.
func requestLogging(router *gas.Router) {
	router.UseMiddlewareFunc(gas.RequestLogger[RequestLogger]())
}

// injected takes the request's logger as a DI parameter; it carries the
// request ID.
func injected(_ gas.Context, logger RequestLogger) error {
	logger.Info("note listed").Send()
	return nil
}

// #endregion request-logger

// #region shipping
// Ships every record to an HTTP endpoint as well as writing locally. Delivery
// is best-effort: records are dropped rather than blocking the caller, and
// failures go to WithErrorHandler instead of the logging call site.
func shipping(endpoint, apiKey string) gas.Logger {
	return gaslog.NewShippingLogger(
		endpoint,
		gaslog.NewOTLPMarshaler(
			gaslog.WithServiceName("notes"),
			gaslog.WithServiceVersion("1.4.2"),
		),
		gaslog.WithHeader("X-API-Key", apiKey),
		gaslog.WithBatchSize(100),
	)()
}

// #endregion shipping

func main() {}
