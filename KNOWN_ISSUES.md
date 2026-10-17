# Known issues

Defects, test gaps, and debts found in Gas that are not fixed by the change
that found them. An entry stays here until the fix is merged; the fix removes
the entry in the same change, and, when the fix is visible to users, its
`CHANGELOG.md` entry records what was fixed.

Each entry has a stable ID (`KI-NNN`, never reused), and these fields:

- **Status**: `open`, `investigating`, `decision needed` (a §3 decision in
  `CLAUDE.md` is pending), or `fix in progress` with the branch or PR.
- **Area**: the module and package.
- **Found**: the date, and how it was found.
- **Detail**: what is wrong, with the evidence (error text, file and line,
  input that triggers it).
- **Next step**: what has to happen to close it.

## KI-004: built-in module constructors are not on the `CONVENTIONS.md` shape

- **Status**: open
- **Area**: the DI constructors of the built-in modules.
- **Found**: 2026-10-16, while writing `CONVENTIONS.md`.
- **Detail**: `CONVENTIONS.md` makes
  `func NewModule(opts ...Option) func(<dependencies>) (*Module, error)` the
  shape of every constructor whose result is registered in the DI
  container. On `main` none of them return an error:

  - `func(deps...) *Service`: `auth/jwt.New`, `auth/apikey.New`,
    `auth/session.New`, `auth/token.New`, `cache/memory.New`,
    `cache/valkey.New`, `database.New`, `email/ses.New` and
    `email/ses.NewWithCustomProvider`, `migrate.New`, `queue/sqs.New`, and
    `ui.New`.
  - `func(deps...) *Store`: `auth/apikey/db.New`, `auth/session/db.New`,
    `auth/token/db.New`, and `template/db.NewStore`.
  - `func() *Store`: `template/dir.NewStore(dir)` and
    `template/fs.NewStore(fsys)`.
  - `func() *<logger type>`: `gas.NewNopLogger`, `log.NewZeroLogLogger`,
    `log.NewSlogLogger`, and `log.NewShippingLogger`.

  Out of scope, because an app builds the value directly rather than
  registering a constructor: in core, `NewApp`, `NewWorker`, `NewRouter`,
  `NewServiceContainer`, `NewEventBus`, `NewContext`, and `NewError`;
  `auth.NewPrincipal`; `config.New`,
  `config/extensions/gasenv.NewExtension`, `config/providers`
  `NewDotEnvProvider`, `NewEnvProvider`, and `NewJSONProvider`,
  `config/providers/secretsmanager.NewProvider`, and
  `config/configtest.NewMockConfigWithValues`; `log.NewShippingHandler`
  and `log.NewOTLPMarshaler`; `template/composite.NewStore` and
  `template/memory.NewStore`; and `ui.NewEngine`. Generated sqlc code is out
  of scope too.
- **Next step**: migrate each module to the convention in its own PR.

## KI-005: a cycle through a captured `*gas.ServiceContainer` crashes the process

- **Status**: decision needed
- **Area**: core DI (`service_registry.go`).
- **Found**: 2026-10-16, while testing constructors that resolve through
  the container.
- **Detail**: `BuildAll` checks for cycles using constructor parameter
  types only. A constructor that calls `Resolve` on a captured container,
  and so comes back to the type being built, recurses until the process
  dies with `fatal error: stack overflow`. This is a runtime fatal error:
  not a panic, not an error, and `recover` cannot catch it. Verified on
  `main` with:

  ```go
  var w *gas.Worker
  w = gas.NewWorker(gas.WithSingletonService[*T](func() (*T, error) {
  	_, err := w.ServiceContainer().Resolve[*T]()
  	return &T{}, err
  }))
  w.InitServices(context.Background()) // fatal error: stack overflow
  ```

  `CONVENTIONS.md` keeps built-in modules off this path by declaring
  dependencies as parameters; app code can still reach it. An `Init` that
  resolves its own type through a captured container recurses the same
  way, because `invoke` runs `Init` before `setInstance` caches the value.

  `Resolve` takes no chain or context, so a resolve from a captured
  container cannot be told apart from a concurrent one without goroutine
  identity or an API change. Options considered: tracking a build stack
  while `BuildAll` runs (exact, and covers the repro above, since
  `BuildAll` is single-goroutine), plus a depth bound as a backstop for
  resolves outside `BuildAll` (turns the overflow into an error but cannot
  name the cycle). Either one interacts with KI-011, since a concurrent
  resolve during a build would see a false cycle.
