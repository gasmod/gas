# gas/email

[![Test](https://github.com/gasmod/gas/actions/workflows/test.yml/badge.svg)](https://github.com/gasmod/gas/actions/workflows/test.yml) [![Go Reference](https://pkg.go.dev/badge/github.com/gasmod/gas/email.svg)](https://pkg.go.dev/github.com/gasmod/gas/email) ![Go Version](https://img.shields.io/github/go-mod/go-version/gasmod/gas?filename=email/go.mod) [![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)

Part of the [Gas](../README.md) monorepo · [Documentation](https://gasmod.github.io/gas) · [All modules](../README.md#modules)

Transactional email for the [Gas](../README.md) framework, with two interchangeable senders that
keep one contract, Amazon SES's rules: Amazon SES through its API v2 (or an emulator that speaks
it), and an in-memory outbox for tests and local development. Messages are checked and their
addresses normalized before anything is sent, and templates render through `gas.TemplateProvider`.

Implements `gas.EmailProvider`.

```bash
go get github.com/gasmod/gas/email
```

```go
gas.WithSingletonService[gas.EmailProvider](ses.New()),
```

Both senders need `gas.TemplateProvider`, `gas.ConfigProvider` and `gas.Logger`, even if you only
ever call `Send`, and read the `email` section; the SES sender takes its region and credentials
from `email.region` and the static keys, or else from the AWS SDK's default chain.

| Package | Provides |
|---|---|
| `email` | The shared contract, address and header rules, limits and sentinel errors |
| `email/ses` | Amazon SES (API v2), and emulators via a custom endpoint |
| `email/memory` | In-process outbox with SES's rules, for tests and local development |
| `email/emailtest` | Recording mock of `gas.EmailProvider` |

## Documentation

The full guide, with configuration, testing, and worked examples, is on the docs site.
This README is deliberately a signpost: keeping a second copy here is how the docs drifted before.

- **Guide:** [Email](https://gasmod.github.io/gas/modules/email/)
- **API reference:** [pkg.go.dev/github.com/gasmod/gas/email](https://pkg.go.dev/github.com/gasmod/gas/email)
- **Contributing:** [CONTRIBUTING.md](../CONTRIBUTING.md)

## License

[MIT](LICENSE)
