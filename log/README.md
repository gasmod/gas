# gas/log

[![Test](https://github.com/gasmod/gas/actions/workflows/test.yml/badge.svg)](https://github.com/gasmod/gas/actions/workflows/test.yml) [![Go Reference](https://pkg.go.dev/badge/github.com/gasmod/gas/log.svg)](https://pkg.go.dev/github.com/gasmod/gas/log) ![Go Version](https://img.shields.io/github/go-mod/go-version/gasmod/gas?filename=log/go.mod) [![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)

Part of the [Gas](../README.md) monorepo · [Documentation](https://gasmod.github.io/gas) · [All modules](../README.md#modules)

Structured logging for the [Gas](../README.md) framework: a `log/slog` backend, a zerolog
backend, and batched shipping of records over HTTP, with an OTLP/JSON marshaler included.

Implements `gas.Logger`; `ShippingLogger` is also a `gas.Service`.

```bash
go get github.com/gasmod/gas/log
```

```go
gas.WithSingletonService[gas.Logger](gaslog.NewSlogLogger()),
```

The constructors take no dependencies from the container. `NewShippingLogger` takes the
endpoint and a `Marshaler`, such as `gaslog.NewOTLPMarshaler()`.

| Package | Provides |
|---|---|
| `github.com/gasmod/gas/log` | `NewSlogLogger`, `NewShippingLogger`, `NewShippingHandler`, `NewOTLPMarshaler`; standard library only |
| `github.com/gasmod/gas/log/zerolog` | `New`, a logger over [rs/zerolog](https://github.com/rs/zerolog) |
| `github.com/gasmod/gas/log/logtest` | `RecordingLogger`, a `gas.Logger` that records events for tests |

## Documentation

The full guide, with configuration, testing, and worked examples, is on the docs site.
This README is deliberately a signpost: keeping a second copy here is how the docs drifted before.

- **Guide:** [Logging](https://gasmod.github.io/gas/modules/logging/)
- **API reference:** [pkg.go.dev/github.com/gasmod/gas/log](https://pkg.go.dev/github.com/gasmod/gas/log)
- **Contributing:** [CONTRIBUTING.md](../CONTRIBUTING.md)

## License

[MIT](LICENSE)
