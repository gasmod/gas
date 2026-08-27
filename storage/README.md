# gas/storage

[![Test](https://github.com/gasmod/gas/actions/workflows/test.yml/badge.svg)](https://github.com/gasmod/gas/actions/workflows/test.yml) [![Go Reference](https://pkg.go.dev/badge/github.com/gasmod/gas/storage.svg)](https://pkg.go.dev/github.com/gasmod/gas/storage) ![Go Version](https://img.shields.io/github/go-mod/go-version/gasmod/gas?filename=storage/go.mod) [![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)

Part of the [Gas](../README.md) monorepo · [Documentation](https://gasmod.github.io/gas) · [All modules](../README.md#modules)

Object storage for the [Gas](../README.md) framework, on AWS S3 and S3-compatible services
such as MinIO, LocalStack, and DigitalOcean Spaces.

Implements `gas.StorageProvider`.

```bash
go get github.com/gasmod/gas/storage
```

```go
gas.WithSingletonService[gas.StorageProvider](s3.New()),
```

`s3.New()` needs `gas.ConfigProvider` and `gas.Logger`.

| Package | Provides |
|---|---|
| `storage/s3` | AWS S3 and any S3-compatible object store |
| `storage/storagetest` | Recording mock of `gas.StorageProvider` |

## Documentation

The full guide, with configuration, testing, and worked examples, is on the docs site.
This README is deliberately a signpost: keeping a second copy here is how the docs drifted before.

- **Guide:** [Store and serve files](https://gasmod.github.io/gas/guides/files/)
- **API reference:** [pkg.go.dev/github.com/gasmod/gas/storage](https://pkg.go.dev/github.com/gasmod/gas/storage)
- **Contributing:** [CONTRIBUTING.md](../CONTRIBUTING.md)

## License

[MIT](LICENSE)
