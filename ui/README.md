# gas/ui

[![Test](https://github.com/gasmod/gas/actions/workflows/test.yml/badge.svg)](https://github.com/gasmod/gas/actions/workflows/test.yml) [![Go Reference](https://pkg.go.dev/badge/github.com/gasmod/gas/ui.svg)](https://pkg.go.dev/github.com/gasmod/gas/ui) ![Go Version](https://img.shields.io/github/go-mod/go-version/gasmod/gas?filename=ui/go.mod) [![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)

Part of the [Gas](../README.md) monorepo · [Documentation](https://gasmod.github.io/gas) · [All modules](../README.md#modules)

HTML rendering for the [Gas](../README.md) framework with `html/template`: layouts, partials,
fragments for HTMX swaps, and static files served from inside an `os.Root`, over any
[gas/template](../template/README.md) store.

Implements `ui.Provider`, the contract this module owns.

```bash
go get github.com/gasmod/gas/ui
```

```go
gas.WithSingletonService[ui.Provider](ui.New[gas.TemplateProvider]()),
```

`ui.New[T]()` needs `T` (a `gas.TemplateProvider`), `*gas.Router`, `gas.ConfigProvider` and
`gas.Logger`, and reads the `ui` section. With a static directory or file system configured, it
registers the static routes, `/static/*` by default; there is none by default.

| Package | Provides |
|---|---|
| `ui` | `Provider`, the service, render options, `StaticHandler` and sentinel errors |
| `ui/uitest` | Recording mock of `ui.Provider` |

## Documentation

The full documentation is on the docs site.
This README is deliberately a signpost: keeping a second copy here is how the docs drifted before.

- **Docs:** [ui](https://gasmod.github.io/gas/reference/ui/)
- **API reference:** [pkg.go.dev/github.com/gasmod/gas/ui](https://pkg.go.dev/github.com/gasmod/gas/ui)
- **Contributing:** [CONTRIBUTING.md](../CONTRIBUTING.md)

## License

[MIT](LICENSE)
