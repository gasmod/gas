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
