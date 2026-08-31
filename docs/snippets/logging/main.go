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
// A singleton logger is shared. Registering it scoped instead gives each
// request its own instance, which is what lets middleware attach request
// fields without leaking them across requests.
func register() []gas.Option {
	return []gas.Option{
		gas.WithSingletonService[gas.Logger](gaslog.NewSlogLogger()),
		gas.WithScopedService[gas.Logger](gaslog.NewZeroLogLogger()),
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

// #region base-fields
// SetBaseFields mutates the receiver instead of branching, so everything
// logged later in the same request carries these fields, including from
// handlers that never saw this middleware.
func attachRequestFields(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger := gas.MustResolveFromRequestScope[gas.Logger](r)
		logger.SetBaseFields().
			Str("user_id", "u-123").
			Str("trace_id", r.Header.Get("X-Trace-Id")).
			Apply()
		next.ServeHTTP(w, r)
	})
}

// #endregion base-fields

// #region request-logger
// The built-in request logger records method, path, status, bytes, duration
// and remote address. Responses at 400 and above log at error level.
func requestLogging(router *gas.Router) {
	router.UseMiddlewareFunc(gas.RequestLogger[gas.Logger]())
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
