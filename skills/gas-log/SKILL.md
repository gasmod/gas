---
name: gas-log
description: >
  Reference documentation for the gas/log Go module
  (github.com/gasmod/gas/log) and its subpackages log/zerolog and
  log/logtest: gas.Logger implementations and HTTP log shipping for the Gas
  ecosystem. Use this skill when writing, reviewing, or debugging Go code
  that involves structured logging in Gas services. Covers the slog backend
  (NewSlogLogger, LevelTrace), the zerolog backend (gaszerolog.New), the
  recording logger for tests (logtest), the no-op gas.NopLogger, the fluent
  gas.Logger / gas.LogEvent / gas.LoggerContext interfaces, sub-loggers via
  With(), per-request loggers on the request context via gas.WithLogger /
  gas.LoggerFromContext and gas.RequestLogger, DI registration via a
  singleton gas.Logger plus a scoped per-request logger type, level mapping
  across backends, and the log-shipping subsystem: NewShippingLogger (a
  gas.Service whose Init starts the sender) / NewShippingHandler, the
  batching HTTP sender, per-sink level thresholds, the Marshaler interface
  and Record type, the OTLP/HTTP JSON marshaler (NewOTLPMarshaler),
  Flush/Close/Dropped, the error handler and its sentinel errors, and
  best-effort drop delivery. Make sure to use this skill whenever working
  with logging or log shipping in a Gas application, even if the user
  doesn't explicitly mention "gas/log": any code that imports
  gasmod/gas/log or references gas.Logger, or that ships logs to an
  HTTP/OTLP endpoint, should trigger this skill.
---

# Gas Log Package Reference

`gas.Logger` implementations for the Gas ecosystem. The module implements
`gas.Logger`, `gas.LogEvent`, and `gas.LoggerContext` with a slog backend,
a zerolog backend in its own subpackage, and a recording logger for tests,
plus a **shipping logger** built on the slog backend that also delivers
every record to an HTTP endpoint. See [Shipping logs over HTTP](#shipping-logs-over-http).

```
import (
    gaslog "github.com/gasmod/gas/log"             // stdlib + gas only
    gaszerolog "github.com/gasmod/gas/log/zerolog" // only if you use zerolog
    "github.com/gasmod/gas/log/logtest"            // tests
)
```

> **Note:** The no-op logger is `gas.NopLogger` in gas core, a struct whose
> zero value is ready to use (`var l gas.Logger = &gas.NopLogger{}`). There is
> no `NewNopLogger` constructor. In tests that assert on log output, use
> `logtest.New()` instead.

## Backends

| Backend       | Constructor                                      | Backing library     | Notes                                                         |
|---------------|--------------------------------------------------|---------------------|---------------------------------------------------------------|
| **Slog**      | `gaslog.NewSlogLogger(opts ...SlogOption)`       | `log/slog` (stdlib) | Zero external deps. Trace is `gaslog.LevelTrace` (`DEBUG-4`). |
| **Zerolog**   | `gaszerolog.New(opts ...gaszerolog.Option)`      | rs/zerolog          | Own package, so only apps that use it depend on zerolog.      |
| **Shipping**  | `gaslog.NewShippingLogger(endpoint, m, opts...)` | `log/slog` (stdlib) | Slog backend over a `ShippingHandler`; a `gas.Service`.       |
| **Recording** | `logtest.New()`                                  | none                | For tests; `Entries()` returns what was sent.                 |

The logger constructors return a constructor in the shape the Gas DI
container expects, `func() (*T, error)`; call it with `()` outside the
container. Options only record their value; the constructor checks them
and fails with an error, never later. With no options, the slog backend
writes to `slog.Default()`.

### Choosing a Backend

- **Slog**: the default choice; stdlib only. Trace is its own level,
  `LevelTrace` (`slog.LevelDebug - 4`), so a handler can filter it apart
  from Debug.
- **Zerolog**: prefer when you already use zerolog elsewhere in your stack.
  Adds the zerolog dependency, through `gas/log/zerolog` only.
- **Shipping**: when records must also reach an HTTP/OTLP collector.

## Constructors and Options

### SlogLogger

```go
func NewSlogLogger(opts ...SlogOption) func() (*SlogLogger, error)

type SlogOption func(*SlogLogger)

func WithSlogLogger(logger *slog.Logger) SlogOption // nil -> ErrNilLogger
```

`SlogLogger.Flush` does nothing, since it buffers nothing. `Send` hands
the record to the handler with the caller's program counter (so
`AddSource` reports the line that called `Send`) and discards the
handler's error, as `slog.Logger` does.

