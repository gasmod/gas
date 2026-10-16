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
