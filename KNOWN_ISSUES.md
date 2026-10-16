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

## KI-002: s3 backend has no real-service test suite after the storage rewrite

- **Status**: decision needed
- **Area**: `storage/s3`
- **Found**: 2026-10-15, while splitting the uncommitted work into PRs.
- **Detail**: `main` tests the s3 backend against LocalStack
  (`storage/s3/integration_test.go`). The pending storage rewrite deletes
  that suite and drops testcontainers from `storage/go.mod`, so the s3
  backend would be tested only through mocks and fakes, which §2 of
  `CLAUDE.md` does not accept. MinIO is ruled out by its license, and no
  replacement container has been chosen.
- **Next step**: choose the S3-compatible container for the suite, then
  add it back.

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
    `email/ses.NewWithCustomProvider`, `migrate.New`, `queue/sqs.New`,
    `storage/dir.New`, `storage/s3.New`, and `ui.New`.
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

## KI-022: template dir store panics after Close

- **Status**: open
- **Area**: `template/dir` (`store.go`).
- **Found**: 2026-10-16, while writing the template `doc.go`.
- **Detail**: the store opens its `os.Root` once, through a `sync.Once`
  (`template/dir/store.go:50`), and `Close` sets it to nil (`:135`). The
  `Once` does not run again, so a `Get` or `List` after `Close` calls
  `FS()` on the nil root (`:69`, `:86`) and panics with `invalid memory
  address or nil pointer dereference`. Verified with a throwaway test for
  `Get`. `Close` also writes `s.root` without the store's lock, so a `Close`
  concurrent with a read is a data race.
- **Next step**: return an error after `Close` (for example a closed flag
  checked under the lock), since §7 of `CLAUDE.md` reserves panics for
  wiring mistakes.

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