- **Next step**: decide whether resolution should detect re-entrant
  construction of a type and return a cycle error, and how (a §3
  decision in `CLAUDE.md`); see KI-011 for the concurrency half.

## KI-007: `auth.Middleware` treats a nil principal as authenticated

- **Status**: decision needed
- **Area**: `auth` (`middleware.go`, `auth.go`).
- **Found**: 2026-10-16, while moving the auth interfaces into the auth
  module.
- **Detail**: `Middleware` treats any nil error from `Authenticate` as
  success. At `auth/middleware.go:49` it calls `Authenticate`, and at
  `:55` and `:56` it stores the returned principal with `gas.WithPrincipal`
  and calls the next handler, even when that principal is nil. `Chain`
  passes the same result on (`auth/auth.go:89` and `:90`), so one member
  returning `(nil, nil)` ends the chain as a success. A handler that calls
  `Context.Principal` still gets a 401, and `RequireScheme` still answers
  403, but a route that relies on `Middleware` alone runs for a caller
  with no credentials. The `jwt`, `session`, and `apikey` services never
  return `(nil, nil)`; `authtest.MockAuthenticator` does when
  `AuthenticateFn` is nil (`auth/authtest/mock.go:45`), and so can any
  app-written `Authenticator`. Repro sketch:

  ```go
  type none struct{}

  func (none) Authenticate(
      context.Context, *http.Request,
  ) (gas.Principal, error) {
      return nil, nil
  }

  ok := func(w http.ResponseWriter, _ *http.Request) {
      w.WriteHeader(http.StatusOK)
  }
  h := auth.Middleware(none{})(http.HandlerFunc(ok))
  rr := httptest.NewRecorder()
  h.ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))
  // rr.Code == 200: the protected handler ran without a principal.
  ```

- **Next step**: decide the contract (a §3 security decision in
  `CLAUDE.md`): whether `Middleware` and `Chain` reject a nil principal
  with `ErrUnauthenticated`, and whether the `Authenticator` doc and the
  `authtest` mock's default change to match.

## KI-009: `auth.Middleware` panics with an `auth:` prefix

- **Status**: open
- **Area**: `auth` (`middleware.go`).
- **Found**: 2026-10-16, while moving the auth interfaces into the auth
  module.
- **Detail**: `Middleware` with a nil authenticator panics with
  `"auth: nil authenticator"` (`auth/middleware.go:35`). §7 of
  `CLAUDE.md` asks every panic message to start with `gas:` and say how
  to fix the mistake.
- **Next step**: reword the message, for example
  `"gas: auth.Middleware: nil Authenticator; pass a configured
  authenticator"`.

## KI-010: suspected flake in the s3 multipart content-length test

- **Status**: open
- **Area**: `storage/s3` tests.
- **Found**: 2026-10-16, in review of the darwin lock-open fix, once in
  about 31 runs.
- **Detail**: the `multipart short` case of
  `TestProvider_PutObjectContentLength` (`storage/s3/provider_test.go:1088`)
  failed when an `UploadPart` send to the in-process fake S3 hit ENOBUFS
  (`write: no buffer space available`). The transfer manager keeps the first
  error, so that transport error masked `ErrContentLengthMismatch` and the
  `errors.Is` assertion failed. The runtime behavior is correct; only the
  assertion is fragile under transport errors.
