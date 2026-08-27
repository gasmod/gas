# gas/auth

[![Test](https://github.com/gasmod/gas/actions/workflows/test.yml/badge.svg)](https://github.com/gasmod/gas/actions/workflows/test.yml) [![Go Reference](https://pkg.go.dev/badge/github.com/gasmod/gas/auth.svg)](https://pkg.go.dev/github.com/gasmod/gas/auth) ![Go Version](https://img.shields.io/github/go-mod/go-version/gasmod/gas?filename=auth/go.mod) [![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)

Part of the [Gas](../README.md) monorepo · [Documentation](https://gasmod.github.io/gas) · [All modules](../README.md#modules)

Authentication, authorization, and credential management for the [Gas](../README.md) framework:
JWT, server-side sessions, API keys, and single-use tokens.

Implements `gas.Authenticator`, `gas.Authorizer`, `gas.PrincipalRevoker`.

```bash
go get github.com/gasmod/gas/auth
```

```go
gas.WithSingletonService[*jwt.Service](jwt.New()),
gas.WithSingletonService[*session.Service](session.New()),
```

`jwt.New()` needs `gas.ConfigProvider` and `gas.Logger`. `session.New()`, `apikey.New()` and
`token.New()` also need `gas.DatabaseProvider` and `gas.MigrationManager`, since they persist
credentials and own migrations.

| Package | Provides |
|---|---|
| `auth` | `BasePrincipal`, `Chain`, middleware, scheme constants, sentinel errors |
| `auth/jwt` | Stateless JWT, HS256 or RS256 |
| `auth/session` | Server-side sessions with cookie management |
| `auth/apikey` | Hashed API keys with scopes |
| `auth/token` | Single-use tokens: magic links, verification, password reset |
| `auth/authtest` | Mocks for the three auth interfaces |

## Documentation

The full guide, with configuration, testing, and worked examples, is on the docs site.
This README is deliberately a signpost: keeping a second copy here is how the docs drifted before.

- **Guide:** [Authenticate requests](https://gasmod.github.io/gas/guides/auth/)
- **API reference:** [pkg.go.dev/github.com/gasmod/gas/auth](https://pkg.go.dev/github.com/gasmod/gas/auth)
- **Contributing:** [CONTRIBUTING.md](../CONTRIBUTING.md)

## License

[MIT](LICENSE)
