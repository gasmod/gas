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
gas.WithSingletonService[gas.TemplateProvider](templatefs.NewStore(os.DirFS("templates"))),
```

`db.NewStore()` needs `gas.DatabaseProvider`, `gas.Logger` and `gas.MigrationManager`. The
others need nothing; `dir` and `fs` return a constructor you call yourself.

| Package | Use case |
|---|---|
| `template/memory` | Development, tests, ephemeral content |
| `template/dir` | A directory on disk, with an in-memory overlay |
| `template/fs` | Read-only over any `fs.FS`, including `embed.FS` |
| `template/db` | Persistent and multi-instance: PostgreSQL, MySQL, SQLite |
| `template/composite` | Chain providers: write to the first, read through all |
| `template/templatetest` | Recording mock of `gas.TemplateProvider` |

## Documentation

The full guide, with configuration, testing, and worked examples, is on the docs site.
This README is deliberately a signpost: keeping a second copy here is how the docs drifted before.

- **Guide:** [Serve HTML](https://gasmod.github.io/gas/guides/html/)
- **API reference:** [pkg.go.dev/github.com/gasmod/gas/template](https://pkg.go.dev/github.com/gasmod/gas/template)
- **Contributing:** [CONTRIBUTING.md](../CONTRIBUTING.md)

## License

[MIT](LICENSE)