### zerolog.Logger

```go
// package github.com/gasmod/gas/log/zerolog
func New(opts ...Option) func() (*Logger, error)

type Option func(*Logger)

func WithLogger(logger *zerolog.Logger) Option // nil -> ErrNilLogger (== log.ErrNilLogger)
```

```go
zl := zerolog.New(os.Stdout).With().Timestamp().Logger()

logger, err := gaszerolog.New(gaszerolog.WithLogger(&zl))()
if err != nil {
    return err
}
logger.Info("order placed").Str("order", "A-1").Int("items", 3).Send()
```

Its `Flush` does nothing: zerolog writes each event on `Send`. A buffering
writer, such as a diode writer, is flushed by its owner.

### logtest.RecordingLogger

```go
logger := logtest.New() // the zero value is ready too
svc.Run(logger)

for _, e := range logger.Entries() { // a copy, in send order
    if e.Level == "error" { // "trace", "debug", "info", "warn", "error"
        t.Errorf("unexpected error: %s %v", e.Msg, e.Fields)
    }
}
```

A logger from `With()` records into the same entries; its context fields
come before the event's own, so an event's field of the same key wins.

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

An event belongs to the goroutine that started it and is not safe for
concurrent use. It is sent at most once: every method called after `Send`,
`Send` included, does nothing. An event at a disabled level is a shared
no-op event, so its fields cost nothing. A map or slice given to `Any`
must not change before `Send` returns.

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

Encodings differ by backend. `Err` with a nil error: slog writes the key
with a null value, zerolog leaves the field out, shipping sends the empty
value. `Duration`: slog's JSON handler writes nanoseconds, zerolog writes
`zerolog.DurationFieldUnit` (milliseconds unless changed), shipping sends
the `String` text such as `1.5s`. `Any` with an error writes its `Error`
text on all three.

### Other Methods

| Method  | On       | Description |
|---------|----------|-------------|
| `Flush` | Logger   | No-op for slog, zerolog, logtest and `NopLogger`; on a `ShippingLogger` and its `With()` sub-loggers, posts queued records and waits up to `DefaultFlushTimeout` (10s) |
| `Send`  | LogEvent | Emit the log event |

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
Context fields are written before each event's own. A key given twice is
written twice (logtest keeps the last). Contexts are not safe for
concurrent use; the loggers are.

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

`gas.RequestLogger[T]()` does this for `request_id` (taken from chi's
`RequestID` middleware, which must run upstream), so mounting it gives
every handler a request logger without extra code. It also logs one
`request` line per request with method, path, status, bytes, duration and
remote address: info below 400, warn below 500, error from 500. Options:
`gas.WithRequestLoggerAppendRequestID(bool)` (default true) and
`gas.WithRequestLoggerBuilder(func(gas.Logger, *http.Request) gas.Logger)`
to add more fields. When T cannot be resolved it passes the request on and
logs nothing. With a scoped request-logger type (see DI Registration), it
also puts that logger in the request scope, so handlers that take the type
as a parameter get it.

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
    if logger == nil { // nil when no logger was stored
        logger = s.logger
    }
    logger.Debug("processing").Send()
}
```

## Shipping logs over HTTP

`NewShippingLogger` returns the constructor of a `*ShippingLogger`, a
`gas.Logger` that writes locally **and** ships every record to an HTTP
endpoint. It is a `SlogLogger` over a `ShippingHandler`: records are
captured by the handler, queued, batched, and posted by a background
sender goroutine. The wire shape is a pluggable `Marshaler`; an OTLP/HTTP
JSON marshaler ships in the box.

```go
logger, err := gaslog.NewShippingLogger(
    "https://collector.example:4318/v1/logs",
    gaslog.NewOTLPMarshaler(
        gaslog.WithServiceName("my-service"),
        gaslog.WithServiceVersion("1.4.2"),
    ),
    gaslog.WithHeader("Authorization", "Bearer "+os.Getenv("LOG_TOKEN")),
    gaslog.WithBatchSize(100),
    gaslog.WithFlushInterval(2*time.Second),
)()
if err != nil {
    return err
}
if err := logger.Init(ctx); err != nil { // starts the sender
    return err
}
defer logger.Close(ctx) // posts what is queued, within ctx

