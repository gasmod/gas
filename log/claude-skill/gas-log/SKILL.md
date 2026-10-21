---
name: gas-log
description: >
  Reference documentation for the gas/log Go package
  (github.com/gasmod/gas/log) — pluggable structured logging backends and
  HTTP log shipping for the Gas ecosystem. Use this skill when writing,
  reviewing, or debugging Go code that involves structured logging in Gas
  services. Covers the two local backends (ZeroLogLogger, SlogLogger), the
  fluent gas.Logger / gas.LogEvent / gas.LoggerContext interfaces,
  constructor options, sub-loggers via With(), per-request loggers on the
  request context via gas.WithLogger / gas.LoggerFromContext, DI registration
  via a singleton gas.Logger plus a scoped per-request logger type, level
  mapping across backends,
  and the log-shipping subsystem: NewShippingLogger / NewShippingHandler, the
  batching HTTP sender, per-sink level thresholds, the Marshaler interface and
  Record type, the OTLP/HTTP JSON marshaler (NewOTLPMarshaler), the fanout and
  shipping Handler for custom routing/alerting, and best-effort drop delivery.
  Make sure to use this skill whenever working with logging or log shipping in
  a Gas application, even if the user doesn't explicitly mention "gas/log" —
  any code that imports gasmod/gas/log or references gas.Logger, or that ships
  logs to an HTTP/OTLP endpoint, should trigger this skill.
---

# Gas Log Package Reference

