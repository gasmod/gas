# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.9.0] - 2026-11-11

### Changed

- The internal modules are required at v0.9.0.

## [0.8.0] - 2026-11-11

### Changed

- **Go 1.27.0 or later is required**, down from 1.27.3. Every module
  declares `go 1.27.0`, the first Go with generic methods, which the
  core's API uses. Each also declares `toolchain go1.27.4`, which builds
  this repository and its CI only, since Go ignores a dependency's
  `toolchain` line. Build apps with Go 1.27.4 or later: earlier 1.27
  releases can crash an HTTP/1 server
  ([GO-2026-6859](https://pkg.go.dev/vuln/GO-2026-6859)).
- `github.com/gasmod/omap` is required at v0.2.1, which requires Go 1.24
  rather than 1.27.3.
- `github.com/go-chi/chi/v5` is required at
  `v5.5.1-0.20261018135022-9f8adb1aabaa`, an untagged commit after
  v5.5.0.
- Dependencies updated, and the internal modules required at v0.8.0.

## [0.7.0] - 2026-11-11

### Changed

- The internal modules are required at v0.7.0.

### Fixed

- A field error's `pointer` follows the JSON body for embedded structs.
  An untagged embedded struct, which encoding/json flattens, added its Go
  type name as a segment, such as `#/Profile/name` for `name`; it now
  adds none, `#/name`. An error on such a field at the root, such as a
  `required` embedded pointer, points at the whole body, `#`.
- The `pointer` of a field in a generic request type held the type's
  name and its type argument's package path, such as
  `#/com~1example~1app/Item%5D/title`; it is now `#/title`.

## [0.6.3] - 2026-11-10

### Changed

- The internal modules are required at v0.6.3.

## [0.6.2] - 2026-11-10

### Changed

- The internal modules are required at v0.6.2.

## [0.6.1] - 2026-11-10

### Changed

- Dependencies updated, and the internal modules required at v0.6.1.

### Removed

- **BREAKING: `RouteGroup.NotFound`.** The NotFound handler is the
  router's alone: `Router.NotFound` sets it for every path, as
  `Router.SetErrorHandler` sets the one ErrorHandler. A handler set on a
  `Group` was always replaced by the router's.

### Fixed

- `CacheControl` keeps a Cache-Control the handler set, as it did before
  v0.6.0. Since v0.6.0 it set its header when the status was written,
  after the handler, so a handler's `private` or `no-store` went out as
  the middleware's directives, such as `public`.
- A handler's `ResponseWriter` is an `http.Pusher` again, as it was
  before v0.6.0. Since v0.6.0 the Router's writer, and `CacheControl`'s,
  dropped it, so `w.(http.Pusher)` failed even on an HTTP/2 connection
  and HTTP/2 server push was lost, with `RequestLogger` too. `Push` now
  passes on to the server's writer, and returns `http.ErrNotSupported`
  where it cannot push, such as on HTTP/1, as net/http's HTTP/2 writer
  does when the client disabled push.

## [0.6.0] - 2026-11-08

v0.6.0 rewrites the core and every module around documented contracts.
Each package's `doc.go` is its behavioral contract and lists its errors
verbatim; read it before upgrading. Breaking changes are marked
**BREAKING**. This file covers the core package, `github.com/gasmod/gas`,
in full, and each module in brief; each module's own `CHANGELOG.md` has
the details.

### Added

- **`WithBaseConfig(Config)`**, an `AppOption` that sets the `Config`
  Start binds the `ConfigProvider`'s values over and validates, in place
  of `DefaultConfig()`. Start validates it even when no `ConfigProvider`
  is registered.
- **`WithAllowedEnvironments(envs...)`** sets the environments Start
  accepts in `Config.Env`, for a config module that allows others.
  `DefaultEnvironment` (`EnvDevelopment`) is always allowed. The default
  is development, testing, staging and production.
- **Service aliases.** `RegisterServiceAlias[Target, Alias]()` on the
  `ServiceContainer` and the `Worker`, and the `WithServiceAlias[Target,
  Alias]()` option, make one registration resolvable under an interface it
  implements as well. Resolving the alias resolves the target: the same
  instance, the target's lifetime, one `Init` and one `Close`. The alias and
  its target can be registered in either order; a target still missing at
  startup fails `BuildAll` with an error wrapping `ErrNotRegistered`, and a
  singleton that depends on an alias of a scoped or transient service fails
  with `ErrCaptiveDependency`. Registration panics with an error wrapping
  `ErrInvalidAlias` if the alias is not an interface, the target does not
  implement it, the two are the same type, the alias is already registered
  as a service or instance, the target is itself an alias, or the alias is
  the target of another alias.
- **`ServerSettings.ReadHeaderTimeout`**, which `App.Server` sets on the
  `*http.Server`. It bounds how long a client may take to send its request
  headers, so a service that zeroes `ReadTimeout` to stream request bodies
  keeps a header deadline. The default is 5s, and it must be positive.
  Closes #144.
- **`ServerSettings.MaxFormBodyBytes`**, 1 MiB by default, caps the body
  `Context.BindForm` reads; a larger one is a 413 with code
  `CodePayloadTooLarge`. `WithMaxFormBodyBytes` sets it on a `Context`
  made with `NewContext`.
- **`Timeout(d)` middleware** gives each request a deadline and answers
  503 with code `CodeTimeout` when it passes. It buffers the response, up
  to `DefaultTimeoutMaxBuffer` (8 MiB) or `WithTimeoutMaxBuffer`, and
  answers 503 past that; a write past the cap returns
  `ErrResponseTooLarge`.
- **`BodyLimit(n)` middleware** caps request bodies, answering 413 in the
  unified error shape.
- **`gas.Secret`**, a string for configuration secrets that prints,
  marshals and logs as `[redacted]` and refuses gob encoding.
- **`Context.Blob(status, contentType, b)`** writes bytes the handler has
  already encoded, under the given content type. An empty `contentType`
  leaves `Content-Type` as it is, and when none is set net/http detects one
  from the body. A write error is returned wrapped, as `HTML` and `Text` do.
- **`WithShutdownTimeout`**, a `WorkerOption` that bounds the shutdown
  `Worker.Run` starts after a signal; the default is
  `DefaultShutdownTimeout`, 30s. An App uses the Config's
  `ShutdownTimeout` instead.
- **Sentinel errors** for the lifecycle (`ErrAlreadyStarted`,
  `ErrNotStarted`, `ErrShutDown`), options (`ErrInvalidOption`),
  configuration (`ErrInvalidConfig`), the kill switch
  (`ErrNoBuiltInstance`, `ErrServiceNotActive`, `ErrServiceAlreadyActive`,
  `ErrDuplicateServiceName`), the container (`ErrScopeClosed`,
  `ErrInvalidConstructor`, `ErrNilInstance`, `ErrInvalidAlias`), routing
  (`ErrInvalidHandler`, `ErrUnknownMiddleware`, `ErrNotFoundHandlerSet`),
  recovered panics (`ErrPanic`) and responses (`ErrResponseCommitted`,
  `ErrResponseTooLarge`), to be tested with `errors.Is`. The package
  documentation lists sample error texts verbatim.
- **`CodeMethodNotAllowed`**: a path routed for other methods only answers
  405 in the unified error shape, with an `Allow` header.
- **Exported defaults**: `DefaultHost`, `DefaultPort`, the
  `Default...Timeout` and `DefaultMax...BodyBytes` constants that
  `DefaultConfig` sets, `DefaultEnvironment`, `DefaultServiceInitTimeout`
  and `ShutdownGracePeriod`.

### Changed

- **BREAKING: `App.Config` returns a `Config` by value.** It returned the
  App's `*Config`, so a caller could change the App's settings through it.
  It now returns a copy: the base config before Start, the bound one
  after. It is safe to call concurrently with Start. Set the base config
  with `WithBaseConfig`.
- **BREAKING: the App no longer registers `*gas.Config` in the
  container.** A service or handler that takes a `*gas.Config` now fails
  Start with an error wrapping `ErrNotRegistered`; a service binds the
  settings it needs through its own `ConfigProvider` dependency. Start
  binds and validates the Config once every service is built and
  initialized, keeps it only once it is valid, and closes the services
  when it is not. The errors read `gas: invalid config: ...` and wrap
  `ErrInvalidConfig`. That moved the binding, and `AppConfigUpdated`,
  earlier: Start binds the Config and emits `AppConfigUpdated` before it
  emits `SystemAllServicesInitialized`, runs the pending migrations and
  runs the ready hooks, where v0.5.6 did both after the ready hooks. A
  ready hook now sees the bound Config through `App.Config`, and an
  invalid Config fails Start before any migration runs. An `AppConfigUpdated` subscriber that needs the migrations
  applied, such as one that reads a table, does that work in a ready hook
  (`ReadyFunc`) instead, which still runs after them.
- **BREAKING: `Config.Env` binds from the key `gasenv`** and replaces the
  embedded `WithGasEnv`, which is removed. The config module's
  `EnvExtension` stores the environment it resolves from `GAS_ENV` there.
- **BREAKING: Start validates the bound Config.** `Env` must be an
  allowed environment; `Host` an IP address or an RFC 1123 hostname;
  `Port` 0 to 65535; `ReadTimeout`, `WriteTimeout` and `IdleTimeout` not
  negative, with `IdleTimeout` positive when `ReadTimeout` is 0;
  `ReadHeaderTimeout`, `ShutdownTimeout` and both body limits positive.
  An error names the key and the rule, never the value.
- **BREAKING: `ConfigProvider`**: `Set` and `SetDefault` return an error,
  and `Bind` takes no options; `BindOption` and `BindOptions` are removed.
- **BREAKING: `MigrationManager`** takes a context:
  `RunPendingContext(ctx)` and `DownContext(ctx, n)` replace `RunPending`
  and `Down`. Start runs the pending migrations with its ctx.
- **BREAKING: an App or a Worker runs once.** A second Start returns
  `ErrAlreadyStarted`, and a Start once Shutdown has begun `ErrShutDown`.
  A Start that fails closes, newest first, every service it initialized,
  and every later Start returns its error again. Shutdown runs once; a
  second call waits for the first and returns nil.
- **BREAKING: `App.Stop` has one deadline.** It returns within the
  Config's `ShutdownTimeout` plus up to three `ShutdownGracePeriod`s,
  which also bounds every service's `Close`. At the deadline it cancels
  the context of the requests still running, then closes their
  connections. A server shutdown error, which `Stop` used to log only, is
  returned as `gas: server shutdown: ...`, joined with the other shutdown
  errors; a clean `Stop` returns nil.
- **BREAKING: `Worker.Run` and `App.Run` take a `context.Context`.** They
  stop on SIGINT, SIGTERM or the end of ctx, which is a normal stop: Run
  returns the shutdown's error. Start runs under ctx, so each Init gets
  its values, and the shutdown keeps its values but not its cancellation.
  `Run(context.Background())` behaves as `Run()` did.
- **BREAKING: error responses are RFC 9457 problem details.** A JSON
  error is now `application/problem+json`:
  `{"type","title","status","detail","code","errors","details"}` in place
  of `{"error":{"status","code","message","fields","details"}}`. `title`
  is the HTTP status phrase and `detail` the message. Field errors are
  `errors` items of `detail`, an RFC 6901 `pointer` such as
  `#/items/0/name`, and `rule`. `type` is the error's code under the base
  that the new `WithProblemTypeBase` option or `Router.SetProblemTypeBase`
  sets, and is left out (`about:blank`) without one. `Problem` replaces
  `ErrorResponse` for decoding; `FieldError` has `Pointer` in place of
  `Field`; `WantsJSON` also accepts `application/problem+json`. `*Error`
  is no longer a wire type and has no `json` tags, so `json.Marshal` of
  one, or `ctx.JSON(status, err)`, gives its Go field names, such as
  `{"Details":null,"Code":"validation_failed","Message":"bad","Fields":[...],"Status":422}`,
  where v0.5.6 gave `{"status":422,"code":"validation_failed","message":"bad","fields":[...]}`.
  Write an error with `WriteError(w, r, err)`, or return it from a
  DI-aware handler, to send the problem body.
- **BREAKING: `Router.Use`, `Group` and `Route` take an owner.** `Use`,
  `UseMiddlewareFunc` and `UseMiddlewareByName` take the owning service
  first, as `Handle` and `Register` do, nil for the router. `Group` and
  `Route` take the owner once and pass a new `*RouteGroup`, whose `Use`,
  `Handle`, `Get` and the other verbs, `NotFound`, `Group` and `Route`
  take none: the whole group belongs to that service. When the kill
  switch removes the owner of a `Use` call, its middleware answers 503 on
  every route until the owner calls `Use` again, which takes the old
  call's place.
- **BREAKING: the kill switch tracks services by instance**, so two
  services with one name are both closed. `CloseService` and
  `RestartService` return `ErrNoBuiltInstance`,
  `ErrDuplicateServiceName`, `ErrServiceNotActive`,
  `ErrServiceAlreadyActive`, `ErrNotStarted` or `ErrShutDown`. A service
  that is not active is not handed out: resolving it fails with an error
  wrapping `ErrServiceNotActive`, and a handler that depends on it answers
  503.
- **BREAKING: a scoped service's `Init` runs under the init deadline.** It
  got the scope's context with no deadline added, and `InitTimeout` was
  ignored. It now runs like a singleton's: under the container's init
  timeout (15s by default, `WithServiceInitTimeout` or
  `WithServiceContainerServiceTimeout`) or the service's own `InitTimeout`,
  added to the scope's context. A scoped `Init` that takes longer fails the
  resolution with `init <T>: timed out after ...`, wrapping
  `context.DeadlineExceeded`, and one that returns nil after its deadline is
  closed again. Implement `InitTimeout` (0 for no deadline) to change it.
- **BREAKING: options that get a value they cannot take fail Start**
  with an error wrapping `ErrInvalidOption`, before anything is
  initialized. A negative `WithServiceInitTimeout` and a
  `WithTrustedOrigin` that is not an absolute URL used to panic; a
  non-positive `WithShutdownTimeout` and an empty
  `WithAllowedEnvironments` fail the same way. A negative
  `WithServiceContainerServiceTimeout`, which used to panic, fails
  `BuildAll`.
- **BREAKING: `Context` loses its error constructor methods**
  (`BadRequest`, `BadRequestErr` and the others); use the package
  functions, such as `gas.BadRequest(msg)` and
  `gas.BadRequest(msg).WithCause(err)`.
- **BREAKING: `RequestLogger`'s line changed.** A log query or alert
  built on v0.5.6's lines needs updating:
  - Level: a 4xx is logged at warn, where it was error. Info below 400
    and error from 500 are as before. To keep matching client errors,
    match `status` from 400 rather than the error level.
  - `duration` is a `time.Duration` through `LogEvent.Duration`, where it
    was the string `time.Duration.String()` gave, such as `"1.5ms"`. The
    Logger encodes it: slog's JSON handler as integer nanoseconds, such
    as `1500000`, and the log module's zerolog backend in
    `zerolog.DurationFieldUnit`, milliseconds unless changed. The other
    fields keep their names and types: `method`, `path` and `remote`
    strings, `status` and `bytes` ints.
  - One line per request. With `RequestLogger`, the default ErrorHandler
    no longer logs a line of its own (`request error` at warn for a 4xx,
    `unhandled request error` at error for a 5xx) next to the `request`
    line; the `request` line carries the error as `error`. Without
    `RequestLogger` it still logs its own line, now `request error` for a
    5xx too, at error.
- **`App.Handler` no longer builds the server.** It used to return
  `Server().Handler`, so calling it before `Start`, as tests and embedders
  do, built the `*http.Server` from the config as it stood then. It now
  builds only the handler, once; a handler assigned to `Server().Handler`
  is no longer what it returns.
- **config**: rewritten. `Set`, `SetDefault` and `SetDefaults` return
  errors, keys are validated, and printing a `*Config` shows counts only.
  **BREAKING**: the environment provider requires a prefix. An
  `EnvProvider` with none fails to load with `ErrEnvPrefixRequired`
  unless given `WithoutEnvPrefix`, and so does `config.New` with no
  provider, whose default `EnvProvider` has no prefix.
- **auth**: rewritten; the JWT signing key is a `gas.Secret`. The jwt,
  token, apikey and session services gain `Config()`, a copy of the
  Config their last successful Init bound.
- **database**: rewritten; `Dialect` replaces `Driver`, MySQL is
  supported, `SafeError`, a default SQLite busy timeout, and the DSN is a
  `gas.Secret`. `Service.Config()` returns the bound Config.
- **migrate**: rewritten; all-digit versions order numerically, new
  directives, `ClearDirty` and `Applied`. The tracking table records a
  SHA-256 checksum of each migration's up SQL (`AppliedMigration.Checksum`),
  and `RunPending` fails with `ErrChecksumMismatch` when an applied
  migration was edited; `Init` adds the `checksum` column to an existing
  table.
- **log**: the root package is stdlib only; zerolog moves to
  `log/zerolog`; `NewShippingHandler`; endpoints in errors show the host
  only.
- **template**: one store contract; `dir` is a `gas.Service`;
  `WithExtensions`, `WithMaxSize`; `db` drops sqlc and adds a migration.
- **cache**: one contract for `memory` (LRU) and `valkey` (`OpTimeout`,
  `KeyPrefix`, `Username`, TLS); `cache.addr` is required; a negative TTL
  is an error. Both services gain `Config()`.
- **queue**: a new `memory` store; `sqs` takes queue URLs or names,
  validates messages before sending, has no region default, bounds every
  call with `OpTimeout`, and adds `NackAfter` and `Extend`. Both services
  gain `Config()`.
- **email**: `ses` moves to SESv2 and sends custom headers; a new
  `memory` outbox; messages are validated before sending;
  `SendFromTemplate` no longer mutates the message. Both senders gain
  `Config()`.
- **ui**: `Render` and `RenderFragment` take ctx; `ui.reload` replaces
  environment-driven recompiling; `Reload`; a configurable fragment
  block; static files through `os.Root`, off unless configured; the
  public `Engine` is removed. `Service.Config()` returns the bound
  Config.
- **storage**: a new `memory` backend and a conformance suite; `dir`,
  `s3` and the presigner are services; one `ErrObjectNotFound`; metadata
  follows S3's rules on every backend; presigned uploads need a content
  length; `dir` syncs writes to disk. **BREAKING**: `Provider` gains
  `CopyObject`, which copies an object within a bucket or across buckets,
  and the s3 backend's `API` interface gains `CopyObject`. Each backend
  gains `Config()`. Bucket names are no longer refused for S3's reserved
  starts and ends, such as `xn--` and `-s3alias`; S3 refuses them itself.
- **The agent skills are one `gas` skill, generated from the Go doc.**
  `skills/gas` replaces the twelve hand-written skills (`gas`,
  `gas-auth`, `gas-cache` and the others): a `SKILL.md` with the
  installation guide and a package index, and a reference per package
  holding its doc overview, with `go doc` for the API. `make skill`
  regenerates it, and the docs workflow fails when it is stale.

### Removed

- **BREAKING: `Worker.InitServices`**, **`Router.Mux`** and
  **`ErrNoConstructor`**.
- **BREAKING: `WithGasEnv`**; `Config.Env` binds from `gasenv`.
- **BREAKING: `UIProvider`**; the ui module defines its own `Provider`.
- **BREAKING: `NewNopLogger`, `NopLogEvent` and `NopLoggerContext`.** The
  zero `NopLogger` is ready to use; register it as an instance, such as
  `gas.WithServiceInstance[gas.Logger](&gas.NopLogger{})`.
- **BREAKING: the exported option structs** `CacheControlOptions`,
  `RequestLoggerOptions`, `SecurityHeadersOptions` and
  `ServiceContainerOptions`; their option functions are unchanged.
- **BREAKING: `Worker.ActiveServicesMap`.** It was a test helper compiled
  into the library. Use `ActiveServices` for the names of the active
  services.
- **BREAKING: `Context.RSS`.** Encode the feed and write it with `Blob`:
  `body, err := xml.Marshal(feed)`, then `ctx.Blob(status,
  "application/rss+xml; charset=utf-8", append([]byte(xml.Header), body...))`.
  A type that implements `gas.Context` itself must drop `RSS` and add
  `Blob`.

### Fixed

- **A server built before `Start` gets the bound config.** `App.Server`
  copied the config into the `*http.Server` once, so a service whose
  `Init` called `Server()` pinned the defaults for the life of the
  process. `Start` now applies the bound `Addr` and timeouts to every
  field the caller left as `Server` set it, and keeps the fields the
  caller changed. `AppConfigUpdated` subscribers run after that and have
  the last word. A server `Serve` has begun with is left alone.
- **Startup is deterministic.** Singletons are built in an order fixed by
  their dependencies and the order they were registered in, and
  captive-dependency and shape errors are reported in registration order,
  so the same broken setup reports the same error every time.
- **A restarted service no longer applies its `Router.Use` middleware
  twice.** A service that called `Use` in `Init` added the middleware
  again on every restart, and the kill switch could not remove it.
- **template**: on Linux, `dir`'s `List` of a directory that was removed
  or moved reports it as such, not as the walk's error.
- **storage**: on Linux, `dir`'s `Init` no longer fails when another
  process deletes a bucket while Init sweeps it.
- Known issues KI-010, KI-032 to KI-036, KI-038 to KI-058, KI-062,
  KI-063, KI-071, KI-080, KI-081 and KI-083.

### Security

- Configuration secrets (JWT signing key, database DSN, AWS secret keys
  and session tokens, the Valkey password) no longer print through fmt,
  JSON, YAML or log/slog, and refuse gob encoding.
- Errors and logs no longer carry keys, bucket or queue names, URLs,
  account IDs, addresses or credentials; the AWS SDK's default logger is
  silenced.
- Request handling: no error body after a committed response
  (`ErrResponseCommitted`), `Cache-Control: no-store` on every error
  response, bounded shutdown, and no use of closed services.
- **Requires Go 1.27.3.** The `go` directive moves from 1.27 to 1.27.3,
  the first 1.27 release with the fix for GO-2026-6809 (CVE-2026-111943):
  `crypto/x509` now rejects intermediate certificates known to be
  compromised. A build with an older toolchain switches to 1.27.3 under
  `GOTOOLCHAIN=auto` and fails under `GOTOOLCHAIN=local`.

## [0.5.6] - 2026-10-19

No change to this module's code. The release pinned every internal module
at 0.5.6.

## [0.5.5] - 2026-10-19

The tags v0.5.1 to v0.5.5 shipped their changes under `[Unreleased]`, and
the entries were first written at 0.5.5. This section holds them, and
covers every change to this module since 0.5.0.

### Added

- **Service init deadline:** every singleton's Init (at startup and on
  `RestartService`) gets a deadline, 15 seconds by default. When it passes,
  Init's context is canceled and the container waits for Init to return,
  then fails with an error wrapping `context.DeadlineExceeded`; no Init is
  left running behind a failed start. An Init that ignores the cancellation
  and returns nil late is closed again. `gas.WithServiceInitTimeout` (a
  `WorkerOption`) and `gas.WithServiceContainerServiceTimeout` (a new
  `gas.ServiceContainerOption` for `NewServiceContainer`) change the
  default, and 0 disables it; both panic on a negative duration. A service
  implementing `gas.ServiceInitTimeout` sets its own; a negative value there
  fails Init without running it.
- **DI sentinel errors:** `gas.ErrNotRegistered`,
  `gas.ErrCircularDependency`, `gas.ErrCaptiveDependency`,
  `gas.ErrScopeRequired`, `gas.ErrNoConstructor`, `gas.ErrInvalidLifetime`,
  and `gas.ErrInvalidServiceShape`. Errors from `Resolve`, `BuildAll`, and
  startup wrap the matching sentinel, so callers can use `errors.Is` instead
  of matching message text. The messages are unchanged, except that the
  scoped-service error now says to use `container.NewScope(ctx)`.
- **`Router` method shorthands:** `Get`, `Head`, `Post`, `Put`, `Patch`,
  and `Delete` register a route for that method, as `Handle` does (#51).

### Changed

- **BREAKING: `*Worker` no longer implements `gas.Service`.** Its `Name`,
  `Init`, and `Close` were no-ops that existed only so a Worker could be
  passed as an owner, and they listed the Worker as `gas/worker` in
  `ActiveServices`, where it no longer appears (#48).
- **`Context.JSON` wraps an encoding error** as `failed to encode JSON:
  <cause>`; it used to return the encoder's error as is (#51).
- **BREAKING: `gas.NewNopLogger` returns an error.** It now returns
  `func() (*NopLogger, error)`, the `CONVENTIONS.md` shape. It never fails and
  returns nil. Registering it with `gas.WithSingletonService` is unchanged,
  since the container accepts a trailing error. Code that called
  `gas.NewNopLogger()()` for a value can use `&gas.NopLogger{}`, which is
  what the framework's own tests now do. With this, every built-in module
  constructor is on the convention (KI-004, #106).
- **BREAKING: core no longer imports the config module.** Core's
  `ConfigProvider` named `config.BindOption` and `providers.Provider`, and
  core's `Config` embedded `gasenv.WithGasEnv`, so core required
  `github.com/gasmod/gas/config` although modules are meant to meet only
  through core. The types now live in core and config implements them:
  `gas.BindOption` and `gas.BindOptions` (`Bind`'s options; `Validate`
  defaults to true), `gas.Environment` with `gas.EnvDevelopment`,
  `gas.EnvTesting`, `gas.EnvStaging` and `gas.EnvProduction`, and
  `gas.WithGasEnv`. `gas.ConfigProvider` drops `LoadProvider` and
  `LoadProviderContext`: loading sources is setup on the concrete
  `*config.Config` before it is registered, and nothing resolved through the
  interface called them. A custom `ConfigProvider` keeps working, since
  `config.BindOption` is now an alias of `gas.BindOption`; code that called
  `LoadProvider` through `gas.ConfigProvider` must call it on the
  `*config.Config` instead. The built-in modules embed `gas.WithGasEnv` and no
  longer import `config/extensions/gasenv` (KI-030, #105).
- **A panicking event subscriber no longer takes the process down.** `Emit`
  ran each subscriber on a bare goroutine, so one panicking handler crashed
  the process, and that was documented as intended. `Emit` now recovers it:
  the other subscribers still run and the returned WaitGroup still
  completes. The stack is written to stderr, and on a Worker's bus the panic
  is logged at error level as `event subscriber panic`, with the event type,
  the owning service, the panic value, and the stack. A handler that relied
  on a panic to stop the process must now exit explicitly (#9).
- **BREAKING: `gas.PrincipalFromContext` returns `(Principal, bool)`.** It
  returned a bare `Principal` that callers had to nil-check, the shape a
  context lookup in Go does not usually have. It now reports whether a
  principal is present, and a nil or typed-nil principal reports false.
  Replace `p := gas.PrincipalFromContext(ctx); if p == nil { ... }` with
  `p, ok := gas.PrincipalFromContext(ctx); if !ok { ... }`. In a DI-aware
  handler, `ctx.Principal()` remains the shorter path.
- **BREAKING: `gas.Service` takes a context.** `Init(ctx context.Context)`
  and `Close(ctx context.Context)` replace `Init()` and `Close()`; the
  container rejects a type with the old signatures and names the expected
  ones. A singleton's Init gets the caller's startup context plus its init
  deadline, and that context is canceled when Init returns, so background
  work must not derive from it. A scoped service's Init gets the scope's
  context; in an App that is the request context, and the request scope's
  Close gets that context without its cancellation.
- **BREAKING: lifecycle methods take the caller's context.**
  `Worker.Start(ctx)`, `Worker.InitServices(ctx)`, `Worker.Shutdown(ctx)`,
  `Worker.CloseService[T](ctx)`, `Worker.RestartService[T](ctx)`,
  `App.Start(ctx)`, `App.Stop(ctx)`, `ServiceContainer.BuildAll(ctx)`,
  `ServiceContainer.NewScope(ctx)`, and `Scope.Close(ctx)`. `Run` is
  unchanged and uses `context.Background()`. `App.Stop` bounds the HTTP
  server's shutdown by `ServerSettings.ShutdownTimeout` or its context,
  whichever ends first.
- **BREAKING: `Worker.Shutdown` runs once and returns Close errors.** It
  closes only services that are still active, so one already closed by
  `CloseService` is not closed again, and services it closes leave
  `ActiveServices`. Every Close error is returned, joined, instead of being
  logged; later calls return nil. Once Shutdown has begun, `CloseService`
  and `RestartService` return an error, so the kill switch cannot reopen a
  service after shutdown.
- **BREAKING: `Authenticator`, `Authorizer`, and `PrincipalRevoker` moved to
  the auth module.** They are now `auth.Authenticator`, `auth.Authorizer`, and
  `auth.PrincipalRevoker` in `github.com/gasmod/gas/auth`, the only module
  that implements them. Replace `gas.Authenticator` with `auth.Authenticator`
  (and likewise for the other two). `gas.Principal`, `gas.PrincipalMetadata`,
  `gas.BasePrincipalMetadata`, `gas.MetadataValue`, `gas.WithPrincipal`,
  `gas.PrincipalFromContext`, and `Context.Principal` stay in core.

### Removed

- **`gas.Config.Validate`.** `ConfigProvider.Bind` already validates the
  struct it binds, so `App.Start` no longer calls a separate `Validate`, and
  the method is gone; the rule lives in `Server.Host`'s validate tag. An App
  started without a `ConfigProvider` runs on its Config unchecked.
- **BREAKING: `gas.StorageProvider` is removed from core**, together with
  `gas.StorageObject`, `gas.ObjectInfo`, `gas.StorageOption`, `gas.InBucket`,
  `gas.WithContentType`, `gas.WithMetadata`, and `gas.ApplyStorageOptions`.
  Object storage is now `storage.Provider` and `storage.Presigner` in the
  `gas/storage` module; register `s3.NewProvider()` and `s3.NewPresigner()`
  (or `dir.NewProvider(...)`) under them. See `storage/CHANGELOG.md`.
- **BREAKING: `gas.MustPrincipalFromContext`.** It panicked on any request
  that reached a handler without a principal. Use `ctx.Principal()` in a
  handler, which returns a 401 `*gas.Error` the handler can return, or
  `gas.PrincipalFromContext` and check its `ok` result.
- **BREAKING: `Logger.SetBaseFields` and `gas.MutableLoggerContext`**, with
  `gas.NopMutableLoggerContext`. `SetBaseFields` changed a logger in place
  without synchronization, so calling it on the shared singleton (as
  `gas.RequestLogger` did) raced between requests and piled up fields for the
  life of the process. Derive a logger with `With()` and put it on the request
  context with `gas.WithLogger` instead; read it with `gas.LoggerFromContext`.
  `Logger` implementations no longer need the method.

### Fixed

- **Registering a service while the container resolves is no longer a data
  race.** The container's mutex guarded its instance maps but not its
  registrations, so a `Register*Service` call after startup raced with any
  concurrent `Resolve`, `CanResolve`, or request-scope resolution (`go test
  -race` reported it). The registrations are now guarded by the same mutex;
  validation and the topological sort work on a copy taken under it (#11).
- **`PrincipalFromContext` treats a typed-nil principal as absent.** A nil
  pointer stored with `WithPrincipal` came back as a non-nil `Principal`, so
  a `!= nil` check passed and `Context.Principal` returned it with no error;
  calling its methods could then panic. It now reports it as absent, and
  `Context.Principal` answers 401, matching what the auth middleware does
  since the typed-nil fix there (part of #12).
- **Request-scope `Close` errors are logged.** The App's request-scope
  middleware discarded the error from closing a request's scope, so a scoped
  service failing to clean up left no trace. It now logs `request scope close
  failed` at error level, with the request's method and path and the joined
  `Close` errors, through the Worker's logger (part of #12).
- **Server config validation no longer does a DNS lookup.** `Config.Validate`
  checked `Server.Host` with `net.LookupHost`, so startup depended on the
  resolver: slow or offline DNS delayed or failed `Start`, and a well-formed
  name that only resolves later (such as a container hostname) was rejected.
  `Server.Host` now carries the validate tag
  `required,max=253,ip|hostname_rfc1123`, which `ConfigProvider.Bind` checks
  when `App.Start` binds the Config: an IP address or an RFC 1123 hostname
  (no trailing dot), not resolved, so a host that does not resolve fails when
  the server listens. The error is the validator's (`Field validation for
  'Host' failed on the 'ip|hostname_rfc1123' tag`) under
  `gas: config binding:` (part of #12).
- **`App.Run` stops the App when the server fails.** When `Serve` returned an
  error, such as a port already in use, `Run` returned it without calling
  `Stop`, leaving every service `Start` had initialized open. It now runs
  `Stop` and returns the server error joined with any `Stop` error. The
  server error is no longer wrapped a second time in `gas: server error:`
  (part of #12).
- **Plain handlers get panic recovery.** Only DI-aware handlers were wrapped
  in recovery, so a panic in a plain `http.HandlerFunc` route or `NotFound`
  handler bypassed the error handler: net/http recovered it, logged it to the
  server's error log, and dropped the connection with no response. Plain
  handlers now get the same recovery: the stack goes to stderr and to the
  request's `Logger`, and the error handler renders the error (500 by
  default). `http.ErrAbortHandler` still aborts. The default error handler
  also no longer panics on a request with no request scope, such as one
  served by a `Router` used without an App (part of #12).
- **Boot validation no longer accepts dependencies that fail on every
  request.** A DI-aware handler, or `CanResolve`, accepted an interface that
  was only implemented by a registered concrete type, but resolution looks up
  the exact type, so startup passed and each request answered 500. Validation
  now uses the same exact-type lookup as resolution: register the
  constructor under the interface (`WithService[MyInterface](...)`) to inject
  it as one. The captive-dependency check uses the same lookup. A DI-aware
  `NotFound` handler's dependencies, never validated before, are now checked
  at startup like a route handler's (#10).
- **A failed `Start` no longer leaves services running.** When a service's
  `Init` failed, the services initialized before it were never tracked, so
  neither the failed `Start` nor a later `Shutdown` closed them; a failing
  migration or ready hook left every service running until the caller called
  `Shutdown`; and a second `Start` got nil from the run-once `InitServices`
  and ran migrations and ready hooks against a half-built container. Now a
  failed `Start` (or `InitServices`) closes, in reverse order, every service
  it had initialized, with `context.WithoutCancel` of its context, and
  returns the startup error joined with any `Close` errors. Later calls
  return that same error, and a later `Shutdown` has nothing to close. A
  pre-registered instance whose `Init` failed is not closed. `App.Start` does
  the same when its config fails to bind or validate after the Worker
  started.
- **A dependency cycle through a captured container no longer crashes the
  process.** A constructor or `Init` that resolved the type being built
  through a captured `*ServiceContainer` or `Scope` recursed until `fatal
  error: stack overflow`, which `recover` cannot catch. Resolution now tracks
  the types each goroutine is building and returns `ErrCircularDependency`
  naming the cycle (`A -> B -> A`), for every lifetime. Constructors nested
  more than 256 deep also return `ErrCircularDependency`.
- **Concurrent resolution of a lazy singleton builds it once.** The container
  had no lock, so goroutines resolving an unbuilt singleton at the same time
  raced and each ran its constructor and `Init`. One goroutine now builds it
  while the others wait for that build and share its instance; two builds on
  different goroutines that would wait on each other return
  `ErrCircularDependency` instead of deadlocking. The container's instance
  maps are guarded by a mutex, and constructors and `Init` run without it
  held.
- **The Worker's logger no longer races with the kill switch.** `Worker`
  resolved its logger lazily and unsynchronized on first use and, with no
  `Logger` registered, registered a slog fallback in the container, so
  `CloseService` and `RestartService` running concurrently raced on the
  logger field and the container's instance map (intermittently `fatal
  error: concurrent map read and map write`). The logger is now resolved
  once under a `sync.Once`, and the fallback stays private to the Worker:
  resolving `gas.Logger` with none registered keeps returning
  `ErrNotRegistered` instead of the fallback.
- **`gas.RequestLogger` no longer changes the logger it resolves.** It added
  `request_id` with `SetBaseFields`, so with the singleton `gas.Logger`
  concurrent requests raced and every request added one more `request_id` to
  the shared logger. It now derives a per-request logger with `With()`, runs
  the builder on it, and stores it on the request context with
  `gas.WithLogger`. When `T` is registered scoped (the documented
  `RequestLogger` type built from the singleton with `With()`), the request
  logger also replaces `T`'s instance in the request scope, so handlers and
  services that take `T` get its `request_id`, as they did before. A
  singleton `T` is never shadowed, and a scoped `T` whose instance is a
  `Service` is not replaced, so the scope still closes it.
  `gas.RequestLogger[gas.Logger]()` with the singleton is now safe too, with
  the request logger on the context only.

### Security

- **`BindJSON` caps the request body.** It decoded straight off the body with
  no limit, so large JSON bodies could exhaust memory within the read
  timeout. It now reads at most `Server.MaxJSONBodyBytes` (new, 1 MiB by
  default; zero or negative turns the cap off) and returns a 413 `*Error`
  with the new code `gas.CodePayloadTooLarge`, wrapping the
  `*http.MaxBytesError`. A Context built with `NewContext` gets the same 1 MiB
  default, and the new `gas.WithMaxJSONBodyBytes` option changes it. Handlers
  that read the body themselves are not capped (#8).

## [0.5.0] - 2026-09-19

The tags v0.3.1 to v0.4.6 shipped their changes under `[Unreleased]`,
which became this section at 0.5.0. It covers every change to this module
since 0.3.0.

### Added

- **Unified error shape** — `gas.Error` with `Status`, `Code`, `Message`,
  `Fields`, and `Details`, plus constructors (`gas.NotFound`,
  `gas.BadRequest`, `gas.Unprocessable`, and the rest), builder methods
  (`WithCause`, `WithField`, `WithDetail`), and `gas.AsError`. Handlers
  return it and core renders it, replacing the per-application error struct.
- **`gas.WriteError`** — the single rendering entry point, usable from
  handlers, custom `ErrorHandler`s, and custom middleware. It never logs and
  never touches the request scope, so it is safe before the scope middleware
  runs. `gas.WantsJSON` exposes the same Accept negotiation.
- **`Context.WriteError` and `Context.WriteErrorJSON`** write the unified
  response directly from a handler. v0.3.5 to v0.4.3 named them
  `Context.Error` and `Context.ErrorJSON` (#44).
- **Error constructors on `Context`**, one pair per package-level
  constructor: `NotFound(msg)` and `NotFoundErr(err, msg)`, and likewise for
  `BadRequest`, `Unauthorized`, `Forbidden`, `Conflict`, `Unprocessable`,
  `TooManyRequests`, `Internal`, and `ServiceUnavailable`. Each builds a
  `*gas.Error`, the `Err` form with `err` as its cause, and returns it for
  the `ErrorHandler` to render; none writes the response (#44).
- **`Context.Principal`** returns the request's authenticated principal, or
  a 401 `*gas.Error` when none is attached (#45).
- **`gas.ErrorResponse`** — the `{"error": {...}}` envelope, exported so Go
  clients and tests can decode it.
- **`Worker.ReadyFunc`** — the imperative form of `WithReadyFunc`. Safe for
  concurrent use; panics if called after the ready hooks have run, since the
  func would never execute.
- **`Scope.Resolve[T]` and `Scope.MustResolve[T]`** — resolve directly from a
  scope.
- `*Worker`, `*EventBus`, and `*Router` implement `gas.Service`, so they can
  be passed as owners. They appear in `ActiveServices` as `gas/worker`,
  `gas/eventbus`, and `gas/router`.

### Changed

- **BREAKING: Go 1.27 is required.** The API now uses generic methods.
- **BREAKING: DI registration and resolution are generic methods.**
  `gas.RegisterCtor`, `gas.RegisterInstance`, `gas.Resolve`, `gas.MustResolve`,
  `gas.TypePtr`, and the reflection-based type-token methods are removed. Use
  `c.RegisterService[T](ctor, lifetime)`, `c.RegisterSingletonService[T](ctor)`
  (and the scoped and transient variants), `c.RegisterServiceInstance[T](val)`,
  `c.Resolve[T]()`, and `c.MustResolve[T]()` on `*ServiceContainer`; the
  registration methods are forwarded on `*Worker`. `CanResolve` is now
  `c.CanResolve[T]()`. The `With*Service` options are unchanged.
- **BREAKING: `RegisterServiceInstance` registers under the static type.** It
  previously used the value's dynamic type; it now registers under `T` at the
  call site, so an interface-typed variable is resolvable as that interface
  only.
- **BREAKING: events are types, not values.** Declare an event by embedding
  its payload, `type UserCreated struct{ gas.Event[UserCreatedPayload] }`, and
  use the generic methods `bus.Emit[UserCreated](payload)`,
  `bus.Subscribe[UserCreated](handler)`, and
  `bus.SubscribeWithOwner[UserCreated](service, handler)`. The package-level
  `gas.Emit`, `gas.Subscribe`, and `gas.SubscribeWithOwner`, the string-keyed
  `EventBus` methods, and `Event.Name` are removed. Subscriptions are keyed by
  the event type, so two events never collide even with identical payloads.
  The system events (`gas.SystemServiceClosed` and the rest) are now types.
- **BREAKING: owners are a `gas.Service`, not a name.** `Router.Handle`,
  `Router.Register`, `Router.NotFound`, `Router.RemoveByService`,
  `EventBus.SubscribeWithOwner`, `EventBus.RemoveByService`, and
  `MigrationManager.Register` / `RegisterSlice` / `RegisterFS` take the service
  itself; pass `s` instead of `s.Name()`. `Migration.Service` is a
  `gas.Service`. Ownership is still tracked by `Name()`. A `nil` owner on the
  router means no owning service and is attributed to the root router
  (`gas/router`), on sub-routers too.
- **BREAKING: the kill switch is addressed by type.** `CloseService(name)` and
  `RestartService(name)` become `CloseService[T]()` and `RestartService[T]()`,
  where `T` is the exact type the service was registered under. Neither
  constructs a service that was never built.
- **BREAKING: `NopLoggerCtor` is removed**; `NewNopLogger` returns a plain
  `func() *NopLogger`.
- **BREAKING (gas/auth): the `db` stores' `Init` takes the owning
  `gas.Service`** instead of its name.
- **`gas.Error.Status` is serialized into the JSON body** as `"status"`. An
  invalid status is normalized to 500 in both the status line and the body, so
  the two always agree.
- **Constructor signatures are validated at registration.** `RegisterService`
  and its lifetime variants now panic at the call site,
  naming the constructor and the type it was registered for, when the
  constructor is not a function, is variadic, returns other than one or two
  values, produces a first result that is neither assignable to nor an
  implementation of the registered type, or returns a non-error second value.
  These previously registered cleanly and surfaced as a bare `reflect` panic
  out of `BuildAll` with no registration site in the message — except the
  three-result case, which built the service and silently dropped the
  constructor's error.
- The default `ErrorHandler` now renders a `gas.Error` at its own status
  instead of collapsing everything to a plain-text 500. Clients that do not
  explicitly prefer `text/html` receive the JSON envelope.
- Handler errors below status 500 log at warn level instead of error.
- Routes belonging to a service torn down via `CloseService` now return
  the unified error shape (503, `service_unavailable`) instead of a
  plain-text body. The status is unchanged.
- `Context.BindJSON` and `Context.BindForm` return `*gas.Error`: 400 for a
  malformed body, 422 with per-field detail for a validation failure. The
  underlying error remains reachable through `errors.As`.
- Validation field names now follow the `json` tag the client sent rather than
  the Go struct field name.

### Fixed

- **`CloseService` and `RestartService` deadlocked a subscriber that called
  back into the Worker.** Both emitted their system event while holding the
  Worker lock; they now release it first.
- **Shutdown closed services in a random order.** `Worker.Shutdown` documents
  "reverse initialization order", but `InitServices` derived `serviceOrder` by
  ranging over the container's instance map, and Go randomizes map iteration.
  A service could be closed after a dependency it uses inside `Close()`, which
  surfaced as an intermittent, per-process shutdown failure. The container now
  records the order instances become available and `EachInstance` walks it.
- `Scope.Close` had the same defect on the per-request path and now tears
  scoped services down in reverse resolution order.

- **Breaking:** a `Service` registered with `WithServiceInstance` is now
  initialized by the container. It previously was not: `Init` never ran, yet
  the container still closed it at shutdown and still called `Init` if it was
  restarted through the kill switch, so a pre-built service was closed without
  ever having been initialized. Implementing `gas.Service` hands the lifecycle
  to the container regardless of how the value was registered. Callers that
  called `Init()` themselves before registering should stop, or it runs twice.

- **Breaking:** a registered type that declares `Init` or `Close` but does not
  fully implement `gas.Service` is now rejected at startup with an error naming
  the missing or mis-typed methods, instead of being silently skipped. Writing
  one lifecycle hook and forgetting the rest used to produce a service that
  registered cleanly and then did nothing: `Init` never ran, `Close` never ran
  (leaking whatever it held), and the kill switch could not see it. `Init` and
  `Close` are the managed lifecycle, so declaring either commits to the whole
  interface. Declaring only `Name()` does not trigger it — such types stay
  ordinary dependencies. A third-party `io.Closer` (`*sql.DB` and the like) can
  no longer be registered directly; wrap it in a service of your own.
- **Breaking:** `Router.RemoveByModule` and `EventBus.RemoveByModule` are
  renamed to `RemoveByService`. Both always took a service name; "module" was
  vestigial vocabulary from before services were named services.
- Killing a service now disables every named middleware it registered wherever
  that middleware is referenced, not just the routes the service owns. A route
  guarded by a killed service's middleware returns 503 instead of continuing to
  run the torn-down middleware, so a teardown can never drop an authorization
  check and leave the route open. Named middleware is now resolved on each
  build of the routing tree rather than captured at registration; re-registering
  the name (the `RestartService` path) re-arms it.

### Security

- Errors that are not a `gas.Error`, including handler panics and DI
  resolution failures, collapse to a generic 500. The original reaches the
  logger only and is never serialized into a response body.

## [0.3.0] - 2026-07-02

First open source release. Versions prior to 0.3.0 were developed in a private
repository; this entry summarizes the framework as published.

### Added

- **App lifecycle** — `gas.NewApp` with a single-call `Run()`, plus composable
  `Start()` / `Serve()` / `Stop()` for custom orchestration. Graceful shutdown
  closes services in reverse init order.
- **Worker lifecycle** — `gas.Worker` for non-HTTP environments (Lambda,
  background workers, CLI tools) with the same DI, events, and migration
  support without a router or HTTP server.
- **Dependency injection container** with singleton, scoped, and transient
  lifetimes, constructor-based registration, automatic topological sorting,
  and lifetime-rule validation.
- **Router** with per-service ownership tracking, surgical service teardown
  (kill-switch), route grouping, idempotent `Route()` registration, DI-aware
  handlers, and automatic `HEAD` handlers for `GET` routes.
- **EventBus** for decoupled service-to-service communication, with
  ownership-tracked subscriptions and system events.
- **Middleware** — request logging, recovery, CSRF protection, and
  `SecurityHeaders` with configurable CSP, HSTS, and cross-origin policies.
- **Request scopes** for per-request service resolution.
- **Migrations** — migration registration and lifecycle integration, run
  automatically on startup.
- **Context and error handling** — `gas.Context` request helpers and a
  pluggable `ErrorHandler`.
- **Provider interfaces** implemented by the other gasmod modules:
  `ConfigProvider`, `DatabaseProvider`, `CacheProvider`, `StorageProvider`,
  `EmailProvider`, `JobQueueProvider`, `TemplateProvider`, `UIProvider`, and
  `Logger` (with `slog` and no-op implementations built in).
- **Authentication and authorization interfaces** — `Authenticator`,
  `Authorizer`, `PrincipalRevoker`, and `Principal`.
- **Health and readiness** — `HealthReporter` / `ReadyReporter` interfaces and
  ready hooks for startup gating.

### Fixed

- Eliminated a data race between the router and the service kill-switch by
  switching route storage to copy-on-write.

[Unreleased]: https://github.com/gasmod/gas/compare/v0.8.0...HEAD
[0.8.0]: https://github.com/gasmod/gas/compare/v0.7.0...v0.8.0
[0.7.0]: https://github.com/gasmod/gas/compare/v0.6.3...v0.7.0
[0.6.3]: https://github.com/gasmod/gas/compare/v0.6.2...v0.6.3
[0.6.2]: https://github.com/gasmod/gas/compare/v0.6.1...v0.6.2
[0.6.1]: https://github.com/gasmod/gas/compare/v0.6.0...v0.6.1
[0.6.0]: https://github.com/gasmod/gas/compare/v0.5.6...v0.6.0
[0.5.6]: https://github.com/gasmod/gas/compare/v0.5.5...v0.5.6
[0.5.5]: https://github.com/gasmod/gas/compare/v0.5.0...v0.5.5
[0.5.0]: https://github.com/gasmod/gas/compare/v0.3.0...v0.5.0
[0.3.0]: https://github.com/gasmod/gas/releases/tag/v0.3.0