- **Next step**: if it recurs, make the fake read the whole part before it
  replies, or accept a transport error in that case.

## KI-011: concurrent lazy singleton resolution is a data race

- **Status**: decision needed
- **Area**: core DI (`service_registry.go`).
- **Found**: 2026-10-16, while designing the KI-005 cycle guard.
- **Detail**: `ServiceContainer` has no mutex; `registrations`,
  `instances`, and `instanceOrder` are plain maps and slices. `BuildAll`
  builds every singleton on one goroutine, and after that resolution only
  reads them, but a singleton resolved lazily (before `InitServices`,
  registered after it, or from a constructor through a captured
  container) is built on whichever goroutine asks. With four goroutines
  calling `Resolve` on the same unbuilt singleton, whose constructor
  sleeps 20ms, the constructor ran four times and `go test -race`
  reported three data races.
- **Next step**: decide whether lazy resolution becomes concurrency-safe,
  for example one in-flight build per type under a container lock with
  later callers waiting, weighing the deadlock risk of holding it while a
  constructor and `Init` run (a §3 decision in `CLAUDE.md`).

## KI-012: ui static files are unreachable with the default settings

- **Status**: open
- **Area**: `ui` (`config.go`, `service.go`).
- **Found**: 2026-10-16, while writing the ui `doc.go`.
- **Detail**: the comment on `Settings.StaticStripPrefix`
  (`ui/config.go:46`) says it defaults to `StaticPath` when empty, but
  `initStaticRoute` passes it unchanged (`ui/service.go:147` and `:149`),
  so with the defaults nothing is stripped. A request for
  `/static/app.css` is looked up as `static/app.css` inside the static
  root, which for the default `StaticDir` of `static/` is
  `./static/static/app.css`; a file at `./static/app.css` is reachable at
  no URL. Verified with the service built over an `fstest.MapFS` holding
  `app.css` and `static/app.css`, default config: `GET /static/app.css`
  served the nested file and `GET /app.css` answered 404.
- **Next step**: apply the documented default (strip `StaticPath`'s
  literal prefix when `StaticStripPrefix` is empty) or change the
  documentation. Either changes which URLs serve which files, so it is a
  decision for Ahmed.

## KI-013: ui template rebuilds race with concurrent renders

- **Status**: open
- **Area**: `ui` (`render.go`).
- **Found**: 2026-10-16, while writing the ui `doc.go`.
- **Detail**: `Engine.Build` resets `layouts`, `partials`, and `pages`
  (`ui/render.go:90`) and `classify` appends to them (`:201` to `:205`)
  outside `e.mu`, while renders read them (`:124`, `:129`, `:182`). In a
  development-like environment every render calls `Build`, so concurrent
  renders race: 8 goroutines rendering 20 times each produced 28 data race
  reports under `go test -race`. The same probe passes without `-race`,
  so the failure is silent corruption rather than a crash.
- **Next step**: hold `e.mu` across `Build`, or build into locals and
  swap them in under the lock. A plain bug fix, outside the current task
  list.

## KI-014: the Worker's lazy logger races with the kill switch

- **Status**: decision needed
- **Area**: core (`worker.go`, `service_management.go`).
- **Found**: 2026-10-16, in review of the context-aware lifecycle change.
- **Detail**: `Worker.getLogger` resolves the logger on first use and
  stores it in `w.logger` without a lock (`worker.go:230`). With no
  `Logger` registered it also registers a slog fallback
  (`worker.go:237`), which writes the container's `instances` map
  (`service_registry.go:158`). `CloseService` logs after releasing `w.mu`
  (`service_management.go:140` and `:145`), while `RestartService` reads
  the same map under `w.mu` (`resolveBuilt`, `service_registry.go:259`).
  Run concurrently, this is a data race under `go test -race`, and
  without it an intermittent `fatal error: concurrent map read and map
  write` (about one in fifteen runs of a 300-iteration probe). Lines are
  as on `main`.