Pluggable structured logging backends for the Gas ecosystem. Implements
`gas.Logger`, `gas.LogEvent`, and `gas.LoggerContext` with two
interchangeable local backends (Zerolog,
Slog), plus a **shipping logger** built on the Slog backend that also delivers
every record to an HTTP endpoint. See [Shipping logs over HTTP](#shipping-logs-over-http).

```
import gaslog "github.com/gasmod/gas/log"
```

> **Note:** A no-op logger (`gas.NewNopLogger()`) lives in gas core, not in
> this package. Use it for tests or when logging is disabled.

## Backends

| Backend     | Constructor                                     | Backing library     | Notes                                                                 |
|-------------|-------------------------------------------------|---------------------|-----------------------------------------------------------------------|
| **Zerolog** | `NewZeroLogLogger(opts ...ZeroLogLoggerOption)` | rs/zerolog          | High-performance structured JSON. Full level support including Trace. |
| **Slog**    | `NewSlogLogger(opts ...SlogLoggerOption)`       | `log/slog` (stdlib) | Zero external deps. Trace maps to Debug (slog has no Trace).          |

Each constructor returns a constructor function type (`ZeroLogLoggerCtor` /
`SlogLoggerCtor`) that the Gas DI container accepts directly. When no options
are provided, backends use sensible defaults: Zerolog uses the global
`zerolog/log.Logger`; Slog uses `slog.Default()` with `eventInitialCapacity`
of 5.

### Choosing a Backend

- **Zerolog** — prefer when you need Trace-level logging, high throughput, or
  already use zerolog elsewhere in your stack. Adds one external dependency.
- **Slog** — prefer when you want zero external dependencies (stdlib only) or
  your deployment already uses slog-based tooling. Trace and Debug both map to
  `slog.LevelDebug`.

## Constructor Types and Options

### ZeroLogLogger

```go
// Constructor type — passed to gas.WithService[gas.Logger](...)
type ZeroLogLoggerCtor func() (*ZeroLogLogger, error)

// Constructor — returns a ZeroLogLoggerCtor
func NewZeroLogLogger(opts ...ZeroLogLoggerOption) ZeroLogLoggerCtor

// Options
type ZeroLogLoggerOption func(*ZeroLogLogger)

func WithZeroLogInstance(logger *zerolog.Logger) ZeroLogLoggerOption
```

### SlogLogger

```go
// Constructor type — passed to gas.WithService[gas.Logger](...)
type SlogLoggerCtor func() (*SlogLogger, error)

// Constructor — returns a SlogLoggerCtor
func NewSlogLogger(opts ...SlogLoggerOption) SlogLoggerCtor

// Options
type SlogLoggerOption func(*SlogLogger)

func WithSlogInstance(logger *slog.Logger) SlogLoggerOption
func WithEventInitialCapacity(capacity int) SlogLoggerOption
```

`WithEventInitialCapacity` controls the pre-allocated attribute slice capacity
per event. Reduces allocations when you know the typical field count. The
default is 5; the value is used as given, so 0 pre-allocates nothing and a
negative value panics when an event is created. Each `LogEvent` collects fields
and emits them as a single `slog.LogAttrs` call on `Send()`.

## Fluent API

All backends share the same interface. Call a level method to get a
`gas.LogEvent`, chain fields, finalize with `Send()`:

```go
logger.Info("request handled").
    Str("method", r.Method).
    Int("status", code).
    Duration("latency", elapsed).
    Send()
```

### Level Methods

| Method  | Returns        |
|---------|----------------|
| `Trace` | `gas.LogEvent` |
| `Debug` | `gas.LogEvent` |
| `Info`  | `gas.LogEvent` |
| `Warn`  | `gas.LogEvent` |
| `Error` | `gas.LogEvent` |

### Field Methods

Available on `gas.LogEvent` and `gas.LoggerContext`:

| Method     | Signature                         | Description                |
|------------|-----------------------------------|----------------------------|
| `Str`      | `(key, val string)`               | String field               |
| `Int`      | `(key string, val int)`           | Integer field              |
| `Int64`    | `(key string, val int64)`         | 64-bit integer field       |
| `Float64`  | `(key string, val float64)`       | Float field                |
| `Bool`     | `(key string, val bool)`          | Boolean field              |
| `Err`      | `(key string, val error)`         | Error field                |
| `Duration` | `(key string, val time.Duration)` | Duration field             |
| `Any`      | `(key string, val any)`           | Arbitrary type field       |

### Other Methods

| Method  | On             | Description                                      |
|---------|----------------|--------------------------------------------------|
| `Flush` | Logger         | No-op for the local backends; on a `ShippingLogger` and its `With()` sub-loggers, posts buffered records and waits |
| `Send`  | LogEvent       | Emit the log event                                |

## Sub-loggers (With)

Create a sub-logger with persistent fields baked in:

```go
reqLogger := logger.With().
    Str("request_id", reqID).
    Str("service", "auth").
    Logger()

reqLogger.Debug("validating token").Send()
// all events from reqLogger include request_id and service
```

`With()` returns a `gas.LoggerContext`. Chain field methods, then call
`Logger()` to produce a new `gas.Logger`. The original logger is unchanged.

## Request Fields

Loggers are never changed in place. For fields every log line of a request
should carry, middleware derives a logger with `With()` and stores it on the
request context; handlers and services read it back with
`gas.LoggerFromContext`:

```go
func userFields(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        base := gas.LoggerFromContext(r.Context()) // set by gas.RequestLogger
        if base == nil {
            base = gas.MustResolveFromRequestScope[gas.Logger](r)
        }
        logger := base.With().Str("user_id", userID(r)).Logger()
        next.ServeHTTP(w, r.WithContext(gas.WithLogger(r.Context(), logger)))
    })
}
```

`gas.RequestLogger` does this for `request_id`, so mounting it upstream gives
every handler a request logger without extra code. With a scoped
request-logger type (see DI Registration), it also puts that logger in the
request scope, so handlers that take the type as a parameter get it.

## Context-Scoped Logging

Propagate loggers through `context.Context` using helpers defined in gas core:

```go
// Store logger in context
ctx := gas.WithLogger(r.Context(), reqLogger)

// Retrieve from context (returns nil if absent)
logger := gas.LoggerFromContext(ctx)
logger.Debug("processing").Send()
```

Typical pattern in a request handler:

```go
func (s *Service) handleRequest(w http.ResponseWriter, r *http.Request) {
    reqLogger := s.logger.With().
        Str("request_id", r.Header.Get("X-Request-ID")).
        Logger()

    ctx := gas.WithLogger(r.Context(), reqLogger)
    s.process(ctx)
}

func (s *Service) process(ctx context.Context) {
    logger := gas.LoggerFromContext(ctx)
    logger.Debug("processing").Send()
}
```

## Shipping logs over HTTP

`NewShippingLogger` returns a `gas.Logger` that writes locally **and** ships
every record to an HTTP endpoint. It is built on the Slog backend: records are
captured by an `slog.Handler`, batched, and delivered by a background
goroutine. The wire shape is a pluggable `Marshaler`, so the transport is
reusable across schemas; an OTLP/HTTP JSON marshaler ships in the box.

```go
logger, err := gaslog.NewShippingLogger(
    "https://logs.example.com/v1/logs",
    gaslog.NewOTLPMarshaler(
        gaslog.WithServiceName("my-service"),
        gaslog.WithServiceVersion("1.4.2"),
    ),
    gaslog.WithHeader("X-API-Key", os.Getenv("LOG_KEY")),
    gaslog.WithBatchSize(100),
    gaslog.WithFlushInterval(2*time.Second),
)()
if err != nil {
    return err
}

logger.Info("request handled").Str("method", "GET").Int("status", 200).Send()
```

The returned `*ShippingLogger` embeds `*SlogLogger`, so the entire fluent API
(level methods, field methods, `With()`) works unchanged.

### Constructors

```go
// gas.Logger wrapper; the ctor satisfies the DI container's constructor shape.
type ShippingLoggerCtor func() (*ShippingLogger, error)
func NewShippingLogger(endpoint string, marshaler Marshaler, opts ...ShippingOption) ShippingLoggerCtor

// Standalone slog.Handler + io.Closer, for adding shipping to an existing slog
// setup (e.g. slog.SetDefault) without adopting the gas.Logger wrapper. Same
// options. Call Close on shutdown to flush buffered records and stop delivery.
func NewShippingHandler(endpoint string, marshaler Marshaler, opts ...ShippingOption) (slog.Handler, io.Closer)
```

`ShippingLogger` implements `gas.Service`: registered in the DI container,
`Close(ctx)` is called at shutdown and drains buffered records (the ctx is
not consulted; the drain is bounded by the HTTP client timeout). Outside the
container, call `Flush()` before exit (posts and blocks until sent; a sub-logger
from `With()` flushes the same sender) or
`Close(ctx)` (drains and stops the delivery goroutine; idempotent). `Init(ctx)` is a
no-op — the delivery goroutine starts at construction, so the logger works with
or without the container.

### Options

| Option                          | Effect                                                                                   | Default            |
|---------------------------------|------------------------------------------------------------------------------------------|--------------------|
| `WithLevel(slog.Leveler)`       | Minimum level shipped **remotely** (also seeds the default local handler — see below).    | `slog.LevelInfo`   |
| `WithLocalHandler(slog.Handler)`| Handler that also receives every record locally (stdout/file alongside shipping).         | JSON to `os.Stderr`|
| `WithoutLocalHandler()`         | Disable local logging; ship only.                                                         | local enabled      |
| `WithHeader(key, value)`        | Request header sent on every batch (repeatable; e.g. an API key).                         | none               |
| `WithBatchSize(n)`              | Records accumulated before a batch is sent.                                               | 100                |
| `WithQueueSize(n)`              | Buffered-record capacity; records drop when full so logging never blocks.                 | 1024               |
| `WithFlushInterval(d)`          | Max time a record waits before delivery.                                                  | 2s                 |
| `WithHTTPClient(*http.Client)`  | Override the delivery client.                                                             | 10s-timeout client |
| `WithErrorHandler(func(error))` | Callback for **delivery** failures (marshal, transport, non-2xx). Never reaches log call. | none (silent)      |
| `WithName(string)`              | `gas.Service` name reported by the logger.                                                | `gas/log-shipping` |

### Delivery semantics

Best-effort by design: `enqueue` drops records (never blocks the caller) when
the queue is full or the sender is shutting down. The background loop posts a
batch on size, on the flush interval, on an explicit `Flush()`, and once more
on shutdown. Delivery failures (marshal error, transport error, non-2xx status)
go to `WithErrorHandler` and never propagate to the logging call site. There is
no built-in retry — a failed batch is reported and dropped. The local handler
sees each record first: if it returns an error, the record is not shipped and
`Send` drops the error. After `Close`, the local handler still writes, records
meant for shipping are dropped, and `Flush` returns at once.

### Per-sink level thresholds (important nuance)

`WithLevel` sets the **ship** threshold, and when no local handler is supplied
the default stderr handler is created with **that same level**. So
`WithLevel(slog.LevelError)` alone makes *both* console and server Error-only.
To keep the console verbose while shipping only Error and up, set the local
handler's level explicitly:

```go
logger, err := gaslog.NewShippingLogger(
    endpoint,
    gaslog.NewOTLPMarshaler(gaslog.WithServiceName("my-service")),
    gaslog.WithLevel(slog.LevelError), // ship Error+ only
    gaslog.WithLocalHandler(           // but keep console at Debug
        slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}),
    ),
)()
if err != nil {
    return err
}
```

### Field-based routing and alerting (not built in — compose it)

**Level is the only routing axis the shipping logger provides.** The shipping
`Handler.Enabled` gates purely on level; the `Marshaler` only reshapes the wire
format. There is no built-in "ship this / drop that" by field and no alerting.
To route by a semantic field (e.g. drop router 404s, page an admin on critical
records) compose your own `slog.Handler` around a ship-only handler and drive it
from attributes you set at the call site:

```go
// Ship-only handler (fanout collapses to just the sender when local is off).
shipHandler, shipCloser := gaslog.NewShippingHandler(
    endpoint,
    gaslog.NewOTLPMarshaler(gaslog.WithServiceName("my-service")),
    gaslog.WithoutLocalHandler(),
    gaslog.WithLevel(slog.LevelError),
)
defer shipCloser.Close() // drain on shutdown

console := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug})
root := routingHandler{console: console, ship: shipHandler, alerts: myPager}
logger, err := gaslog.NewSlogLogger(gaslog.WithSlogInstance(slog.New(root)))()
if err != nil {
    return err
}

// routingHandler.Handle inspects r.Attrs: e.g. kind=="http_404" -> skip ship,
// alert==true -> also notify the pager, otherwise ship for later review.
// (Implement Enabled as the OR of console/ship; fan WithAttrs/WithGroup to both.)
```

Call sites then express intent as fields:

```go
logger.Error("write lost after commit").Err("error", err).Bool("alert", true).Send() // ship + page
logger.Error("payment provider timeout").Err("error", err).Send()                    // ship, review later
logger.Warn("route not found").Str("kind", "http_404").Str("path", p).Send()         // console only
```

> `WithErrorHandler` is **only** for shipping delivery failures. It is not a
> content-alert hook — alerting on log content is entirely the routing handler's job.

### Marshaler and Record

To ship a different wire shape, implement `Marshaler`. `Record` is
backend-neutral (level, timestamp, message, fully-qualified attributes):

```go
type Record struct {
    Time    time.Time
    Message string
    Attrs   []slog.Attr
    Level   slog.Level
}

type Marshaler interface {
    Marshal(records []Record) ([]byte, error) // encode a batch into one request body
    ContentType() string                      // value for the Content-Type header
}
```

A wrapping `Marshaler` that filters `[]Record` before delegating is the
lightest way to drop a subset from the server (no alerting).

### OTLP marshaler

`NewOTLPMarshaler` emits OpenTelemetry OTLP/HTTP JSON logs
(`resourceLogs -> scopeLogs -> logRecords`), `Content-Type: application/json`.

```go
func NewOTLPMarshaler(opts ...OTLPOption) *OTLPMarshaler
```

| Option                          | Effect                                                                     |
|---------------------------------|----------------------------------------------------------------------------|
| `WithServiceName(name)`         | Sets `service.name` resource attr; also seeds the scope name if unset.     |
| `WithServiceVersion(version)`   | Sets `service.version` resource attr.                                      |
| `WithResourceAttribute(k, v)`   | Adds an arbitrary resource attr (`host.name`, `deployment.environment`, …).|
| `WithScopeName(name)`           | Sets the instrumentation scope name per batch.                             |

slog levels map to OTLP severity band floors: Debug→5, Info→9, Warn→13,
Error→17. `SeverityText` is the slog level string.

> **Trace correlation caveat:** the emitted `logRecord` carries only
> `timeUnixNano`, `severityText`, `body`, `attributes`, `severityNumber`. It has
> **no `traceId`/`spanId` fields**. A `trace_id` you add via a field lands under
> `attributes`, not in OTLP's native trace-correlation fields — good enough to
> *search* logs by trace id, but backends that correlate logs to traces via the
> native field won't pick it up. Write a custom `Marshaler` that lifts
> `trace_id`/`span_id` attrs into the top-level fields if you need that.

## Level Mapping

| gas.Logger method | Zerolog level        | Slog level        |
|-------------------|----------------------|-------------------|
| `Trace`           | `zerolog.TraceLevel` | `slog.LevelDebug` |
| `Debug`           | `zerolog.DebugLevel` | `slog.LevelDebug` |
| `Info`            | `zerolog.InfoLevel`  | `slog.LevelInfo`  |
| `Warn`            | `zerolog.WarnLevel`  | `slog.LevelWarn`  |
| `Error`           | `zerolog.ErrorLevel` | `slog.LevelError` |

## DI Registration

Register `gas.Logger` as a **singleton**. Most services take a `gas.Logger` in
their constructor, and the container rejects a singleton that depends on a
scoped service (`captive dependency: singleton X depends on scoped
gas.Logger`):

```go
zl := zerolog.New(os.Stdout).With().Timestamp().Logger()

app := gas.NewApp(
    gas.WithSingletonService[gas.Logger](
        gaslog.NewZeroLogLogger(gaslog.WithZeroLogInstance(&zl)),
    ),
    // ...other services
)
```

With slog (zero external deps):

```go
sl := slog.New(slog.NewJSONHandler(os.Stdout, nil))

app := gas.NewApp(
    gas.WithSingletonService[gas.Logger](
        gaslog.NewSlogLogger(
            gaslog.WithSlogInstance(sl),
            gaslog.WithEventInitialCapacity(5),
        ),
    ),
)
```

Using defaults (Zerolog uses `log.Logger`, Slog uses `slog.Default()`):

```go
app := gas.NewApp(
    gas.WithSingletonService[gas.Logger](gaslog.NewZeroLogLogger()),
)
```

For per-request fields, register a separate scoped type derived from the
singleton and give it to the request middleware. It derives a logger carrying
`request_id` from the request's instance and puts it back in the request scope
and on the request context, leaving the singleton unchanged:

```go
type RequestLogger interface{ gas.Logger }

app := gas.NewApp(
    gas.WithSingletonService[gas.Logger](gaslog.NewSlogLogger()),
    gas.WithScopedService[RequestLogger](func(l gas.Logger) RequestLogger {
        return l.With().Logger()
    }),
)
app.Router().UseMiddlewareFunc(gas.RequestLogger[RequestLogger]())
// A handler taking RequestLogger gets the request_id:
//   func list(ctx gas.Context, logger RequestLogger) error
```

`gas.RequestLogger[gas.Logger]()` with the singleton is also safe: the
singleton is left unchanged and the request logger is only on the context.

## Complete Example

A full service with DI-wired logging, sub-loggers, and context propagation:

```go
package myservice

import (
    "context"
    "net/http"

    "github.com/gasmod/gas"
)

type Service struct {
    logger gas.Logger
    router *gas.Router
}

func New(logger gas.Logger, router *gas.Router) *Service {
    return &Service{logger: logger, router: router}
}

func (s *Service) Name() string  { return "myservice" }
func (s *Service) Close(ctx context.Context) error { return nil }

func (s *Service) Init(ctx context.Context) error {
    s.logger.Info("service initialized").Str("name", s.Name()).Send()
    s.router.Handle(s, "GET", "/users/{id}", s.handleGetUser)
    return nil
}

func (s *Service) handleGetUser(ctx gas.Context, db gas.DatabaseProvider) error {
    // Create a sub-logger scoped to this request
    reqLogger := s.logger.With().
        Str("request_id", ctx.Header("X-Request-ID")).
        Str("user_id", ctx.Param("id")).
        Logger()

    // Propagate via context for downstream calls
    reqCtx := gas.WithLogger(ctx, reqLogger)

    user, err := s.fetchUser(reqCtx, db, ctx.Param("id"))
    if err != nil {
        reqLogger.Error("failed to fetch user").Err("error", err).Send()
        return err
    }

    reqLogger.Info("user fetched").Send()
    return ctx.JSON(http.StatusOK, user)
}

func (s *Service) fetchUser(ctx context.Context, db gas.DatabaseProvider, id string) (*User, error) {
    logger := gas.LoggerFromContext(ctx)
    logger.Debug("querying database").Str("user_id", id).Send()
    // ... database query
    return nil, nil
}

type User struct {
    ID   string `json:"id"`
    Name string `json:"name"`
}
```

App wiring:

```go
package main

import (
    "os"

    "github.com/gasmod/gas"
    gaslog "github.com/gasmod/gas/log"
    "github.com/rs/zerolog"

    "myapp/myservice"
)

func main() {
    zl := zerolog.New(os.Stdout).With().Timestamp().Logger()

    app := gas.NewApp(
        gas.WithSingletonService[gas.Logger](
            gaslog.NewZeroLogLogger(gaslog.WithZeroLogInstance(&zl)),
        ),
        gas.WithSingletonService[*myservice.Service](myservice.New),
    )
    app.Run()
}
```
