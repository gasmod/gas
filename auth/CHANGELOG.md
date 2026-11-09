# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **session: `Service.RevokeSession`** deletes a `Session` from `Create`
  or `Rotate` without a request. Since v0.6.0 a principal's credential ID
  is the hash of the session ID, so `Revoke` of a principal built from
  `Session.ID`, which worked before, matches no session and returns nil
  with the session still live. A nil session, or one whose ID is not a
  session ID, is `ErrInvalidSession`.

## [0.6.0] - 2026-11-08

v0.6.0 rewrites the module around documented contracts. Each package's
`doc.go` is its behavioral contract and lists its errors verbatim; read it
before upgrading. Most changes are breaking.

### Added

- **`auth.ErrNoCredentials`.** An authenticator returns it when the
  request carries no credentials for it, such as no `Authorization`
  header for jwt or no cookie for session. It does not match
  `ErrUnauthenticated`.
- **`auth.RequireAuthorization(authorizer, action, resource)`**: middleware
  that runs an `Authorizer` on the principal `Middleware` stored, with 401
  for no principal, 403 for an error matching `ErrForbidden`, and 500 for
  any other error. It panics at construction for a nil `Authorizer`.
- **`auth.WithChallenge(value)`** sets the `WWW-Authenticate` header that
  `Middleware` sends with a 401. By default none is sent.
- **Shared sentinels for issuing and revoking**: `auth.ErrEmptySubject`,
  `ErrValueTooLong`, `ErrNilPrincipal` and `ErrSchemeMismatch`, with
  `MaxValueLength` (255) and `CheckLength`. The services return them,
  wrapped after their name.
- **Exported names in every service package**: `ServiceName`, the
  `Default*` constants, and sentinel errors for every failure, including
  `ErrNilConfig`, `ErrNoConfigProvider`, `ErrInvalidConfig`,
  `ErrNotInitialized` and `ErrNilLogger`, which the four services share.
- **`Config() Config` on the jwt, session, apikey and token services.**
  It returns a copy of the configuration the last successful `Init`
  resolved, defaults included, or the zero `Config` before one; a failed
  `Init` and `Close` keep it. The `Provider` that apikey's `WithTx`
  returns is a `*Service` whose `Config` is that of the service it came
  from.
- **jwt**: a `Leeway` setting (`jwt.leeway`, default 0) for the clock
  skew allowed on `exp`, `nbf` and `iat`; a random `jti` on every signed
  token, which becomes the principal's credential ID; RS256 without a
  private key, which only verifies; PKCS #1 public keys next to PKIX; and
  `MethodHS256`, `MethodRS256`, `MinHMACKeyBytes` (32) and
  `MinRSAKeyBits` (2048).
- **session**: `Rotate`, which moves the request's session to a new ID
  without extending its absolute expiry; `MaxLifetime` (default 30 days),
  an absolute expiry that `ExtendOnAccess` never moves past; and
  `RequireSameOrigin`, middleware that rejects cross-site writes by their
  `Sec-Fetch-Site` and `Origin` headers with a 403.
- **apikey**: a background cleanup of expired keys, off by default, set
  with `CleanupInterval` and `CleanupTimeout` (default 30s); and the
  errors `ErrInvalidScope`, `ErrReservedMetadataKey`,
  `ErrInvalidMetadata`, `ErrInvalidTTL` and `ErrInvalidExpiry`.
- **token**: `ErrInvalidTTL` for a negative ttl.

### Changed

- **BREAKING: the store packages are internal.** `apikey/db`, `session/db`
  and `token/db` move to `internal/apikeystore`, `internal/sessionstore`
  and `internal/tokenstore`; each service builds its own store in `Init`.
  `apikey.KeyInfo` is its own struct instead of an alias of `db.KeyInfo`,
  and has no `KeyHash` field.
- **BREAKING: `Chain` tells missing credentials, denials and outages
  apart.** A member that returns `ErrNoCredentials` is skipped. When no
  member succeeds, the chain returns `ErrNoCredentials` if every member
  did, or it is empty; otherwise every infrastructure error joined, in
  member order; otherwise the first denial. It used to return the last
  member's error, and `ErrUnauthenticated` when empty.
