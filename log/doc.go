// Package log provides logging backends for the Gas ecosystem: adapters that
// implement core's gas.Logger over zerolog and log/slog, and a shipping
// logger that also delivers records in batches to an HTTP endpoint.
//
// # Role in the framework
//
// The logger interfaces live in core: gas.Logger, gas.LogEvent,
// gas.LoggerContext and gas.MutableLoggerContext. Services depend on
// gas.Logger, not on this package. The package implements them three ways:
//
//   - [ZeroLogLogger] adapts a [zerolog.Logger]; by default it uses
//     zerolog's global logger.
//   - [SlogLogger] adapts a [log/slog] logger; by default it uses
//     slog.Default(). slog has no Trace level, so Trace logs at Debug.
//   - [ShippingLogger] builds on SlogLogger. Every record goes to a local
//     handler (JSON on stderr by default) and to a batching HTTP sender that
//     posts batches to an endpoint, encoded by a [Marshaler];
//     [OTLPMarshaler] encodes OTLP/HTTP JSON logs. ShippingLogger also
//     implements gas.Service.
//
// [NewShippingHandler] gives the same local-plus-shipping pipeline as a
// plain [slog.Handler] with an [io.Closer], for code that uses slog
// directly. [Handler] is the shipping half on its own, exported so it can be
// composed into custom handler chains.
//
// All backends share the fluent API: a level method (Info, Debug, ...)
// returns a gas.LogEvent, field methods (Str, Int, Err, ...) add to it, and
// Send emits it.
//
//	logger.Info("request handled").
//		Str("method", r.Method).
//		Int("status", code).
//		Duration("latency", elapsed).
//		Send()
//
// # Wiring
//
// NewZeroLogLogger, NewSlogLogger and NewShippingLogger take options and
// return a constructor with no parameters, so the DI container needs nothing
// else to build the logger. The app registers it as the gas.Logger
// singleton; the package registers nothing itself:
//
//	app := gas.NewApp(
//		gas.WithSingletonService[gas.Logger](gaslog.NewSlogLogger()),
//	)
//
// Register it as a singleton. Most services take a gas.Logger in their
// constructor, and the container rejects a singleton that depends on a
// scoped or transient service.
//
// # Configuration
//
// The package reads no configuration from gas.ConfigProvider; everything is
// set with options. A ShippingLogger defaults to batches of 100 records, a
// queue of 1024 records, a flush every 2 seconds, an HTTP client with a
// 10-second timeout, a minimum level of Info, the service name
// "gas/log-shipping", and a local JSON handler on stderr at the same level.
// WithBatchSize, WithQueueSize and WithFlushInterval ignore values that are
// not positive; WithHTTPClient and WithLevel ignore nil; WithName ignores an
// empty name. WithLocalHandler replaces the local handler and
// WithoutLocalHandler removes it.
//
// # Lifecycle
//
// ZeroLogLogger and SlogLogger have no lifecycle. A ShippingLogger starts its
// delivery goroutine when it is constructed, so it works with or without the
// container; its Init does nothing. Close, which the container runs at
// shutdown, posts the records still queued, stops the goroutine and returns
// once that last post has finished, so it can take as long as the HTTP
// client's timeout. Close can be called more than once. After Close, the
// local handler still writes, records meant for shipping are dropped, and
// Flush returns at once. For NewShippingHandler, the caller closes the
// returned io.Closer the same way.
//
// # Delivery
//
// Shipping never blocks the logging call. A record is queued and dropped
// silently if the queue is full. The sender posts a batch when it reaches
// the batch size, when the flush interval passes, on Flush, and on Close.
// Flush blocks until the current batch has been posted. Marshal errors,
// transport errors and responses with status 300 or above go to the
// WithErrorHandler callback and never to the code that logged; without a
// callback they are discarded. The local handler sees a record first: if it
// fails, the record is not shipped and Send drops the error.
//
// # Invariants
//
// Logging through a logger is safe for concurrent use. SetBaseFields is not:
// Apply replaces the logger's fields in place without synchronization, so
// calling it on a logger that other goroutines are using is a data race, and
// the fields it adds stay on that logger for good (KI-026 in
// KNOWN_ISSUES.md). A sub-logger from With on a ShippingLogger is a
// SlogLogger over the same handlers that keeps the shipping sender's flush,
// so its records are shipped and its Flush posts and waits for them, like
// the ShippingLogger's own.
//
//nolint:revive // intentional package name
package log
