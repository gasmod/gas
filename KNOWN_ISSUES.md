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
  dependencies as parameters; app code can still reach it.
- **Next step**: decide whether resolution should detect re-entrant
  construction of a type and return a cycle error (a §3 decision in
  `CLAUDE.md`).

## KI-006: DI errors have no sentinel

- **Status**: decision needed
- **Area**: core DI (`service_registry.go`).
- **Found**: 2026-10-16, while writing DI tests.
- **Detail**: the core package defines no exported `Err*` sentinels, and
  every DI error is a plain `fmt.Errorf` in `service_registry.go`:

  - `:287` resolver is nil
  - `:302` resolved incorrect type
  - `:322` and `:407` no registration
  - `:338` scoped service resolved outside a scope
  - `:344` and `:428` unknown lifetime
  - `:464` no constructor
  - `:546` and `:552` captive dependency
  - `:625` circular dependency
  - `:837` and `:865` startup service-shape errors

  Callers and tests can only match message text, contrary to §2 of
  `CLAUDE.md`, which asks for `errors.Is` or `errors.As`; the core's own
  `unhappy_path_test.go:31`, `:55`, and `:78` use `strings.Contains`.
- **Next step**: one §3 decision covering sentinels for all of these.

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