- **BREAKING: `Middleware` answers 500 for infrastructure errors.** A 401
  is for `ErrNoCredentials` or an error matching `ErrUnauthenticated`;
  any other error, such as a database that is down, gets a 500 and is
  logged at error level with the request's `gas.Logger`. Every error used
  to get a 401. It also panics for a nil pointer `Authenticator`, not only
  a nil interface. `WithOnError(nil)` keeps the default handler instead
  of setting a nil one.
- **BREAKING: `RequireScheme` answers 401 for a request without a
  principal**, and 403 only for a principal of another scheme. It used to
  answer 403 for both.
- **BREAKING: `ErrCredentialsExpired` and `ErrCredentialRevoked` wrap
  `ErrUnauthenticated`**, and their messages are
  `unauthenticated: credentials expired` and
  `unauthenticated: credential revoked`.
- **BREAKING: every service checks its configuration the same way.**
  `WithConfig` takes a copy, which `Init` checks against the `validate`
  tags and the hand-written rules, as it checks a bound one;
  `WithConfig(nil)` fails `Init` with `ErrNilConfig`, and neither a
  `WithConfig` nor a `ConfigProvider` with `ErrNoConfigProvider`. The
  constructors fail with `ErrNilLogger` for a nil logger, where they used
  to panic or accept it.
- **BREAKING: the services are restartable and refuse use outside
  `Init`.** `Init` may run again, after `Close` or not, and resolves the
  configuration afresh; a failed `Init` leaves the service uninitialized.
  `Close` stops the cleanup and waits for a run in progress. Before
  `Init` succeeds and after `Close`, the methods return
  `ErrNotInitialized`, which `Middleware` answers with a 500.
- **BREAKING: errors start with the service's name and wrap a
  sentinel**, such as `gas/auth/jwt: unauthenticated: invalid token:
  malformed`. Authentication denials wrap `ErrNoCredentials`,
  `ErrUnauthenticated`, `ErrCredentialsExpired` or `ErrCredentialRevoked`.
- **BREAKING: jwt.** `Settings.SigningKey` is a `gas.Secret`.
  `WithSigningMethod` is removed; set `jwt.signingmethod` or use
  `WithConfig`. HS256 refuses key paths and RS256 a signing key, rather
  than ignoring them. RSA keys need at least 2048 bits, and the private
  key must match the public one. Verify requires `exp` and a non-empty
  `sub`, checks `iat` and `nbf`, and denies with `ErrTokenInvalid` and a
  fixed reason, or `ErrTokenExpired`; both now wrap the auth sentinels.
  Sign refuses an empty or over-long subject, a non-positive expiry, a
  custom claim named after a registered claim (`ErrReservedClaim`), which
  used to be overwritten, and a claim that does not encode as JSON
  (`ErrInvalidClaim`). Authenticate returns `ErrNoCredentials` without a
  bearer header and `ErrTokenInvalid` for more than one.
- **BREAKING: session.** Sessions are stored by the SHA-256 of their ID.
  The migration `20261030140100_hash_session_ids` deletes every stored
  session, which signs every user out once, and on MySQL needs 8.0.13 or
  later. The principal's credential ID is the SHA-256 hex of the session
  ID, not the ID. A session is expired from its expiry on, not after it.
  `SetCookie` and `ClearCookie` return an error, and the cookie's
  `Expires` is the absolute expiry. `ExtendOnAccess` extends a
  session only when less than half of `SessionTTL` remains, and a failed
  extend is logged at warn level instead of failing the request. Create
  refuses a nil request (`ErrNilRequest`), an empty subject, and a subject
  over 255 bytes, copies the metadata, and cleans the stored address and
  User-Agent. `Revoke` refuses a nil principal or one of another scheme.
  `Init` checks the cookie name, path, domain and SameSite, and logs the
  insecure-attribute warnings once per `Init`, not on each cookie
  written.
- **BREAKING: apikey.** Authenticate tells a revoked key
  (`ErrCredentialRevoked`) and an expired one (`ErrCredentialsExpired`)
  from an unknown one (`ErrUnauthenticated`), and a failed `LastUsed`
  update is logged at warn level instead of failing the request. Generate
  refuses an empty subject, a subject or name over 255 bytes, a scope that
  is empty, holds a comma or has surrounding white space, metadata with the
  reserved key `scopes`, and a `WithTTL` or `WithExpiresAt` that is not in
  the future; these used to be ignored or stored. `Revoke` and `Delete`
  refuse a nil principal or one of another scheme. `List` breaks ties in
  the creation time by ID, descending.