- **Next step**: making a `NopLogger` the Worker's default, set at
  construction, should remove the lazy registration and the lazy field
  write; verify that when that change lands.

## KI-015: a failed Start leaves initialized services open

- **Status**: decision needed
- **Area**: core (`worker.go`).
- **Found**: 2026-10-16, in review of the context-aware lifecycle change.
- **Detail**: `InitServices` records services for shutdown only after
  `BuildAll` succeeds (`worker.go:105` to `:122` on `main`), so when one
  service's Init fails, the services already initialized are never
  closed, by the failed start or by a later `Shutdown`. `InitServices`
  also runs once (`initOnce`), so a second `Start` after a failure gets
  nil from it and goes on to run migrations and ready hooks against a
  half-built container.
- **Next step**: decide whether a failed start closes what it
  initialized, and whether a later `Start` returns the original error (a
  §3 lifecycle decision in `CLAUDE.md`).

## KI-020: a dirty migration can only be cleared by hand

- **Status**: decision needed
- **Area**: `migrate`.
- **Found**: 2026-10-16, while writing the migrate `doc.go`.
- **Detail**: a failed migration is recorded as dirty, and while any
  version is dirty `RunPending` refuses to run. The `Service` exposes
  `Register`, `RegisterSlice`, `RegisterFS`, `RunPending`, `Down`, and
  `CheckReady`; none of them clears the mark, so recovery needs manual SQL
  against `__gas_migrations`.
- **Next step**: decide whether the module should offer a way to clear
  or force a dirty version (a §3 API decision in `CLAUDE.md`).

## KI-021: one service can register the same migration version twice

- **Status**: open
- **Area**: `migrate` (`manager.go`).
- **Found**: 2026-10-16, while writing the migrate `doc.go`.
- **Detail**: `allMigrationsSorted` (`migrate/manager.go:351`) rejects a
  version registered by two different services but not one registered
  twice by the same service. `RunPending` then applies the first, and
  recording the second hits the tracking table's primary key; the failure
  marks the version dirty, although its first migration did apply, which
  blocks every later run.
  Verified on SQLite: `UNIQUE constraint failed: __gas_migrations.version`,
  then one dirty version.
- **Next step**: reject a duplicate version from the same service at
  registration or in `RunPending`.

## KI-023: two template db stores register the same migration

- **Status**: open
- **Area**: `template/db` (`store.go`).
- **Found**: 2026-10-16, while writing the template `doc.go`.
- **Detail**: the package documents namespaces as the way for several
  stores to share the `__gas_templates` table, but every store registers
  the table's migration, version `20260322235959`, under the same service
  name, `gas/template/db` (`template/db/store.go:79` and `:105`). Two
  stores therefore register one version twice under one service, which
  is the case in KI-021: `RunPending` applies it once, fails on the
  tracking table's primary key for the second, and marks the version
  dirty. Verified with a recording migration manager (two stores, two
  registrations of `gas/template/db@20260322235959`); the `RunPending`
  outcome is KI-021's, verified separately.
- **Next step**: register the migration once however many stores exist,
  or have the migration manager accept an identical re-registration.

## KI-024: template db store cannot be written to while services start

- **Status**: open
- **Area**: `template/db` (`store.go`), with the Worker's startup order.
- **Found**: 2026-10-16, while writing the template `doc.go`.
- **Detail**: the store's table comes from a migration it registers when
  it starts, and the Worker applies migrations only after every service
  has started. A service that registers templates into the store from its
  own start-up, as services are told to do, writes before the table
  exists on a fresh database. Verified on SQLite: `Register` before
  `RunPending` failed with `no such table: __gas_templates`, and succeeded
  after it.
- **Next step**: create the table when the store starts instead of
  through a deferred migration, or document that templates can only be
  written once startup has finished.

## KI-025: a ShippingLogger sub-logger's Flush does nothing

