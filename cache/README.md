# gas/cache

[![Test](https://github.com/gasmod/gas/actions/workflows/test.yml/badge.svg)](https://github.com/gasmod/gas/actions/workflows/test.yml) [![Go Reference](https://pkg.go.dev/badge/github.com/gasmod/gas/cache.svg)](https://pkg.go.dev/github.com/gasmod/gas/cache) ![Go Version](https://img.shields.io/github/go-mod/go-version/gasmod/gas?filename=cache/go.mod) [![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)

Part of the [Gas](../README.md) monorepo · [Documentation](https://gasmod.github.io/gas) · [All modules](../README.md#modules)

Key-value caching for the [Gas](../README.md) framework, with two interchangeable backends:
in-memory for development, Valkey (Redis-compatible) for production.

Implements `gas.CacheProvider`.

```bash
go get github.com/gasmod/gas/cache
```

```go
gas.WithSingletonService[gas.CacheProvider](cachemem.New()),
```

Both backends need `gas.ConfigProvider` and `gas.Logger`.

| Package | Use case |
|---|---|
| `cache/memory` | Development, testing, single-instance deployments |
| `cache/valkey` | Production, multi-instance deployments |
| `cache/cachetest` | Recording mock of `gas.CacheProvider` |

## Documentation

The full guide, with configuration, testing, and worked examples, is on the docs site.
This README is deliberately a signpost: keeping a second copy here is how the docs drifted before.

- **Guide:** [Cache expensive work](https://gasmod.github.io/gas/guides/caching/)
- **API reference:** [pkg.go.dev/github.com/gasmod/gas/cache](https://pkg.go.dev/github.com/gasmod/gas/cache)
- **Contributing:** [CONTRIBUTING.md](../CONTRIBUTING.md)

## License

[MIT](LICENSE)
