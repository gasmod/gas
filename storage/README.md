# gas/storage

[![Test](https://github.com/gasmod/gas/actions/workflows/test.yml/badge.svg)](https://github.com/gasmod/gas/actions/workflows/test.yml) [![Go Reference](https://pkg.go.dev/badge/github.com/gasmod/gas/storage.svg)](https://pkg.go.dev/github.com/gasmod/gas/storage) ![Go Version](https://img.shields.io/github/go-mod/go-version/gasmod/gas?filename=storage/go.mod) [![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)

Part of the [Gas](../README.md) monorepo · [Documentation](https://gasmod.github.io/gas) · [All modules](../README.md#modules)

Object storage for the [Gas](../README.md) framework, on AWS S3 and S3-compatible services
such as MinIO and LocalStack, or on a local directory.

Defines `storage.Provider` and `storage.Presigner`.

```bash
go get github.com/gasmod/gas/storage
```

```go
gas.WithSingletonService[storage.Provider](s3.NewProvider()),
gas.WithSingletonService[storage.Presigner](s3.NewPresigner()),
```

Both constructors need a `gas.ConfigProvider`.

| Package               | Provides                                                                     |
|-----------------------|------------------------------------------------------------------------------|
| `storage`             | The `Provider` and `Presigner` interfaces, their inputs, and sentinel errors |
| `storage/s3`          | AWS S3 and any S3-compatible object store                                    |
| `storage/dir`         | A directory on local disk, through an `os.Root`                              |
| `storage/storagetest` | Recording mocks of `storage.Provider` and `storage.Presigner`                |

## Documentation

The full guide, with configuration, testing, and worked examples, is on the docs site.
This README is deliberately a signpost: keeping a second copy here is how the docs drifted before.

- **Guide:** [Storage](https://gasmod.github.io/gas/modules/storage/)
- **API reference:** [pkg.go.dev/github.com/gasmod/gas/storage](https://pkg.go.dev/github.com/gasmod/gas/storage)
- **Contributing:** [CONTRIBUTING.md](../CONTRIBUTING.md)

## License

[MIT](LICENSE)
