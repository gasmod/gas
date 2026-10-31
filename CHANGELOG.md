# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

v0.6.0 rewrites every module and the core around documented contracts.
Each package's `doc.go` is its behavioral contract and lists its errors
verbatim; read it before upgrading. Breaking changes are everywhere; the
main ones per module follow.

### Changed

- **core (`gas`)**: core defines its own `ConfigProvider` and
  `Environment` and imports no gas module. `App` and `Worker` start once
  (`ErrAlreadyStarted`, `ErrShutDown`); `Stop` has one deadline that also
  bounds service `Close`; the kill switch tracks services by instance and
  refuses inactive ones through DI with a 503. `Context` loses its error
  constructor methods (use `gas.BadRequest` and the other package
  functions). `MigrationManager` takes ctx (`RunPendingContext`,
  `DownContext`). `Env` binds from `gasenv` and the server settings are
  validated. `InitServices`, `Router.Mux` and `ErrNoConstructor` are
  removed. New `Timeout` and `BodyLimit` middleware, and `gas.Secret`
  for configuration secrets.
- **config**: rewritten; `Set`, `SetDefault` and `SetDefaults` return
  errors, keys are validated, and printing a `*Config` shows counts only.
- **auth**: rewritten; the JWT signing key is a `gas.Secret`.
- **database**: rewritten; `Dialect` replaces `Driver`, MySQL is
  supported, `SafeError`, a default SQLite busy timeout, and the DSN is a
  `gas.Secret`.
- **migrate**: rewritten; all-digit versions order numerically, new
  directives, `ClearDirty` and `Applied`.
- **log**: the root package is stdlib only; zerolog moves to
  `log/zerolog`; `NewShippingHandler`; endpoints in errors show the host
  only.
- **template**: one store contract; `dir` is a `gas.Service`;
  `WithExtensions`, `WithMaxSize`; `db` drops sqlc and adds a migration.
- **cache**: one contract for `memory` (LRU) and `valkey` (`OpTimeout`,
  `KeyPrefix`, `Username`, TLS); `cache.addr` is required; a negative TTL
  is an error.
- **queue**: a new `memory` store; `sqs` takes queue URLs or names,
  validates messages before sending, has no region default, bounds every
  call with `OpTimeout`, and adds `NackAfter` and `Extend`.
- **email**: `ses` moves to SESv2 and sends custom headers; a new
  `memory` outbox; messages are validated before sending;
  `SendFromTemplate` no longer mutates the message.
- **ui**: `Render` and `RenderFragment` take ctx; `ui.reload` replaces
  environment-driven recompiling; `Reload`; a configurable fragment
  block; static files through `os.Root`, off unless configured; the
  public `Engine` is removed.
- **storage**: a new `memory` backend and a conformance suite; `dir`,
  `s3` and the presigner are services; one `ErrObjectNotFound`; metadata
  follows S3's rules on every backend; presigned uploads need a content
  length; `dir` syncs writes to disk.

### Fixed

- Known issues KI-010, KI-032 to KI-036, KI-038 to KI-045, KI-048 to
  KI-054, KI-056 to KI-058, KI-062, KI-063, KI-071, KI-080 and KI-081.

### Security

- Configuration secrets (JWT signing key, database DSN, AWS secret keys
  and session tokens, the Valkey password) no longer print through fmt,
  JSON, YAML or log/slog, and refuse gob encoding.
- Errors and logs no longer carry keys, bucket or queue names, URLs,
  account IDs, addresses or credentials; the AWS SDK's default logger is
  silenced.
- Request handling: no error body after a committed response, no cache
  headers on error responses, bounded shutdown, and no use of closed
  services.
