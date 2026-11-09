# gas/config

[![Test](https://github.com/gasmod/gas/actions/workflows/test.yml/badge.svg)](https://github.com/gasmod/gas/actions/workflows/test.yml) [![Go Reference](https://pkg.go.dev/badge/github.com/gasmod/gas/config.svg)](https://pkg.go.dev/github.com/gasmod/gas/config) ![Go Version](https://img.shields.io/github/go-mod/go-version/gasmod/gas?filename=config/go.mod) [![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)

Part of the [Gas](../README.md)
monorepo · [Documentation](https://gasmod.dev) · [All modules](../README.md#modules)

Configuration for the [Gas](../README.md) framework. Loads from environment variables, JSON,
`.env` files, and AWS Secrets Manager, and binds the result to Go structs with validation.

Implements `gas.ConfigProvider`.

```bash
go get github.com/gasmod/gas/config
```

```go
cfg := config.New(config.WithProvider(config.NewEnvProvider(config.WithEnvPrefix("APP_"))))
if err := cfg.Load(); err != nil { log.Fatal(err) }

gas.WithServiceInstance[gas.ConfigProvider](cfg),
```

Config needs nothing from the container: it is built and loaded before the app, so a bad configuration fails before
anything else starts.

| Package                 | Provides                                                                         |
|-------------------------|----------------------------------------------------------------------------------|
| `config`                | Config; environment, JSON, and `.env` providers; environment detection extension |
| `config/secretsmanager` | AWS Secrets Manager                                                              |
| `config/configtest`     | Mock and a real-backed fake seeded with known values                             |

## Documentation

The full documentation is on the docs site.
This README is deliberately a signpost: keeping a second copy here is how the docs drifted before.

- **Docs:** [config](https://gasmod.dev/reference/config/)
- **API reference:** [pkg.go.dev/github.com/gasmod/gas/config](https://pkg.go.dev/github.com/gasmod/gas/config)
- **Contributing:** [CONTRIBUTING.md](../CONTRIBUTING.md)

## License

[MIT](LICENSE)
