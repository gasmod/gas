# gas/log

[![Test](https://github.com/gasmod/gas/actions/workflows/test.yml/badge.svg)](https://github.com/gasmod/gas/actions/workflows/test.yml) [![Go Reference](https://pkg.go.dev/badge/github.com/gasmod/gas/log.svg)](https://pkg.go.dev/github.com/gasmod/gas/log) ![Go Version](https://img.shields.io/github/go-mod/go-version/gasmod/gas?filename=log/go.mod) [![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)

Logging backends for the [Gas](https://github.com/gasmod/gas) ecosystem. Implements the `gas.Logger`, `gas.LogEvent`, `gas.LoggerContext`, and `gas.MutableLoggerContext` interfaces with three interchangeable backends.

```
go get github.com/gasmod/gas/log
```

## Backends

| Backend     | Constructor                                        | Backing library                             | Notes                                                                         |
|-------------|----------------------------------------------------|---------------------------------------------|-------------------------------------------------------------------------------|
| **Zerolog** | `NewZeroLogLogger(opts ...ZeroLogLoggerOption)`    | [rs/zerolog](https://github.com/rs/zerolog) | High-performance structured JSON logging. Full level support including Trace. |
| **Slog**    | `NewSlogLogger(opts ...SlogLoggerOption)`          | `log/slog` (stdlib)                         | Zero-dependency option. Trace maps to Debug (slog has no Trace level).        |

Each constructor returns a constructor function type (`ZeroLogLoggerCtor`, `SlogLoggerCtor`) compatible with the Gas DI container. When no options are provided, backends use sensible defaults: Zerolog uses the global `zerolog/log.Logger`; Slog uses `slog.Default()` with an initial event capacity of 5.

## Usage

### Zerolog

```go
package main

import (
	"os"

	"github.com/rs/zerolog"
	gaslog "github.com/gasmod/gas/log"
)

func main() {
	zl := zerolog.New(os.Stdout).With().Timestamp().Logger()
	ctor := gaslog.NewZeroLogLogger(gaslog.WithZeroLogInstance(&zl))
	logger := ctor()

	logger.Info("server started").Str("addr", ":8080").Send()
	// {"level":"info","addr":":8080","time":"...","message":"server started"}
}
```

### Slog

```go
package main

import (
	"log/slog"
	"os"

	gaslog "github.com/gasmod/gas/log"
)

func main() {
	sl := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctor := gaslog.NewSlogLogger(
		gaslog.WithSlogInstance(sl),
		gaslog.WithEventInitialCapacity(5),
	)
	logger := ctor()

	logger.Info("server started").Str("addr", ":8080").Send()
	// {"level":"INFO","msg":"server started","addr":":8080"}
}
```

## Fluent API

All backends share the same fluent interface defined by `gas.Logger`:

```go
// Create a log event, chain fields, then send
logger.Info("request handled").
    Str("method", "GET").
    Str("path", "/api/users").
    Int("status", 200).
    Duration("latency", elapsed).
    Send()

// Create a sub-logger with persistent fields
reqLogger := logger.With().
    Str("request_id", reqID).
    Str("service", "auth").
    Logger()

reqLogger.Debug("validating token").Send()
```

### Mutating base fields

Instead of branching into a new sub-logger, `SetBaseFields()` accumulates fields and on `Apply()` mutates the originating logger in-place. Intended for request-scoped middleware that shares one logger instance across the whole request:

```go
// Attach persistent fields directly to an existing logger (in-place mutation)
logger.SetBaseFields().
    Str("request_id", reqID).
    Str("user_id", userID).
    Apply()
// all subsequent events from logger include request_id and user_id
```

### Available field methods

| Method     | Signature                         |
|------------|-----------------------------------|
| `Str`      | `(key, val string)`               |
| `Int`      | `(key string, val int)`           |
| `Int64`    | `(key string, val int64)`         |
| `Float64`  | `(key string, val float64)`       |
| `Bool`     | `(key string, val bool)`          |
| `Err`      | `(key string, val error)`         |
| `Duration` | `(key string, val time.Duration)` |
| `Any`      | `(key string, val any)`           |

These methods are available on `gas.LogEvent` (returned by level methods), `gas.LoggerContext` (returned by `With()`), and `gas.MutableLoggerContext` (returned by `SetBaseFields()`).

## Shipping logs over HTTP

`NewShippingLogger` returns a `gas.Logger` that writes locally **and** ships every record to an HTTP endpoint. It is built on the Slog backend: records are captured by an `slog.Handler`, batched, and delivered by a background goroutine. The wire shape is a pluggable `Marshaler`, so the transport is reusable across schemas; an OTLP/HTTP JSON marshaler ships in the box.

```go
logger := gaslog.NewShippingLogger(
    "https://logs.example.com/v1/logs",
    gaslog.NewOTLPMarshaler(
        gaslog.WithServiceName("my-service"),
        gaslog.WithServiceVersion("1.4.2"),
    ),
    gaslog.WithHeader("X-API-Key", os.Getenv("LOG_KEY")),
    gaslog.WithBatchSize(100),
    gaslog.WithFlushInterval(2*time.Second),
)()

logger.Info("request handled").Str("method", "GET").Int("status", 200).Send()
```

By default it also logs JSON to stderr; pass `WithLocalHandler` to change the local sink or `WithoutLocalHandler` to ship only. Delivery is best-effort: records are dropped (never blocked) when the queue is full, and delivery failures go to `WithErrorHandler` rather than the logging call site.

The returned logger implements `gas.Service`: when registered in the DI container, `Close()` is called at shutdown and drains any buffered records. Outside the container, call `Flush()` before exit or `Close()` to stop the delivery goroutine.

To ship a different wire shape, implement `Marshaler`:

```go
type Marshaler interface {
    Marshal(records []Record) ([]byte, error)
    ContentType() string
}
```

## DI Integration

Register a logger in the Gas DI container by passing the constructor function to `gas.WithService`:

```go
package main

import (
	"os"

	"github.com/gasmod/gas"
	gaslog "github.com/gasmod/gas/log"
	"github.com/rs/zerolog"
)

func main() {
	zl := zerolog.New(os.Stdout).With().Timestamp().Logger()

	app := gas.NewApp(
		gas.WithService[gas.Logger](gaslog.NewZeroLogLogger(gaslog.WithZeroLogInstance(&zl)), gas.ServiceLifetimeScoped),
		// ...
	)
	app.Run()
}
```

Services receive the logger through their constructor:

```go
type MyService struct {
	logger gas.Logger
}

func NewMyService(logger gas.Logger) *MyService {
	return &MyService{logger: logger}
}

func (s *MyService) Init() error {
	s.logger.Info("service initialized").Str("name", s.Name()).Send()
	return nil
}
```

### Context-scoped logging

Use `gas.WithLogger` and `gas.LoggerFromContext` to propagate loggers through `context.Context`:

```go
func (s *MyService) handleRequest(w http.ResponseWriter, r *http.Request) {
	reqLogger := s.logger.With().
		Str("request_id", r.Header.Get("X-Request-ID")).
		Logger()

	ctx := gas.WithLogger(r.Context(), reqLogger)
	s.process(ctx)
}

func (s *MyService) process(ctx context.Context) {
	logger := gas.LoggerFromContext(ctx)
	logger.Debug("processing").Send()
}
```

## Level Mapping

| gas.Logger method | Zerolog level        | Slog level        |
|-------------------|----------------------|-------------------|
| `Trace`           | `zerolog.TraceLevel` | `slog.LevelDebug` |
| `Debug`           | `zerolog.DebugLevel` | `slog.LevelDebug` |
| `Info`            | `zerolog.InfoLevel`  | `slog.LevelInfo`  |
| `Warn`            | `zerolog.WarnLevel`  | `slog.LevelWarn`  |
| `Error`           | `zerolog.ErrorLevel` | `slog.LevelError` |
