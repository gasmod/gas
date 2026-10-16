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
  w.InitServices() // fatal error: stack overflow
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

## KI-016: memory cache Set can panic on a stopped or unstarted service

- **Status**: open
- **Area**: `cache/memory` (`service.go`).
- **Found**: 2026-10-16, while writing the cache `doc.go`. Suspected from
  reading the code; a 2000-round probe racing `Set` against `Close` did
  not trigger it.
- **Detail**: `Set` checks `closed` (`cache/memory/service.go:152`) before
  it takes the mutex (`:165`), and does not check it again before writing
  `s.items[key]` (`:178`). `Close` sets `closed` (`:106`) and then, under
  the mutex, sets `s.items` to nil (`:112` and `:113`). If `Set` passes its
  check, `Close` runs to completion, and `Set` then takes the mutex, the
  write goes to a nil map and panics with `assignment to entry in nil
  map`. A `Set` on a service that was constructed but never started
  writes to the same nil map. `Get`, `Exists`, and `Delete` only read or
  delete, which a nil map allows.
- **Next step**: check `closed` again after taking the mutex in `Set` (or
  keep an empty map on `Close`), with a test that drives the interleaving.

## KI-017: valkey cache operations panic before the service starts

- **Status**: open
- **Area**: `cache/valkey` (`service.go`).
- **Found**: 2026-10-16, while writing the cache `doc.go`.
- **Detail**: the client is created when the service starts, and `closed`
  starts false, so on a `Service` that was constructed but never started
  `Get`, `Set`, `Delete`, and `Exists` pass their `closed` check and call
  the nil client (`cache/valkey/service.go:182`, `:206` and `:208`,
  `:223`, `:235`), and so does `CheckReady` (`:258`). Each one panics with
  `invalid memory address or nil pointer dereference`. Verified for the
  four cache methods with a throwaway test.
- **Next step**: return `cache.ErrClosed`, or a not-started error, when
  the client is nil, since §7 of `CLAUDE.md` reserves panics for wiring
  mistakes.
