// Package log provides pluggable logging backends for the Gas ecosystem.
//
// It implements the [gas.Logger], [gas.LogEvent], and [gas.LoggerContext]
// interfaces with three interchangeable backends:
//
//   - [ZeroLogLogger] — high-performance structured logging via [zerolog].
//   - [SlogLogger] — standard library [log/slog] backend, zero external dependencies.
//   - [NoOpLogger] — discards all output; useful for tests and disabled logging.
//
// All backends expose the same fluent API: call a level method (Info, Debug, etc.)
// to obtain a [gas.LogEvent], chain field methods (Str, Int, Err, ...), and
// finalize with Send.
//
//	logger.Info("request handled").
//		Str("method", r.Method).
//		Int("status", code).
//		Duration("latency", elapsed).
//		Send()
//
// Use With to create a sub-logger with persistent fields:
//
//	reqLogger := logger.With().Str("request_id", id).Logger()
//
// Register a logger in the Gas DI container so services receive it automatically:
//
//	app := gas.NewApp(
//		gas.WithService[gas.Logger](gaslog.NewZeroLogLogger(gaslog.WithZeroLogInstance(&zl)), gas.ServiceLifetimeScoped),
//	)
//
//nolint:revive // intentional package name
package log