- **BREAKING: token.** `ErrTokenInvalid` and `ErrTokenExpired` wrap the
  auth sentinels and come back prefixed with `gas/auth/token:`. A token is
  expired from its expiry on, not after it. Issue refuses a negative ttl,
  an empty subject, and a subject or purpose over 255 bytes. `Revoke` of a
  token that is not stored returns nil instead of `ErrTokenInvalid`.
- **BREAKING: `authtest`.** The mocks' `Calls` field becomes a `Calls()`
  method that returns a copy, next to `CallCount` and `Reset`. With its
  function unset, `MockAuthenticator` returns `ErrNoCredentials`, and
  `MockAuthorizer` returns `ErrForbidden` instead of allowing.
- **The cleanups log differently.** A run that fails is logged at warn
  level as `background task failed`, with `task` and `error`; a run that
  succeeds logs `expired sessions deleted`, `expired tokens deleted` or
  `expired api keys deleted` at debug level with `count`, zero included.
  They used to log failures at error level and deletions at info level.
- **MySQL no longer needs `multiStatements=true`**, since gas/migrate
  v0.6.0 sends a MySQL migration's statements one at a time.
  `parseTime=true` stays required, and gas/database turns it on itself.
- **Redundant indexes are dropped.** The migrations
  `20261030150000_drop_api_keys_key_hash_index` and
  `20261030120000_drop_auth_tokens_token_hash_index` drop the second
  index on the key and token hashes; lookups use their unique indexes.

### Removed

- **BREAKING:** `jwt.WithSigningMethod`, the public `apikey/db`,
  `session/db` and `token/db` packages, `apikey.KeyInfo.KeyHash`, and the
  `Calls` field of the `authtest` mocks.

### Fixed

- The apikey MySQL migration `20250323002_create_api_keys` no longer gives
  the TEXT column `scopes` a default, which MySQL refuses (KI-044).
- apikey's `Generate` no longer panics on a `KeyLength` under 16 given
  with `WithConfig`, since `Init` now checks that config's tags (KI-040).
- On MySQL, every stored time and every cutoff is truncated to the second
  before it is written, so a rounded time can never make an expiry pass
  late.
- A second `Init` of session or token no longer starts a second cleanup
  goroutine, and `Close` waits for a running cleanup.
- The session and token cleanups delete the rows whose expiry equals the
  cutoff, and the session cleanup also the sessions past `MaxLifetime`.

### Security

- **Requires Go 1.27.3.** The `go` directive moves from 1.27 to 1.27.3,
  the first 1.27 release with the fix for GO-2026-6809 (CVE-2026-111943):
  `crypto/x509` now rejects intermediate certificates known to be
  compromised. A build with an older toolchain switches to 1.27.3 under
  `GOTOOLCHAIN=auto` and fails under `GOTOOLCHAIN=local`.
- **The JWT signing key is a `gas.Secret`**, which fmt, encoding/json,
  YAML encoders and log/slog write as `[redacted]`. It printed in full,
  and it can forge any token.
- **A database dump no longer holds working session cookies**, as sessions
  are stored by the hash of their ID.
- **No error includes a token, a key, a session ID, a key path or another
  configuration value**; errors name the setting or claim instead.
- **`MockAuthorizer` fails closed**, so a test that forgets to set
  `AuthorizeFn` no longer passes every authorization.

## [0.5.6] - 2026-10-19

The tags `auth/v0.4.1` to `auth/v0.5.6` shipped their changes under
`[Unreleased]`. This section holds them, and covers every change since
0.1.0, which was released from the separate
`github.com/gasmod/gas-auth` repository.

### Added

- **`auth.Authenticator`, `auth.Authorizer`, and `auth.PrincipalRevoker`.**
  The interfaces moved here from core, with the same methods. `Chain`,
  `Middleware`, the `jwt`, `session`, and `apikey` services, and the
  `authtest` mocks now implement or accept the `auth` interfaces.

### Changed

- **BREAKING: import path.** The module moved from
  `github.com/gasmod/gas-auth` to `github.com/gasmod/gas/auth`, in
  auth/v0.4.1.
