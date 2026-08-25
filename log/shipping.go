//nolint:revive // intentional package name
package log

import (
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/gasmod/gas"
)

// Default shipping-logger settings.
const (
	defaultBatchSize   = 100
	defaultQueueSize   = 1024
	defaultEventCap    = 5
	defaultFlushEvery  = 2 * time.Second
	defaultHTTPTimeout = 10 * time.Second
	defaultServiceName = "gas/log-shipping"
)

// shippingConfig holds the resolved options for a shipping logger.
type shippingConfig struct {
	localHandler slog.Handler
	client       *http.Client
	onError      func(error)
	level        slog.Leveler
	headers      map[string]string
	name         string
	batchSize    int
	queueSize    int
	eventCap     int
	flushEvery   time.Duration
	disableLocal bool
}

func defaultShippingConfig() shippingConfig {
	return shippingConfig{
		client:     &http.Client{Timeout: defaultHTTPTimeout},
		level:      slog.LevelInfo,
		headers:    map[string]string{},
		name:       defaultServiceName,
		batchSize:  defaultBatchSize,
		queueSize:  defaultQueueSize,
		eventCap:   defaultEventCap,
		flushEvery: defaultFlushEvery,
	}
}

// ShippingOption configures a shipping logger built by [NewShippingLogger].
type ShippingOption func(*shippingConfig)

// WithHeader sets a request header sent on every batch (e.g. an API key).
func WithHeader(key, value string) ShippingOption {
	return func(c *shippingConfig) { c.headers[key] = value }
}

// WithBatchSize sets how many records accumulate before a batch is sent.
func WithBatchSize(n int) ShippingOption {
	return func(c *shippingConfig) {
		if n > 0 {
			c.batchSize = n
		}
	}
}

// WithQueueSize sets the buffered-record capacity; records are dropped when the
// queue is full, so the logging path never blocks.
func WithQueueSize(n int) ShippingOption {
	return func(c *shippingConfig) {
		if n > 0 {
			c.queueSize = n
		}
	}
}

// WithFlushInterval sets the maximum time a record waits before delivery.
func WithFlushInterval(d time.Duration) ShippingOption {
	return func(c *shippingConfig) {
		if d > 0 {
			c.flushEvery = d
		}
	}
}

// WithHTTPClient overrides the HTTP client used for delivery.
func WithHTTPClient(client *http.Client) ShippingOption {
	return func(c *shippingConfig) {
		if client != nil {
			c.client = client
		}
	}
}

// WithLevel sets the minimum level shipped remotely.
func WithLevel(level slog.Leveler) ShippingOption {
	return func(c *shippingConfig) {
		if level != nil {
			c.level = level
		}
	}
}

// WithLocalHandler sets the handler that also receives every record locally
// (for stdout/file logging alongside shipping).
func WithLocalHandler(h slog.Handler) ShippingOption {
	return func(c *shippingConfig) { c.localHandler = h }
}

// WithoutLocalHandler disables local logging, shipping records only.
func WithoutLocalHandler() ShippingOption {
	return func(c *shippingConfig) { c.disableLocal = true }
}

// WithErrorHandler registers a callback invoked on delivery failures (marshal,
// transport, or non-2xx responses). Delivery errors never reach the log call.
func WithErrorHandler(fn func(error)) ShippingOption {
	return func(c *shippingConfig) { c.onError = fn }
}

// WithName sets the gas.Service name reported by the logger.
func WithName(name string) ShippingOption {
	return func(c *shippingConfig) {
		if name != "" {
			c.name = name
		}
	}
}

// ShippingLoggerCtor constructs a [ShippingLogger]; it satisfies the Gas DI
// container's constructor shape.
type ShippingLoggerCtor func() *ShippingLogger

// ShippingLogger is a [gas.Logger] that writes locally and ships every record
// to an HTTP endpoint via a pluggable [Marshaler]. It reuses [SlogLogger] for
// the fluent API and adds batching HTTP delivery underneath.
//
// It implements [gas.Service]: when registered in the DI container, Close is
// called at shutdown and drains any buffered records.
type ShippingLogger struct {
	*SlogLogger
	handler *Handler
	name    string
}

var (
	_ gas.Logger  = (*ShippingLogger)(nil)
	_ gas.Service = (*ShippingLogger)(nil)
)

// buildShipping resolves options and constructs the sender, its shipping
// handler, and the composed handler that tees local logging with delivery.
func buildShipping(endpoint string, marshaler Marshaler, opts []ShippingOption) (composed slog.Handler, ship *Handler, cfg shippingConfig) {
	cfg = defaultShippingConfig()
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.localHandler == nil && !cfg.disableLocal {
		cfg.localHandler = slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.level})
	}
	ship = &Handler{sender: newSender(endpoint, marshaler, cfg), level: cfg.level}
	composed = newFanout(cfg.localHandler, ship)
	return composed, ship, cfg
}

// NewShippingHandler builds an [slog.Handler] that writes locally and ships
// every record over HTTP, together with an [io.Closer] that drains and stops
// delivery. Use it to add shipping to an existing slog setup — for example
// slog.SetDefault(slog.New(h)) — without adopting the gas.Logger wrapper.
//
// The same options as [NewShippingLogger] apply. Call Close (typically on
// shutdown) to flush buffered records and stop the delivery goroutine.
func NewShippingHandler(endpoint string, marshaler Marshaler, opts ...ShippingOption) (slog.Handler, io.Closer) {
	composed, ship, _ := buildShipping(endpoint, marshaler, opts)
	return composed, closerFunc(ship.sender.close)
}

// NewShippingLogger returns a constructor for a logger that ships records to
// endpoint using marshaler. By default it also logs JSON to stderr; use
// [WithoutLocalHandler] or [WithLocalHandler] to change that.
func NewShippingLogger(endpoint string, marshaler Marshaler, opts ...ShippingOption) ShippingLoggerCtor {
	return func() *ShippingLogger {
		composed, ship, cfg := buildShipping(endpoint, marshaler, opts)
		return &ShippingLogger{
			SlogLogger: &SlogLogger{logger: slog.New(composed), eventInitialCapacity: cfg.eventCap},
			handler:    ship,
			name:       cfg.name,
		}
	}
}

// closerFunc adapts a function to [io.Closer].
type closerFunc func() error

// Close invokes the wrapped function.
func (f closerFunc) Close() error { return f() }

// Flush posts any buffered records and blocks until they have been sent,
// overriding the no-op inherited from [SlogLogger].
func (l *ShippingLogger) Flush() { l.handler.sender.flush() }

// Name returns the service name (implements [gas.Service]).
func (l *ShippingLogger) Name() string { return l.name }

// Init is a no-op; the delivery goroutine starts at construction so the logger
// works with or without the DI container (implements [gas.Service]).
func (l *ShippingLogger) Init() error { return nil }

// Close drains buffered records and stops the delivery goroutine. It is
// idempotent (implements [gas.Service]).
func (l *ShippingLogger) Close() error { return l.handler.sender.close() }
