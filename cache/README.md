# gas/cache

[![Test](https://github.com/gasmod/gas/actions/workflows/test.yml/badge.svg)](https://github.com/gasmod/gas/actions/workflows/test.yml) [![Go Reference](https://pkg.go.dev/badge/github.com/gasmod/gas/cache.svg)](https://pkg.go.dev/github.com/gasmod/gas/cache) ![Go Version](https://img.shields.io/github/go-mod/go-version/gasmod/gas?filename=cache/go.mod) [![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)

Part of the [Gas](../README.md) monorepo · [Documentation](https://gasmod.github.io/gas) · [All modules](../README.md#modules)

Key-value caching for the [Gas](../README.md) framework, with two interchangeable stores that
keep one contract: in-memory with LRU eviction, and Valkey (Redis-compatible) with key prefixes,
per-command timeouts, TLS and ACL users.

Implements `gas.CacheProvider`.

```bash
go get github.com/gasmod/gas/cache
```

```go
gas.WithSingletonService[gas.CacheProvider](cachemem.New()),
```

Both stores need `gas.ConfigProvider` and `gas.Logger`, and read the `cache` section; the Valkey
store requires `cache.addr`.

| Package | Provides |
|---|---|
| `cache` | The shared contract and sentinel errors |
| `cache/memory` | In-process store, for development, tests and single-instance apps |
| `cache/valkey` | Valkey store, for apps that share a cache across instances |
| `cache/cachetest` | Recording mock of `gas.CacheProvider` |

## Documentation

The full guide, with configuration, testing, and worked examples, is on the docs site.
This README is deliberately a signpost: keeping a second copy here is how the docs drifted before.

- **Guide:** [Cache](https://gasmod.github.io/gas/modules/cache/)
- **API reference:** [pkg.go.dev/github.com/gasmod/gas/cache](https://pkg.go.dev/github.com/gasmod/gas/cache)
- **Contributing:** [CONTRIBUTING.md](../CONTRIBUTING.md)

## License

[MIT](LICENSE)