- **Status**: open
- **Area**: `log` (`slog.go`, `shipping.go`).
- **Found**: 2026-10-16, while writing the log package's `doc.go`.
- **Detail**: `ShippingLogger` embeds `*SlogLogger` and overrides `Flush`
  to post the current batch (`log/shipping.go:214`). `With()` is inherited,
  and `SlogLoggerContext.Logger` returns a plain `*SlogLogger`
  (`log/slog.go:243`) whose `Flush` is a no-op (`log/slog.go:100`). The
  sub-logger's records still ship, through the same handlers, but its
  `Flush` does not wait for them. Repro: a `ShippingLogger` with
  `WithFlushInterval(time.Hour)`, `sub := l.With().Str("k", "v").Logger()`,
  `sub.Info("x").Send()`, `sub.Flush()`: the endpoint has received 0
  batches; `l.Flush()` then delivers 1. Services usually hold such a
  sub-logger (`logger.With().Str("service", ...).Logger()`), so their
  `Flush` calls do nothing.
- **Next step**: have the shipping logger's `With` return a logger whose
  `Flush` reaches the sender.

## KI-026: `SetBaseFields` on a shared logger races and accumulates fields

- **Status**: decision needed
- **Area**: core (`middleware.go`, `logger.go`) and `log` (`slog.go`,
  `zerolog.go`).
- **Found**: 2026-10-16, while writing the log package's `doc.go`.
- **Detail**: `gas.Logger.SetBaseFields` is documented as "intended for use
  by request-scoped middleware" and mutates the receiver. The log backends
  apply it by replacing the logger's fields without synchronization
  (`log/slog.go:180`, `log/zerolog.go:143`). `gas.RequestLogger` calls it
  on the logger it resolves from the request scope to add `request_id`
  (`middleware.go:106`). When the app registers its logger as a singleton,
  as the log README does, that is the shared logger. Repro: an App with a
  `SlogLogger` instance registered as `gas.Logger`, chi's `RequestID` and
  `gas.RequestLogger[gas.Logger]()` as middleware, then 20 parallel
  requests and one more: `go test -race` reports data races in
  `SlogMutableLoggerContext.Apply` from `RequestLogger`, and the last
  request's log line carries 9 `request_id` fields. The fields grow with
  every request for the life of the process.
- **Next step**: decide the contract (a §3 decision in `CLAUDE.md`): a
  per-request logger for the middleware (for example deriving with `With`
  and storing the result on the request), synchronized and non-accumulating
  `SetBaseFields`, or a documented requirement that the logger be scoped.

## KI-027: config file reads follow symlinked directories out of the working directory

- **Status**: open
- **Area**: `config` (`internal/sysfs`).
- **Found**: 2026-10-16, while writing the config packages' `doc.go`.
- **Detail**: the default file system for `JSONProvider` and
  `DotEnvProvider` refuses paths outside the working directory and paths
  whose last element is a symlink. The directory check compares the path
  as written (`config/internal/sysfs/safe_readfile.go:41`), and only the
  last element is checked with `Lstat` (`:46`), so a symlinked directory
  along the path is followed. Repro: in the working directory, a
  directory `link` that is a symlink to a temporary directory outside it,
  holding `secret.json`; `NewJSONProvider(WithJSONFilePath("link/secret.json")).Load()`
  returns the file's values. A symlinked file and an absolute path outside
  the directory are both refused, as documented. The path comes from the
  app, so this weakens a defense rather than opening one to request input.
- **Next step**: resolve symlinks before the directory check (for example
  `filepath.EvalSymlinks`), or open through an `os.Root` on the working
  directory.

## KI-028: secretsmanager `WithTimeout(0)` makes every Load fail

