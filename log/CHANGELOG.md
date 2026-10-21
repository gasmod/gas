# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

- **BREAKING: the logger constructors return an error.** `ZeroLogLoggerCtor`,
  `SlogLoggerCtor` and `ShippingLoggerCtor`, which `NewZeroLogLogger`,
  `NewSlogLogger` and `NewShippingLogger` return, are now
  `func() (*T, error)`, the `CONVENTIONS.md` shape. They cannot fail yet, so
  they return nil; the change is the shape. Registering them with
  `gas.WithSingletonService` is unchanged, since the container accepts a
  trailing error. Code that calls the constructor directly, as in
  `gaslog.NewSlogLogger()()`, must take the second result. `NewShippingHandler`
  and `NewOTLPMarshaler`, which apps build directly, are unchanged (gas#106).
- `ShippingLogger` implements the context-aware `gas.Service`: `Init(ctx)` and
  `Close(ctx)`. `Init` stays a no-op (the delivery goroutine starts at
  construction and never derives from the Init ctx). `Close` ignores ctx; the
  drain is bounded by the HTTP client timeout. Not marked breaking because
  `ShippingLogger` has not been released yet.
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

### Security

- **Requires Go 1.27.3.** The `go` directive moves from 1.27 to 1.27.3,
  the first 1.27 release with the fix for GO-2026-6809 (CVE-2026-111943):
  `crypto/x509` now rejects intermediate certificates known to be
  compromised. A build with an older toolchain switches to 1.27.3 under
  `GOTOOLCHAIN=auto` and fails under `GOTOOLCHAIN=local`.
