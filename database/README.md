# gas/database

[![Test](https://github.com/gasmod/gas/actions/workflows/test.yml/badge.svg)](https://github.com/gasmod/gas/actions/workflows/test.yml) [![Go Reference](https://pkg.go.dev/badge/github.com/gasmod/gas/database.svg)](https://pkg.go.dev/github.com/gasmod/gas/database) ![Go Version](https://img.shields.io/github/go-mod/go-version/gasmod/gas?filename=database/go.mod) [![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)

Part of the [Gas](../README.md) monorepo · [Documentation](https://gasmod.github.io/gas) · [All modules](../README.md#modules)

Database connections for the [Gas](../README.md) framework. Wraps `database/sql` and native
`pgxpool` with connection management, transaction helpers, and sqlc compatibility.

Implements `gas.DatabaseProvider`.

```bash
go get github.com/gasmod/gas/database
```

```go
gas.WithSingletonService[gas.DatabaseProvider](database.New()),
```

`database.New()` needs `gas.ConfigProvider` and `gas.Logger`. The configuration names a
dialect, `postgres`, `mysql` or `sqlite`, and a DSN; SQLite needs the app to import
`modernc.org/sqlite`.

| Mode                         | Backend                                                                       |
|------------------------------|-------------------------------------------------------------------------------|
| `database.ModeSQL` (default) | `database/sql` with the dialect's driver, or a connector from `WithConnector` |
| `database.ModePgx`           | Native `pgxpool` for PostgreSQL, for pgx types and batching                   |

## Documentation

The full guide, with configuration, testing, and worked examples, is on the docs site.
This README is deliberately a signpost: keeping a second copy here is how the docs drifted before.

- **Guide:** [Database](https://gasmod.github.io/gas/modules/database/)
- **API reference:** [pkg.go.dev/github.com/gasmod/gas/database](https://pkg.go.dev/github.com/gasmod/gas/database)
- **Contributing:** [CONTRIBUTING.md](../CONTRIBUTING.md)

## License

[MIT](LICENSE)
