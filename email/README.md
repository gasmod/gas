# gas/email

[![Test](https://github.com/gasmod/gas/actions/workflows/test.yml/badge.svg)](https://github.com/gasmod/gas/actions/workflows/test.yml) [![Go Reference](https://pkg.go.dev/badge/github.com/gasmod/gas/email.svg)](https://pkg.go.dev/github.com/gasmod/gas/email) ![Go Version](https://img.shields.io/github/go-mod/go-version/gasmod/gas?filename=email/go.mod) [![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)

Part of the [Gas](../README.md) monorepo · [Documentation](https://gasmod.github.io/gas) · [All modules](../README.md#modules)

Transactional email for the [Gas](../README.md) framework, over AWS SES, with optional
template rendering through `gas.TemplateProvider`.

Implements `gas.EmailProvider`.

```bash
go get github.com/gasmod/gas/email
```

```go
gas.WithSingletonService[gas.EmailProvider](ses.New()),
```

`ses.New()` needs `gas.TemplateProvider`, `gas.ConfigProvider` and `gas.Logger`, even if you
only ever call `Send`.

| Package | Provides |
|---|---|
| `email/ses` | AWS SES, and LocalStack via a custom endpoint |
| `email/emailtest` | Recording mock of `gas.EmailProvider` |

## Documentation

The full guide, with configuration, testing, and worked examples, is on the docs site.
This README is deliberately a signpost: keeping a second copy here is how the docs drifted before.

- **Guide:** [Send email](https://gasmod.github.io/gas/guides/email/)
- **API reference:** [pkg.go.dev/github.com/gasmod/gas/email](https://pkg.go.dev/github.com/gasmod/gas/email)
- **Contributing:** [CONTRIBUTING.md](../CONTRIBUTING.md)

## License

[MIT](LICENSE)