logger.Info("order placed").Str("order", "A-1").Int("items", 3).Send()
```

**`Init` is required.** The constructor does not start the sender. A
record logged before the first `Init` is written locally, dropped for
shipping, and reported as `ErrNotRunning`. Registered in the container,
`Init` and `Close` are called for you (Shutdown flushes the Logger, then
closes it last of all). `Init` after `Close` starts it again.

### Constructors

```go
func NewShippingLogger(endpoint string, m Marshaler, opts ...ShippingOption) func() (*ShippingLogger, error)

// A plain slog.Handler with the same pipeline, for an app that logs with
// log/slog directly (e.g. slog.SetDefault). It starts its sender at once;
// call Close(ctx) on shutdown.
func NewShippingHandler(endpoint string, m Marshaler, opts ...ShippingOption) (*ShippingHandler, error)
```

Both check, in order: `m` not nil (`ErrNilMarshaler`), the endpoint is an
absolute http or https URL (`ErrInvalidEndpoint`), then each option
(`ErrInvalidOption`).

| Method (ShippingLogger / ShippingHandler) | Behavior |
|---|---|
| `Init(ctx) error` (logger only) | Starts the sender. If an earlier Close's sender still runs, waits for it; if ctx ends first, returns an error wrapping `ErrStillClosing`. |
| `Flush()` / `Flush(ctx) error` | Posts what is queued and waits. The logger's version uses `DefaultFlushTimeout` and reports its error to the error handler. A failed post gives an error wrapping `ErrNotDelivered` with the count. If ctx ends first, nothing is dropped: records stay queued. |
| `Close(ctx) error` | Stops accepting records, posts what is queued within ctx. Later records are written locally only. Returns an `ErrNotDelivered` error on failure; Close of a closed sender returns nil. |
| `Dropped() uint64` | Records dropped since the handler was made. |
| `Name() string` (logger only) | `WithName`, default `DefaultShippingName` (`gas/log-shipping`). Two in one app need distinct names. |

### Options

| Option                           | Effect                                                                         | Default                                   |
|----------------------------------|--------------------------------------------------------------------------------|-------------------------------------------|
| `WithLevel(slog.Leveler)`        | Lowest level shipped (read per record, so a `*slog.LevelVar` works). Also seeds the default local handler. | `DefaultShippingLevel` (`slog.LevelInfo`) |
| `WithLocalHandler(slog.Handler)` | Handler that also receives every record it enables, before shipping.           | JSON to `os.Stderr` at the shipping level |
| `WithoutLocalHandler()`          | Disable local logging; ship only.                                               | local enabled                             |
| `WithHeader(key, value)`         | Request header on every batch (repeatable; applied after Content-Type, so it can replace it). | none                       |
| `WithBatchSize(n)`               | Records per post.                                                               | `DefaultBatchSize` (100)                  |
| `WithQueueSize(n)`               | Queue capacity in records; records drop when full so logging never blocks.     | `DefaultQueueSize` (1024)                 |
| `WithFlushInterval(d)`           | Every interval, all waiting records are posted.                                 | `DefaultFlushInterval` (2s)               |
| `WithHTTPClient(*http.Client)`   | Client used as is, redirect policy included.                                    | `DefaultHTTPTimeout` (10s), no redirects  |
| `WithErrorHandler(func(error))`  | Receives failure reports (see below).                                           | writes the error to `os.Stderr`           |
| `WithName(string)`               | `gas.Service` name.                                                             | `gas/log-shipping`                        |

Each option rejects a value it does not accept with `ErrInvalidOption`:
non-positive sizes or interval, a nil client, level, handler or function,
an empty name, a header name that is not a token or a value with control
characters (the error names neither).

### Delivery semantics

Best-effort by design: `Handle` never blocks. A record is dropped and
counted (`Dropped()`) when the queue is full, while the sender is not
running, when its batch fails to marshal, its post fails, or the response
is not 2xx, and when Close gives up on it. A failed batch is **not
retried**. The queue and batches are bounded by record count only, not
bytes. A post runs on the sender goroutine, so a slow endpoint fills the
queue. The default client follows no redirect: a 3xx is a failed post.

The local handler sees each record first; if it returns an error, `Handle`
returns it and reports `ErrLocalHandler`. The `Record` is built in
`Handle`, on the logging goroutine: attributes are flattened (group keys
joined by `.`), `LogValuer`s resolved, and `KindAny` values encoded (an
error or `fmt.Stringer` to its text, otherwise JSON, or
`!unencodable <type>` for channels, funcs and cycles).

### Error handler and sentinels

Failures go to the error handler on a goroutine of its own, so it may log
through the same logger or call `Flush`/`Close` without deadlock. Reports
are coalesced by class: at most one per class per flush interval, later
ones merged as `(N times)`. A panic in the handler is recovered.

| Sentinel | Meaning |
|---|---|
| `ErrNilLogger` | `WithSlogLogger` / `gaszerolog.WithLogger` got nil |
| `ErrNilMarshaler`, `ErrInvalidEndpoint`, `ErrInvalidOption` | Constructor errors |
| `ErrQueueFull`, `ErrNotRunning`, `ErrLocalHandler`, `ErrMarshal`, `ErrMarshalPanic`, `ErrPost`, `ErrStatus` | Reported to the error handler; `ErrLocalHandler`, `ErrMarshal`, `ErrPost` wrap the cause |
| `ErrNotDelivered` | Flush or Close returned with records not delivered; wraps the ctx error when ctx ended |
| `ErrStillClosing` | Init's ctx ended while an earlier Close's sender still ran |

Test with `errors.Is`. Every message starts with `gas/log: ` and never
contains the endpoint's user info, query or fragment, or a header name or
value. Drops after Close or from an ended Flush/Close context are counted
but not reported.

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

### Field-based routing and alerting (not built in: compose it)

**Level is the only routing axis the shipping pipeline provides.** The
`ShippingHandler` gates purely on level; the `Marshaler` only reshapes the
wire format. There is no built-in "ship this / drop that" by field and no
alerting. To route by a semantic field (e.g. drop router 404s, page an
admin on critical records) compose your own `slog.Handler` around a
ship-only handler and drive it from attributes you set at the call site:

```go
// Ship-only handler; its sender starts at once.
shipHandler, err := gaslog.NewShippingHandler(
    endpoint,
    gaslog.NewOTLPMarshaler(gaslog.WithServiceName("my-service")),
    gaslog.WithoutLocalHandler(),
    gaslog.WithLevel(slog.LevelError),
)
if err != nil {
    return err
}
defer shipHandler.Close(context.Background()) // drain on shutdown

console := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug})
root := routingHandler{console: console, ship: shipHandler, alerts: myPager}
logger, err := gaslog.NewSlogLogger(gaslog.WithSlogLogger(slog.New(root)))()
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

> `WithErrorHandler` is **only** for shipping failures. It is not a
> content-alert hook: alerting on log content is the routing handler's job.

### Marshaler and Record

To ship a different wire shape, implement `Marshaler`. `Record` is
backend-neutral (level, timestamp, message, flattened attributes):

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

`Marshal` is called on the sender goroutine, one batch at a time, and must
not keep the slice: the sender reuses its memory. A wrapping `Marshaler`
that filters `[]Record` before delegating is the lightest way to drop a
subset from the server (no alerting).

### OTLP marshaler

`NewOTLPMarshaler` emits one OTLP/HTTP JSON logs request per batch
(`resourceLogs -> scopeLogs -> logRecords`), `Content-Type`
`OTLPContentType` (`application/json`).

```go
func NewOTLPMarshaler(opts ...OTLPOption) *OTLPMarshaler
func (m *OTLPMarshaler) Replaced() uint64 // records sent with OTLPReplacedBody
```

| Option                        | Effect                                                                   |
|-------------------------------|--------------------------------------------------------------------------|
| `WithServiceName(name)`       | Sets `service.name` resource attr; also the scope name if none is set.   |
| `WithServiceVersion(version)` | Sets `service.version` resource attr.                                    |
| `WithResourceAttribute(k, v)` | Adds a resource attr (`host.name`, `deployment.environment`, ...); a later value of a key replaces an earlier one. |
| `WithScopeName(name)`         | Sets the instrumentation scope name.                                     |

