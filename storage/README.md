# gas/storage

[![Test](https://github.com/gasmod/gas/actions/workflows/test.yml/badge.svg)](https://github.com/gasmod/gas/actions/workflows/test.yml) [![Go Reference](https://pkg.go.dev/badge/github.com/gasmod/gas/storage.svg)](https://pkg.go.dev/github.com/gasmod/gas/storage) ![Go Version](https://img.shields.io/github/go-mod/go-version/gasmod/gas?filename=storage/go.mod) [![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)

Part of the [Gas](../README.md) monorepo · [Documentation](https://gasmod.github.io/gas) · [All modules](../README.md#modules)

Object storage for the [Gas](../README.md) framework, with backends that keep one contract, Amazon
S3's rules, checked before any I/O: Amazon S3 or a server that speaks its API, a directory on a
local disk, and an in-memory store for tests and local development.

Defines `storage.Provider` and `storage.Presigner`.

```bash
go get github.com/gasmod/gas/storage
```

```go
gas.WithSingletonService[storage.Provider](memory.New()),
```

Each backend is a `gas.Service`, needs `gas.ConfigProvider` and `gas.Logger`, and reads the
`storage` section.

| Package               | Provides                                                                             |
|-----------------------|--------------------------------------------------------------------------------------|
| `storage`             | The `Provider` and `Presigner` contract, its input rules, limits and sentinel errors |
| `storage/s3`          | Amazon S3 and S3-compatible services                                                 |
| `storage/dir`         | A directory on local disk, through an `os.Root`                                      |
| `storage/memory`      | In-process store with S3's rules, for tests and local development                    |
| `storage/storagetest` | Recording mocks of `storage.Provider` and `storage.Presigner`                        |

## Documentation

The full documentation is on the docs site.
This README is deliberately a signpost: keeping a second copy here is how the docs drifted before.

- **Docs:** [storage](https://gasmod.github.io/gas/reference/storage/)
- **API reference:** [pkg.go.dev/github.com/gasmod/gas/storage](https://pkg.go.dev/github.com/gasmod/gas/storage)
- **Contributing:** [CONTRIBUTING.md](../CONTRIBUTING.md)

## License

[MIT](LICENSE)
