package log

import (
	"context"
	"log/slog"

	"github.com/gasmod/gas"
)

// ShippingLogger is a gas.Logger that writes each record locally and ships
// it to an HTTP endpoint, through a ShippingHandler, as described in the
// package documentation. It is a gas.Service: Init starts its sender and
// Close stops it. It is safe for concurrent use.
type ShippingLogger struct {
	// logger is the SlogLogger over handler.
	logger *SlogLogger
	// handler is the ShippingHandler.
	handler *ShippingHandler
	// name is the gas.Service name.
	name string
}

var (
	_ gas.Logger  = (*ShippingLogger)(nil)
	_ gas.Service = (*ShippingLogger)(nil)
)

// NewShippingLogger returns the constructor of a ShippingLogger that posts
// the records to endpoint, encoded by m, with opts applied, in the shape
// the gas container expects. The constructor checks what
// NewShippingHandler checks, in the same order, and does not start the
// sender: Init does.
func NewShippingLogger(
	endpoint string,
	m Marshaler,
	opts ...ShippingOption,
) func() (*ShippingLogger, error) {
	return func() (*ShippingLogger, error) {
		h, c, err := buildShippingHandler(endpoint, m, opts)
		if err != nil {
			return nil, err
		}

		l := &ShippingLogger{handler: h, name: c.name}

		l.logger = &SlogLogger{logger: slog.New(h), flush: l.Flush}

		return l, nil
	}
}

// Trace starts an event at LevelTrace.
func (l *ShippingLogger) Trace(msg string) gas.LogEvent { return l.logger.Trace(msg) }

// Debug starts an event at slog.LevelDebug.
func (l *ShippingLogger) Debug(msg string) gas.LogEvent { return l.logger.Debug(msg) }

// Info starts an event at slog.LevelInfo.
func (l *ShippingLogger) Info(msg string) gas.LogEvent { return l.logger.Info(msg) }

// Warn starts an event at slog.LevelWarn.
func (l *ShippingLogger) Warn(msg string) gas.LogEvent { return l.logger.Warn(msg) }

// Error starts an event at slog.LevelError.
func (l *ShippingLogger) Error(msg string) gas.LogEvent { return l.logger.Error(msg) }

// With returns a context for a logger that ships through the same sender
// with the fields added to the context. Its Flush is l's.
func (l *ShippingLogger) With() gas.LoggerContext { return l.logger.With() }

// Flush is ShippingHandler.Flush bounded by DefaultFlushTimeout. Its error,
// if any, goes to the error handler, in the class of ErrNotDelivered.
func (l *ShippingLogger) Flush() {
	ctx, cancel := context.WithTimeout(context.Background(), DefaultFlushTimeout)
	defer cancel()

	err := l.handler.Flush(ctx)
	if err != nil {
		l.handler.sender.reporter.report(ErrNotDelivered, err)
	}
}

// Name returns the name given to WithName, or DefaultShippingName.
func (l *ShippingLogger) Name() string { return l.name }

// Init starts the sender unless it runs, so a ShippingLogger closed by
// Close ships again after Init. When the sender of an earlier Close is
// still posting, because Close's context ended first or Close has not
// returned, Init waits for it to stop; it returns an error wrapping
// ErrStillClosing and ctx's error when ctx ends first. Otherwise it
// returns nil.
func (l *ShippingLogger) Init(ctx context.Context) error { return l.handler.sender.start(ctx) }

// Close is ShippingHandler.Close.
func (l *ShippingLogger) Close(ctx context.Context) error { return l.handler.Close(ctx) }

// Dropped is ShippingHandler.Dropped.
func (l *ShippingLogger) Dropped() uint64 { return l.handler.Dropped() }