`severityNumber` is the floor of the level's band: TRACE (1) below
`slog.LevelDebug`, DEBUG (5), INFO (9), WARN (13), ERROR (17).
`severityText` is `TRACE` for `LevelTrace`, else slog's level name (`INFO`,
`DEBUG-2`). `timeUnixNano` is a string, left out for a zero time. A record
whose value panics on encoding is sent with body `OTLPReplacedBody` rather
than failing the batch.

> **Trace correlation caveat:** the emitted `logRecord` carries only
> `timeUnixNano`, `severityNumber`, `severityText`, `body`, `attributes`. It
> has **no `traceId`/`spanId` fields**. A `trace_id` you add via a field lands
> under `attributes`, not in OTLP's native trace-correlation fields: good
> enough to *search* logs by trace id, but backends that correlate logs to
> traces via the native field won't pick it up. Write a custom `Marshaler`
> that lifts `trace_id`/`span_id` attrs into the top-level fields if you need that.

## Level Mapping

| gas.Logger method | Zerolog level        | Slog / Shipping level           |
|-------------------|----------------------|---------------------------------|
| `Trace`           | `zerolog.TraceLevel` | `gaslog.LevelTrace` (`DEBUG-4`) |
| `Debug`           | `zerolog.DebugLevel` | `slog.LevelDebug`               |
| `Info`            | `zerolog.InfoLevel`  | `slog.LevelInfo`                |
| `Warn`            | `zerolog.WarnLevel`  | `slog.LevelWarn`                |
| `Error`           | `zerolog.ErrorLevel` | `slog.LevelError`               |

A slog handler at `slog.LevelDebug` hides Trace; set the handler level to
`gaslog.LevelTrace` to see it.

## DI Registration

Register `gas.Logger` as a **singleton**. Most services take a `gas.Logger` in
their constructor, and the container rejects a singleton that depends on a
scoped service (`gas.ErrCaptiveDependency`). The constructors take no
dependencies from the container:

```go
sl := slog.New(slog.NewJSONHandler(os.Stdout, nil))

app := gas.NewApp(
    gas.WithSingletonService[gas.Logger](
        gaslog.NewSlogLogger(gaslog.WithSlogLogger(sl)),
    ),
    // ...other services
)
```

With zerolog:

```go
zl := zerolog.New(os.Stdout).With().Timestamp().Logger()

app := gas.NewApp(
    gas.WithSingletonService[gas.Logger](gaszerolog.New(gaszerolog.WithLogger(&zl))),
)
```

With shipping (the container calls `Init` and, at shutdown, `Flush` and
`Close`):

```go
app := gas.NewApp(
    gas.WithSingletonService[gas.Logger](gaslog.NewShippingLogger(
        "https://collector.example:4318/v1/logs",
        gaslog.NewOTLPMarshaler(gaslog.WithServiceName("shop")),
    )),
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

func (s *Service) Name() string                    { return "myservice" }
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
    "log/slog"
    "os"

    "github.com/gasmod/gas"
    gaslog "github.com/gasmod/gas/log"

    "myapp/myservice"
)

func main() {
    sl := slog.New(slog.NewJSONHandler(os.Stdout, nil))

    app := gas.NewApp(
        gas.WithSingletonService[gas.Logger](
            gaslog.NewSlogLogger(gaslog.WithSlogLogger(sl)),
        ),
        gas.WithSingletonService[*myservice.Service](myservice.New),
    )
    if err := app.Run(); err != nil {
        sl.Error("app stopped", "error", err)
        os.Exit(1)
    }
}
```
