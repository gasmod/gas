# gas/migrate

[![Test](https://github.com/gasmod/gas/actions/workflows/test.yml/badge.svg)](https://github.com/gasmod/gas/actions/workflows/test.yml) [![Go Reference](https://pkg.go.dev/badge/github.com/gasmod/gas/migrate.svg)](https://pkg.go.dev/github.com/gasmod/gas/migrate) ![Go Version](https://img.shields.io/github/go-mod/go-version/gasmod/gas?filename=migrate/go.mod) [![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)

Part of the [Gas](../README.md) monorepo · [Documentation](https://gasmod.github.io/gas) · [All modules](../README.md#modules)

Database migrations for the [Gas](../README.md) framework. Services declare the schema they
own; migrate applies everything in global version order, with dirty-state detection and rollback.

Implements `gas.MigrationManager`.

```bash
go get github.com/gasmod/gas/migrate
```

```go
gas.WithSingletonService[gas.MigrationManager](migrate.New()),
```

`migrate.New()` needs `gas.DatabaseProvider`.

| Dialect | Driver names |
|---|---|
| PostgreSQL | `postgres`, `pgx` |
| MySQL | `mysql` |
| SQLite | `sqlite`, `sqlite3` |

## Documentation

The full guide, with configuration, testing, and worked examples, is on the docs site.
This README is deliberately a signpost: keeping a second copy here is how the docs drifted before.

- **Guide:** [Connect a database](https://gasmod.github.io/gas/guides/database/)
- **API reference:** [pkg.go.dev/github.com/gasmod/gas/migrate](https://pkg.go.dev/github.com/gasmod/gas/migrate)
- **Contributing:** [CONTRIBUTING.md](../CONTRIBUTING.md)

## License

[MIT](LICENSE)
