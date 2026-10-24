// Package main shows structured logging and log shipping with gas/log.
package main

// #region imports
import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"log/slog"
	"net/http"
	"os"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/rs/zerolog"

	"github.com/gasmod/gas"
	gaslog "github.com/gasmod/gas/log"
)

// #endregion imports

// #region wiring
// RequestLogger is the per-request logger type. gas.Logger stays a
// singleton; RequestLogger is scoped and derived from it.
type RequestLogger interface{ gas.Logger }

func main() {
	app := gas.NewApp(
		gas.WithSingletonService[gas.Logger](gaslog.NewSlogLogger()),
		gas.WithScopedService[RequestLogger](func(l gas.Logger) RequestLogger {
			return l.With().Logger()
		}),
		gas.WithSingletonService[*Notes](NewNotes),
	)

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

// #endregion wiring

// #region backends
// zerolog writes JSON and supports every level, Trace included.
func zerologBackend() gaslog.ZeroLogLoggerCtor {
	zl := zerolog.New(os.Stdout).With().Timestamp().Logger()
	return gaslog.NewZeroLogLogger(gaslog.WithZeroLogInstance(&zl))
}

// slog's default logger drops Debug records, so give it a handler at the
// level you want. Trace logs at slog's Debug level.
func slogBackend() gaslog.SlogLoggerCtor {
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug})
	return gaslog.NewSlogLogger(gaslog.WithSlogInstance(slog.New(h)))
}

// #endregion backends

// #region fluent
func fluent(logger gas.Logger) {
	logger.Info("note created").
		Str("note_id", "n-42").
		Int("size", 512).
		Bool("pinned", true).
		Send()

	// With derives a new logger with persistent fields; logger is unchanged.
	notes := logger.With().Str("service", "notes").Logger()
	notes.Debug("cache miss").Str("key", "notes:n-42").Send()
}

// #endregion fluent

// Notes installs the request logging middleware and serves the notes routes.
type Notes struct {
	router *gas.Router
}

func NewNotes(router *gas.Router) *Notes { return &Notes{router: router} }

func (s *Notes) Name() string                { return "notes" }
func (s *Notes) Close(context.Context) error { return nil }

func (s *Notes) Init(context.Context) error {
	// #region request-logger
	// chi's RequestID runs first, so the request logger can add request_id.
	// RequestLogger[T] resolves T from the request scope, derives a logger with
	// the request ID, puts it on the request context and, because T is scoped,
	// in the scope too.
	s.router.UseMiddlewareFunc(middleware.RequestID)
	s.router.UseMiddlewareFunc(gas.RequestLogger[RequestLogger]())
	s.router.UseMiddlewareFunc(attachUser)

	s.router.Get(s, "/notes", listNotes)
	// #endregion request-logger

	return nil
}

// #region list-notes
// listNotes takes the request's logger as a dependency; it carries
// request_id.
func listNotes(ctx gas.Context, logger RequestLogger) error {
	logger.Info("listing notes").Send()
	return ctx.JSON(http.StatusOK, []string{})
}

// #endregion list-notes

// #region request-fields
// attachUser adds a field for everything downstream that logs through
// gas.LoggerFromContext. It builds on the logger RequestLogger put on the
// context and stores the result back there; no logger is changed in place.
func attachUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base := gas.LoggerFromContext(r.Context())
		if base == nil {
			next.ServeHTTP(w, r)
			return
		}
		logger := base.With().Str("user_id", r.Header.Get("X-User-Id")).Logger()
		next.ServeHTTP(w, r.WithContext(gas.WithLogger(r.Context(), logger)))
	})
}

// #endregion request-fields

// #region shipping
// Info and above is written locally as JSON on stderr; Warn and above is
// also shipped in batches. Registered in the container, the logger is closed
// at shutdown, which posts what is still queued.
func shippingLogger(endpoint, apiKey string) gas.Option {
	local := slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})

	return gas.WithSingletonService[gas.Logger](gaslog.NewShippingLogger(
		endpoint,
		gaslog.NewOTLPMarshaler(
			gaslog.WithServiceName("notes"),
			gaslog.WithServiceVersion("1.4.2"),
		),
		gaslog.WithHeader("Authorization", "Bearer "+apiKey),
		gaslog.WithLevel(slog.LevelWarn),
		gaslog.WithLocalHandler(local),
		gaslog.WithErrorHandler(func(err error) {
			log.Printf("log shipping: %v", err)
		}),
	))
}

// #endregion shipping

// #region shipping-handler
// The same pipeline for code that uses slog directly. Close posts what is
// queued and stops the delivery goroutine.
func shipSlog(endpoint string) (closeFn func() error) {
	h, closer := gaslog.NewShippingHandler(endpoint, gaslog.NewOTLPMarshaler(
		gaslog.WithServiceName("notes"),
	))
	slog.SetDefault(slog.New(h))
	return closer.Close
}

// #endregion shipping-handler

// #region marshaler
// ndjson sends a batch as newline-delimited JSON objects.
type ndjson struct{}

func (ndjson) ContentType() string { return "application/x-ndjson" }

func (ndjson) Marshal(records []gaslog.Record) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, r := range records {
		line := map[string]any{"time": r.Time, "level": r.Level.String(), "msg": r.Message}
		for _, a := range r.Attrs {
			line[a.Key] = a.Value.Any()
		}
		if err := enc.Encode(line); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

// #endregion marshaler

var _ gaslog.Marshaler = ndjson{}
