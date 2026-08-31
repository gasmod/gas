# gas/log

[![Test](https://github.com/gasmod/gas/actions/workflows/test.yml/badge.svg)](https://github.com/gasmod/gas/actions/workflows/test.yml) [![Go Reference](https://pkg.go.dev/badge/github.com/gasmod/gas/log.svg)](https://pkg.go.dev/github.com/gasmod/gas/log) ![Go Version](https://img.shields.io/github/go-mod/go-version/gasmod/gas?filename=log/go.mod) [![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)

Part of the [Gas](../README.md) monorepo · [Documentation](https://gasmod.github.io/gas) · [All modules](../README.md#modules)

Structured logging for the [Gas](../README.md) framework. Two local backends, plus batched
log shipping over HTTP with an OTLP marshaler included.

Implements `gas.Logger`, `gas.LogEvent`, `gas.LoggerContext`, `gas.MutableLoggerContext`.

```bash
go get github.com/gasmod/gas/log
```

```go
gas.WithSingletonService[gas.Logger](gaslog.NewSlogLogger()),
```

Nothing. The constructors take no dependencies.

| Constructor | Backend |
|---|---|
| `NewZeroLogLogger()` | [rs/zerolog](https://github.com/rs/zerolog); full level support including Trace |
| `NewSlogLogger()` | `log/slog`; no extra dependencies, Trace maps to Debug |
| `NewShippingLogger()` | Writes locally and batches records to an HTTP endpoint |

## Documentation

The full guide, with configuration, testing, and worked examples, is on the docs site.
This README is deliberately a signpost: keeping a second copy here is how the docs drifted before.

- **Guide:** [Structured logging](https://gasmod.github.io/gas/guides/logging/)
- **API reference:** [pkg.go.dev/github.com/gasmod/gas/log](https://pkg.go.dev/github.com/gasmod/gas/log)
- **Contributing:** [CONTRIBUTING.md](../CONTRIBUTING.md)

## License

[MIT](LICENSE)
