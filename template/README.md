# gas/template

[![Test](https://github.com/gasmod/gas/actions/workflows/test.yml/badge.svg)](https://github.com/gasmod/gas/actions/workflows/test.yml) [![Go Reference](https://pkg.go.dev/badge/github.com/gasmod/gas/template.svg)](https://pkg.go.dev/github.com/gasmod/gas/template) ![Go Version](https://img.shields.io/github/go-mod/go-version/gasmod/gas?filename=template/go.mod) [![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)

Part of the [Gas](../README.md) monorepo · [Documentation](https://gasmod.github.io/gas) · [All modules](../README.md#modules)

Template storage for the [Gas](../README.md) framework. Holds raw template content so that
rendering, in `gas/ui` or `gas/email`, stays decoupled from where templates live.

Implements `gas.TemplateProvider`.

```bash
go get github.com/gasmod/gas/template
```

```go
gas.WithSingletonService[gas.TemplateProvider](dir.NewStore("templates")),
```

Every `NewStore` returns a constructor for the container. `db.NewStore()` needs
`gas.DatabaseProvider`, `gas.Logger` and `gas.MigrationManager`; the others need nothing.
Stored templates are code: never let users write them.

| Package | Provides |
|---|---|
| `github.com/gasmod/gas/template` | Sentinel errors, the name and extension rules |
| `github.com/gasmod/gas/template/memory` | A store in memory |
| `github.com/gasmod/gas/template/fs` | A read-only store over any `fs.FS`, such as `embed.FS` |
| `github.com/gasmod/gas/template/dir` | A directory on disk through `os.Root`, with registered templates over it |
| `github.com/gasmod/gas/template/db` | A table in PostgreSQL, MySQL or SQLite, with its migrations |
| `github.com/gasmod/gas/template/composite` | A writable store over read-only fallbacks |
| `github.com/gasmod/gas/template/templatetest` | `MockTemplate` for tests |

## Documentation

The full guide, with configuration, testing, and worked examples, is on the docs site.
This README is deliberately a signpost: keeping a second copy here is how the docs drifted before.

- **Guide:** [Templates](https://gasmod.github.io/gas/modules/templates/)
- **API reference:** [pkg.go.dev/github.com/gasmod/gas/template](https://pkg.go.dev/github.com/gasmod/gas/template)
- **Contributing:** [CONTRIBUTING.md](../CONTRIBUTING.md)

## License

[MIT](LICENSE)