- **BREAKING: Go 1.27 is required.** The `go` directive moves from 1.26.1
  to 1.27, in auth/v0.5.0, as core now uses generic methods.
- **BREAKING: the `db` stores' `Init` takes the owning `gas.Service`.**
  `apikey/db`, `session/db` and `token/db` `Store.Init(service gas.Service)`
  replaces `Init(serviceName string)`, following core's move to service
  owners, in auth/v0.5.0. Pass the service itself instead of its name.
- **BREAKING: `Config.Validate` is removed from `jwt`, `session`, `apikey` and
  `token`; the rules are validate tags.** `ConfigProvider.Bind` already
  validates the struct it binds, so `Init` no longer runs a tag check of its
  own; a config passed with `WithConfig` is not checked against the tags, as
  `gas.Config` is not when an App has no `ConfigProvider`. Tags: jwt
  `SigningMethod` `oneof=HS256 RS256` and `Expiry` `gt=0s`; session
  `CookieName` `required` and `SessionTTL` `gt=0s`; apikey `KeyLength`
  `min=16`; token `DefaultTTL` `gt=0s` and `TokenLength` `min=16`. The rules a
  tag cannot express stay hand-written and run in `Init` for every config: the
  jwt key material per signing method, and a positive `CleanupTimeout` when
  `CleanupInterval` is set (session, token). apikey's separate check that the
  key yields an 8-character prefix is dropped, since `min=16` already
  guarantees it. Errors for the tag rules are now the validator's (gas#116).
- **BREAKING: the constructors return an error.** `jwt.New`, `session.New`,
  `apikey.New` and `token.New` now return `func(<dependencies>) (*Service,
  error)`, and the stores' `apikey/db.New`, `session/db.New` and
  `token/db.New` return `func(...) (*Store, error)`, the `CONVENTIONS.md`
  shape. They cannot fail yet, so they return nil; the change is the shape.
  Registering them with `gas.WithSingletonService` is unchanged, since the
  container accepts a trailing error. Code that calls the returned function
  directly must take the second result (gas#106).
- **BREAKING:** the `jwt`, `session`, `apikey`, and `token` services implement
  the context-aware `gas.Service` (`Init(ctx context.Context) error`,
  `Close(ctx context.Context) error`). None of them does context-bound work in
  `Init` or `Close`, so the context is accepted and unused; the `session` and
  `token` cleanup goroutines still run until `Close` and do not derive from
  the `Init` context.
- **BREAKING: the auth API names `auth.Authenticator`.** It is the element
  type of `Chain` and the parameter type of `Middleware`, so code converting a
  `[]gas.Authenticator` to `auth.Chain` must use `[]auth.Authenticator`
  instead. Values typed `gas.Authenticator` keep working in `Chain` literals
  and as the `Middleware` argument against core v0.5.x, since the method sets
  are identical.
- `Middleware(nil)` panics with `gas: auth.Middleware: nil Authenticator;
  pass a configured Authenticator ...` instead of `auth: nil
  authenticator`, following the `gas:` prefix rule for wiring panics. Code
  that matched the old message must match the new one.

### Security

- **`Middleware` and `Chain` no longer treat a nil principal as
  authenticated.** An `Authenticator` that returned `(nil, nil)`, as any
  app-written one could, let `Middleware` call the next handler for a
  caller with no credentials, and ended a `Chain` as a success. Both now
  treat a nil principal, including a typed nil such as
  `(*BasePrincipal)(nil)`, as `ErrUnauthenticated`: `Middleware` answers
  through its error handler (401 by default), and `Chain` moves on to the
  next member. The `Authenticator` doc now states the contract.
- **`authtest.MockAuthenticator` returns `ErrUnauthenticated` when
  `AuthenticateFn` is unset.** It used to return `(nil, nil)`. Tests that
  relied on that default to pass authentication must set `AuthenticateFn`.

## 0.1.0 - 2026-07-03

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

[Unreleased]: https://github.com/gasmod/gas/compare/auth/v0.6.0...HEAD
[0.6.0]: https://github.com/gasmod/gas/releases/tag/auth/v0.6.0
[0.5.6]: https://github.com/gasmod/gas/releases/tag/auth/v0.5.6