- **Status**: decision needed
- **Area**: `config` (`providers/secretsmanager`).
- **Found**: 2026-10-16, while writing the config packages' `doc.go`.
- **Detail**: `WithTimeout` stores any duration
  (`config/providers/secretsmanager/provider.go:120`), and `Load` uses it
  as `context.WithTimeout(context.Background(), p.timeout)` (`:147`). Zero
  or a negative value gives a context that has already expired. Repro: a
  provider with an injected client that checks its context,
  `WithSecret("s")` and `WithTimeout(0)` (or `-1`): `Load` returns
  `failed to fetch secret s: context deadline exceeded`. Other gas options
  treat 0 as "no limit" or ignore values that are not positive.
- **Next step**: decide what zero and negative values mean here (a §3
  default-value decision in `CLAUDE.md`): no timeout, the default, or an
  error.

## KI-029: an unclosed quote in a .env file drops the rest of the file silently

- **Status**: open
- **Area**: `config` (`internal/dotenv`).
- **Found**: 2026-10-16, while writing the config packages' `doc.go`.
- **Detail**: a value that opens a quote without closing it on its line
  starts a multi-line value (`config/internal/dotenv/parser.go:27`,
  `handleMultiline` at `:134`). If no later line ends with that quote,
  every following line is absorbed and, at the end of the file, the value
  is discarded with no error (`:55`). Repro: `Parse` of
  `A=1\nB="unterminated\nC=3\n` returns only `A`; `B` and `C` are gone and
  the error is nil. A typo in one `.env` line can silently remove the
  configuration below it, contrary to the §7 rule that bad configuration
  produces an error.
- **Next step**: return an error wrapping `ErrDotEnvParseFailed` for a
  value whose quote is never closed. Files that load today with this
  mistake would then fail to load.

## KI-030: core and the modules import the config module

- **Status**: decision needed
- **Area**: core (`go.mod`, `providers.go`, `config.go`) and the modules'
  configuration structs.
- **Found**: 2026-10-16, while writing the config packages' `doc.go`.
- **Detail**: `CLAUDE.md` says modules meet only through the core, yet
  core's `go.mod` requires `github.com/gasmod/gas/config`. Core's
  `gas.ConfigProvider` names `config.BindOption` and `providers.Provider`
  (`providers.go:288`, `:292`, `:293`), and core's own `config.go` embeds
  `gasenv.WithGasEnv` (`config.go:9`, `:25`). Outside the config module,
  these non-test files import `config/extensions/gasenv` to embed
  `WithGasEnv` in their configuration struct: `auth/apikey/config.go`,
  `auth/jwt/config.go`, `auth/session/config.go`, `auth/token/config.go`,
  `cache/memory/config.go`, `cache/valkey/config.go`, `database/config.go`,
  `email/ses/config.go`, `queue/sqs/config.go`, `storage/s3/config.go`,
  `ui/config.go` and `ui/funcmap.go`. `auth/internal/testutil/testutil.go`
  imports `config/configtest`, and four test files under the modules import
  config packages directly. `CLAUDE.md` notes that such imports predate the
  rule and are being rewritten toward it.
- **Next step**: Ahmed decides the target shape (a §3 module-boundary
  decision in `CLAUDE.md`), for example moving the bind-option and provider
  types and the environment type into core so neither core nor the modules
  import config.

## KI-031: the .env parser and the safe file system have no tests or fuzz targets

- **Status**: open
- **Area**: `config` (`internal/dotenv`, `internal/sysfs`).
- **Found**: 2026-10-16, while writing the config packages' `doc.go`.
- **Detail**: `config/internal/dotenv` and `config/internal/sysfs` have no
  test files of their own (`go test` reports "no test files"); they are
  reached only through the provider tests. No fuzz target covers .env or
  JSON parsing, although §2 of `CLAUDE.md` asks for native fuzzing wherever
  configuration sources are parsed. KI-029, an unclosed quote silently
  dropping the rest of a .env file, is the kind of defect a fuzz target
  finds.
- **Next step**: add unit tests for both packages and fuzz targets for
  `dotenv.Parse` and the JSON provider's decoding.
