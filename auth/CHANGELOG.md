# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.0] - 2026-07-03

First open source release. Versions prior to 0.1.0 were developed in a private
repository; this entry summarizes the framework as published.

### Added

- **Root `auth` package** — `BasePrincipal`, the `Chain` composite
  authenticator, `Middleware` with a configurable `WithOnError` handler,
  `RequireScheme`, scheme constants (`SchemeJWT`, `SchemeSession`,
  `SchemeAPIKey`), and sentinel errors (`ErrUnauthenticated`, `ErrForbidden`,
  `ErrCredentialsExpired`, `ErrCredentialRevoked`).
- **JWT service** (`auth/jwt`) — stateless JWT authentication with HS256
  (HMAC) signing and RS256 (RSA) verify-only support, and configurable
  issuer, audience, and expiry claims.
- **Session service** (`auth/session`) — server-side, database-backed
  sessions with cookie management, TTL extension on access, and background
  cleanup.
- **API key service** (`auth/apikey`) — SHA-256-hashed API keys with scopes,
  metadata, and expiration; soft-delete revocation alongside a hard
  `Delete`; `WithIncludeRevoked` listing; and `WithTx` for caller-driven
  transaction scoping.
- **Token service** (`auth/token`) — single-use, time-limited tokens for
  magic links, email verification, and password resets, with atomic
  fetch-and-delete verification.
- **Provider interfaces** for each service (`jwt.Provider`,
  `session.Provider`, `apikey.Provider`, `token.Provider`) implementing
  `gas.Authenticator`, `gas.PrincipalRevoker`, and `gas.Service`.
- **Multi-dialect database support** — PostgreSQL, MySQL, and SQLite for
  session, API key, and token storage, selected automatically from the
  `gas.DatabaseProvider` driver.
- **`authtest` package** — mock implementations (`MockAuthenticator`,
  `MockAuthorizer`, `MockRevoker`) with call-count assertions for testing.
- **DI-driven configuration** — automatic config binding from
  `gas.ConfigProvider` when `WithConfig` is not supplied.

### Fixed

- Made single-use token verification's fetch-and-delete atomic, eliminating
  a race that allowed a token to be consumed more than once.
- Enforced a minimum key length for HS256 signing keys and for generated API
  keys and tokens.
- Rejected non-positive `CleanupTimeout` in session and token config
  validation when `CleanupInterval` is enabled, preventing an
  already-expired context from silently failing the cleanup goroutine on
  every run.

[Unreleased]: https://github.com/gasmod/gas/auth/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/gasmod/gas/auth/releases/tag/v0.1.0
