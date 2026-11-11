# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.9.0] - 2026-11-11

### Changed

- The internal modules are required at v0.9.0.

## [0.8.0] - 2026-11-11

### Changed

- **Go 1.27.0 or later is required**, down from 1.27.3, as for the core:
  build apps with Go 1.27.4 or later, for the `net/http` fix of
  [GO-2026-6859](https://pkg.go.dev/vuln/GO-2026-6859).
- Dependencies updated, and the internal modules required at v0.8.0.

## [0.7.0] - 2026-11-11

### Changed

- The internal modules are required at v0.7.0.

## [0.6.3] - 2026-11-10

### Changed

- The internal modules are required at v0.6.3.

## [0.6.2] - 2026-11-10

### Changed

- The internal modules are required at v0.6.2.

## [0.6.1] - 2026-11-10

### Changed

- Dependencies updated, and the internal modules required at v0.6.1.

## [0.6.0] - 2026-11-08

v0.6.0 rewrites the module around the contract in its package
documentation (`doc.go`), which lists every error verbatim. Read it before
upgrading.

### Added

- **`log/zerolog`**, a package holding the zerolog backend: `New`,
  `WithLogger` and `Logger`. The root package now imports only the
  standard library and `github.com/gasmod/gas`.
- **`log/logtest`**, with `RecordingLogger`, a `gas.Logger` that keeps
  every event it sends, with its level, message and fields, for tests.
- **`ShippingHandler`**, the `slog.Handler` that `NewShippingHandler`
  returns, with `Flush(ctx)`, `Close(ctx)` and `Dropped()`.
  `ShippingLogger.Dropped` returns the same count: records not delivered
  since the handler was made.
- **`LevelTrace`**, slog.LevelDebug - 4, the level of `Trace`.
- **Sentinel errors**, all starting with `gas/log: `: `ErrNilLogger`,
  `ErrNilMarshaler`, `ErrInvalidEndpoint`, `ErrInvalidOption`,
  `ErrQueueFull`, `ErrNotRunning`, `ErrLocalHandler`, `ErrMarshal`,
  `ErrMarshalPanic`, `ErrPost`, `ErrStatus`, `ErrNotDelivered` and
  `ErrStillClosing`.
- **Exported defaults:** `DefaultShippingName`, `DefaultBatchSize`,
  `DefaultQueueSize`, `DefaultFlushInterval`, `DefaultHTTPTimeout`,
  `DefaultFlushTimeout` and `DefaultShippingLevel`; and
  `OTLPContentType`, `OTLPReplacedBody` and `OTLPMarshaler.Replaced`.

### Changed

- **BREAKING: zerolog moves to `log/zerolog`.** `NewZeroLogLogger`,
  `WithZeroLogInstance` and `ZeroLogLogger` are `zerolog.New`,
  `zerolog.WithLogger` and `zerolog.Logger` there.
- **BREAKING: renamed slog options.** `WithSlogInstance` is
  `WithSlogLogger`, and `SlogLoggerOption` is `SlogOption`.
  `WithSlogLogger(nil)` makes the constructor fail with `ErrNilLogger`.
- **BREAKING: constructors return a plain function type.**
  `NewSlogLogger` and `NewShippingLogger` return
  `func() (*SlogLogger, error)` and `func() (*ShippingLogger, error)`;
  the named `SlogLoggerCtor` and `ShippingLoggerCtor` types are removed.
  Registering them with `gas.WithSingletonService[gas.Logger]` is
  unchanged.
- **BREAKING: `NewShippingHandler` returns `(*ShippingHandler, error)`**
  instead of a `slog.Handler` and an `io.Closer`.
- **BREAKING: options are checked.** The constructors fail with
  `ErrNilMarshaler` for a nil Marshaler, `ErrInvalidEndpoint` for an
  endpoint that is not an absolute http or https URL, and
  `ErrInvalidOption` for a batch size, queue size or flush interval that
  is not positive, a nil HTTP client, level, local handler or error
  handler, an empty name, or a `WithHeader` name that is not a token or a
  value with a control character. A size or interval that was not
  positive, a nil client or level, and an empty name used to be ignored.
- **BREAKING: Trace has its own level.** `Trace` logs at `LevelTrace`,
  below slog.LevelDebug, so a handler can filter it apart from Debug; it
  used to log at Debug. OTLP sends it as `TRACE` (severity 1).
- **BREAKING: the shipping lifecycle.** A `ShippingLogger`'s `Init`
  starts the sender, which used to start at construction; a record logged
  before the first `Init` is written locally and dropped for shipping,
  and reported with `ErrNotRunning`. `Close(ctx)` posts what is queued
  within its context and returns an error wrapping `ErrNotDelivered` with
  the count of records not delivered; it used to ignore its context.
  `Init` after `Close` starts the sender again, waiting for the earlier
  sender to stop, or failing with `ErrStillClosing` when its context ends
  first. `NewShippingHandler` starts its sender at once.
- **`Flush` is bounded and reports.** `ShippingLogger.Flush` posts the
  queued records within `DefaultFlushTimeout` and reports a failure to the
  error handler; `ShippingHandler.Flush(ctx)` returns it. When the context
  ends first, the records not posted stay queued.
- **Failures are reported by default.** Without `WithErrorHandler`,
  errors are written to standard error; they used to be discarded. The
  error handler runs on a goroutine of its own, so it may log through the
  same logger, and reports of one class are coalesced to at most one per
  flush interval, such as `gas/log: queue full: record dropped (3 times)`.
  A full queue, which used to drop records silently, is reported with
  `ErrQueueFull`.
- **A failing local handler no longer stops shipping.** The record is
  still queued for shipping, and `Handle` returns the local handler's
  error wrapped with `ErrLocalHandler`.
- **Records are captured in `Handle`.** Attributes are flattened, with a
  group's key joined to theirs by `.`, LogValuers are resolved, and a
  value of kind `slog.KindAny` is encoded on the logging goroutine: an
  error to its text, a `fmt.Stringer` to its String text, a value
  `encoding/json` encodes to its JSON text, and anything else to
  `!unencodable` and its type name, so nothing the caller holds is read
  later and a cycle cannot overflow the stack. A Marshaler must not keep
  the batch it is given.
- **OTLP encoding.** A zero time, one before 1970 or one after 2262
  leaves `timeUnixNano` out; a uint64 above the int64 range
  is a decimal `stringValue`; a non-finite float is `NaN`, `Infinity` or
  `-Infinity`. A record that fails to encode is sent with the body
  `OTLPReplacedBody` and counted by `Replaced`, so it cannot fail its
  batch.
- **Events are sent at most once.** Every event method called after
  `Send`, `Send` included, does nothing, on every backend.

### Removed

- **BREAKING:** `Handler`, which could not be built outside the package
  (KI-042); use `NewShippingHandler`. The exported event and context
  types `SlogLogEvent`, `SlogLoggerContext`, `ZeroLogLogEvent` and
  `ZeroLogLoggerContext`; use `gas.LogEvent` and `gas.LoggerContext`.
- **BREAKING:** `WithEventInitialCapacity`, whose negative value panicked
  on the first event (KI-041).

### Security

- **Endpoints in errors show the scheme and host only.** No error names
  the endpoint's user info, path, query or fragment, where some services
  put the token, or a header name or value. A parse error of the endpoint
  is replaced, not wrapped, and a Marshaler's panic value is left out of
  its report.
- **The default HTTP client follows no redirect.** A 3xx response fails
  the post with its status, so the headers and the records never reach
  another URL. A client given to `WithHTTPClient` keeps its own policy.
- **Requires Go 1.27.3.** The `go` directive moves from 1.27 to 1.27.3,
  the first 1.27 release with the fix for GO-2026-6809 (CVE-2026-111943):
  `crypto/x509` now rejects intermediate certificates known to be
  compromised. A build with an older toolchain switches to 1.27.3 under
  `GOTOOLCHAIN=auto` and fails under `GOTOOLCHAIN=local`.

## [0.5.6] - 2026-10-19

The tags `log/v0.4.1` to `log/v0.5.6` shipped their changes under
`[Unreleased]`. This section holds them. Before `log/v0.4.1` the module
was developed in the separate `github.com/gasmod/gas-log`
repository, and this file records none of its releases.

### Added

- `NewShippingLogger`: a `gas.Logger` that writes locally and ships every record to
  an HTTP endpoint via a batching background sender. Built on the Slog backend and
  implements `gas.Service` for drain-on-shutdown.
- `Marshaler` interface and `Record` type for pluggable wire shapes, with
  `NewOTLPMarshaler` shipping OpenTelemetry OTLP/HTTP JSON logs out of the box.
- `Handler`, an `slog.Handler` that batches and ships records, composable into
  custom handler chains.
- `NewShippingHandler`: returns the shipping sink as a standalone `slog.Handler`
  plus an `io.Closer`, for adding shipping to an existing slog setup (e.g.
  `slog.SetDefault`) without adopting the `gas.Logger` wrapper.

### Changed

- **BREAKING: import path.** The module moved from
  `github.com/gasmod/gas-log` to `github.com/gasmod/gas/log`, in
  log/v0.4.1.
- **BREAKING: Go 1.27 is required.** The `go` directive moves from 1.26.1
  to 1.27, in log/v0.5.0, as core now uses generic methods.
- **BREAKING: the logger constructors return an error.** `ZeroLogLoggerCtor`,
  `SlogLoggerCtor` and `ShippingLoggerCtor`, which `NewZeroLogLogger`,
  `NewSlogLogger` and `NewShippingLogger` return, are now
  `func() (*T, error)`, the `CONVENTIONS.md` shape. They cannot fail yet, so
  they return nil; the change is the shape. Registering them with
  `gas.WithSingletonService` is unchanged, since the container accepts a
  trailing error. Code that calls the constructor directly, as in
  `gaslog.NewSlogLogger()()`, must take the second result. `NewShippingHandler`
  and `NewOTLPMarshaler`, which apps build directly, are unchanged (gas#106).
- **BREAKING:** `ShippingLogger` implements the context-aware `gas.Service`:
  `Init(ctx)` and `Close(ctx)`. `Init` stays a no-op (the delivery goroutine
  starts at construction and never derives from the Init ctx). `Close`
  ignores ctx; the drain is bounded by the HTTP client timeout.
  `ShippingLogger` shipped in log/v0.4.1 with `Init()` and `Close()`.
- A sub-logger made with `With()` on a `ShippingLogger` now flushes the
  shipping sender: its `Flush` posts buffered records and waits, as the
  `ShippingLogger`'s own does. It used to inherit `SlogLogger`'s no-op
  `Flush`, so a service holding such a sub-logger could not wait for its
  records to be delivered.

### Removed

- **BREAKING: `SetBaseFields`, `SlogMutableLoggerContext`, and
  `ZeroLogMutableLoggerContext`**, following the removal of
  `gas.MutableLoggerContext` from core. Derive a logger with `With()` and
  carry it on the request context with `gas.WithLogger` instead.

[Unreleased]: https://github.com/gasmod/gas/compare/log/v0.8.0...HEAD
[0.8.0]: https://github.com/gasmod/gas/releases/tag/log/v0.8.0
[0.7.0]: https://github.com/gasmod/gas/releases/tag/log/v0.7.0
[0.6.3]: https://github.com/gasmod/gas/releases/tag/log/v0.6.3
[0.6.2]: https://github.com/gasmod/gas/releases/tag/log/v0.6.2
[0.6.1]: https://github.com/gasmod/gas/releases/tag/log/v0.6.1
[0.6.0]: https://github.com/gasmod/gas/releases/tag/log/v0.6.0
[0.5.6]: https://github.com/gasmod/gas/releases/tag/log/v0.5.6
