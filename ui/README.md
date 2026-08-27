# gas/ui

[![Test](https://github.com/gasmod/gas/actions/workflows/test.yml/badge.svg)](https://github.com/gasmod/gas/actions/workflows/test.yml) [![Go Reference](https://pkg.go.dev/badge/github.com/gasmod/gas/ui.svg)](https://pkg.go.dev/github.com/gasmod/gas/ui) ![Go Version](https://img.shields.io/github/go-mod/go-version/gasmod/gas?filename=ui/go.mod) [![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)

Part of the [Gas](../README.md) monorepo · [Documentation](https://gasmod.github.io/gas) · [All modules](../README.md#modules)

HTML rendering for the [Gas](../README.md) framework: layouts, partials, static file serving,
and HTMX fragments, over a pluggable [gas/template](../template/README.md) backend.

Implements `gas.UIProvider`.

```bash
go get github.com/gasmod/gas/ui
```

```go
gas.WithSingletonService[gas.UIProvider](ui.New[gas.TemplateProvider]()),
```

`ui.New[T]()` needs `T` (a `gas.TemplateProvider`), `*gas.Router`, `gas.ConfigProvider` and
`gas.Logger`.

| Method | Renders |
|---|---|
| `Render` | Page wrapped in its layout |
| `RenderFragment` | Page without the layout, for HTMX swaps |
| `RenderWithStatus` | Render at an explicit status code |
| `RegisterFuncs` | Contribute template helpers from any service |

## Documentation

The full guide, with configuration, testing, and worked examples, is on the docs site.
This README is deliberately a signpost: keeping a second copy here is how the docs drifted before.

- **Guide:** [Serve HTML](https://gasmod.github.io/gas/guides/html/)
- **API reference:** [pkg.go.dev/github.com/gasmod/gas/ui](https://pkg.go.dev/github.com/gasmod/gas/ui)
- **Contributing:** [CONTRIBUTING.md](../CONTRIBUTING.md)

## License

[MIT](LICENSE)
