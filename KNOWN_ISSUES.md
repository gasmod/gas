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

## KI-018: migrate Down runs the down SQL of a dirty version

- **Status**: open
- **Area**: `migrate` (`manager.go`).
- **Found**: 2026-10-16, while writing the migrate `doc.go`.
- **Detail**: `Down` takes its versions from `getAppliedMigrations`
  (`migrate/manager.go:188`), whose query has no `dirty` filter, so a
  dirty version counts as applied. A dirty version's up SQL was rolled
  back, so `Down` runs its down SQL against a schema that migration never
  changed. Verified on SQLite: with 001 creating table `keep` and a failed
  002 whose down SQL drops `keep`, `Down(1)` returned nil and dropped
  `keep`.
- **Next step**: skip dirty versions in `Down`, or refuse to run while
  any version is dirty, as `RunPending` does.

## KI-019: migrate Down is not atomic

- **Status**: open
- **Area**: `migrate` (`manager.go`).
- **Found**: 2026-10-16, while writing the migrate `doc.go`.
- **Detail**: `applyDown` commits the down SQL in its own transaction and
  then deletes the tracking row outside it (`migrate/manager.go:316` to
  `:335`), unlike the up path, where the SQL and the row commit together.
  If the delete fails, the schema is reverted but the row remains, so
  `RunPending` treats the migration as applied and never re-applies it.
  Verified on SQLite by making the delete fail: `Down(1)` returned the
  error with the table dropped and the row still present, and a later
  `RunPending` returned nil without recreating the table.
- **Next step**: delete the tracking row inside the same transaction as
  the down SQL.

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
