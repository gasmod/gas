# Known issues

Defects, test gaps, and debts found in Gas that are not fixed by the change
that found them. An entry stays here until the fix is merged; the fix removes
the entry in the same change, and, when the fix is visible to users, its
`CHANGELOG.md` entry records what was fixed.

Each entry has a stable ID (`KI-NNN`, never reused), and these fields:

- **Status**: `open`, `investigating`, `decision needed` (a design decision
  is pending), or `fix in progress` with the branch or PR.
- **Area**: the module and package.
- **Found**: the date, and how it was found.
- **Detail**: what is wrong, with the evidence (error text, file and line,
  input that triggers it).
- **Next step**: what has to happen to close it.

## KI-010: suspected flake in the s3 multipart content-length test

- **Status**: open
- **Area**: `storage/s3` tests.
- **Found**: 2026-10-16, in review of the darwin lock-open fix, once in
  about 31 runs.
- **Detail**: the `multipart short` case of
  `TestProvider_PutObjectContentLength` (`storage/s3/provider_test.go:1108`)
  failed when an `UploadPart` send to the in-process fake S3 hit ENOBUFS
  (`write: no buffer space available`). The transfer manager keeps the first
  error, so that transport error masked `ErrContentLengthMismatch` and the
  `errors.Is` assertion failed. The runtime behavior is correct; only the
  assertion is fragile under transport errors.
- **Next step**: if it recurs, accept a transport error in that case. The
  fake already reads each request body in full before it replies
  (`storage/s3/helpers_test.go:56`), so that alone does not prevent it.

## KI-032: `RenderFragment` renders nothing for a page that only defines `content`

- **Status**: open
- **Area**: `ui`.
- **Found**: 2026-10-21, docs claim tests.
- **Detail**: `Engine.RenderFragment` executes the `__page__` template
  (`ui/render.go:177`), which holds only the page's text outside any
  `{{define}}`. A page written to the convention, which defines only
  `content`, renders an empty fragment, so the documented HTMX pattern of
  one template with two entry points (`Render` for the full page,
  `RenderFragment` for the swap) does not work. Claim test:
  `TestRenderFragmentSkipsLayout` in `docs/snippets/claims/guides-html`
  (`fragment = "", want "<ul>...</ul>"`).
- **Next step**: decide what a fragment of a convention page is (the
  `content` block, say) and render that, or change the documented pattern.

## KI-033: `template/dir.Store` cannot be registered as `gas.TemplateProvider`

- **Status**: open
- **Area**: `template/dir`.
- **Found**: 2026-10-21, docs claim tests.
- **Detail**: `Store` declares `Close() error` (`template/dir/store.go:186`)
  and no `Name` or `Init`, so the container rejects it at Start:
  `*dir.Store declares Close but does not implement gas.Service`. The
  package comment (`template/dir/doc.go`) and the guides say to register
  `NewStore(dir)` under `gas.TemplateProvider`. Claim test:
  `TestDirStoreWiredAsTemplateProvider` in
  `docs/snippets/claims/guides-html`.
- **Next step**: give `Store` the `gas.Service` shape, or document another
  way to wire it.

## KI-034: `gas.Email.Headers` is never sent by `email/ses`

- **Status**: open
- **Area**: `email/ses`.
- **Found**: 2026-10-21, docs claim tests.
- **Detail**: `Service.Send` (`email/ses/ses.go:201` onward) builds an SES
  `SendEmail` request from the recipients, subject and bodies and never
  reads `Headers` (`providers.go:124`), so custom headers are dropped
  without an error. Claim test: `TestCcBccReplyToAndHeadersAreSent` in
  `docs/snippets/claims/guides-email` (the header is absent from the SES
  request form).
- **Next step**: send headers (SES `SendEmail` has no header field, so this
  means a raw message), or reject or document them as unsupported.

## KI-035: a recovered handler panic is logged without the request ID

- **Status**: open
- **Area**: core, `handler.go`.
- **Found**: 2026-10-21, docs claim tests.
- **Detail**: `handlePanic` logs through `requestLogger(r)`
  (`handler.go:135`), which resolves the base `gas.Logger` from the request
  scope (`handler.go:41-47`). That is the registered singleton.
  `RequestLogger[T]` puts its `request_id`-enriched logger on the request
  context with `WithLogger` and into the scope's slot for `T`
  (`middleware.go:147`), never under `gas.Logger`, so the panic line lacks
  `request_id` even with chi's `RequestID` and `gas.RequestLogger`
  mounted. Claim test: `TestPanicLoggedThroughRequestLogger` in
  `docs/snippets/claims/guides-errors`.
- **Next step**: log through `LoggerFromContext(r.Context())` when it is
  set, falling back to the scope's `gas.Logger`.

## KI-036: `ses` and `sqs` methods panic before Init

- **Status**: open
- **Area**: `email/ses`, `queue/sqs`.
- **Found**: 2026-10-21, docs audit.
- **Detail**: the client is created in Init, and the methods check only
  `closed`, so on a service that was built but not started they dereference
  a nil client: `ses.Service.Send` (`email/ses/ses.go:201`) and
  `sqs.Service.Enqueue`, `Dequeue`, `Ack` and `Nack`
  (`queue/sqs/service.go:196, 239, 340, 357`). A probe calling each on a
  fresh constructor result panics with a nil pointer dereference instead of
  returning an error.
- **Next step**: return a not-initialized error, as `migrate` does since
  v0.6.0.

## KI-037: the api-server example never runs its migrations

- **Status**: open
- **Area**: `example/api-server`.
- **Found**: 2026-10-21, docs audit.
- **Detail**: `example/api-server/app/app.go:53` registers
  `migrate.New()` under `*migrate.Service`, not `gas.MigrationManager`. The
  Worker looks for `gas.MigrationManager`, so it runs nothing, and the
  auth, files and shares services, which take `gas.MigrationManager`
  (`auth/service.go:43`, `files/service.go:53`, `shares/service.go:51`),
  fail to resolve with `ErrNotRegistered`. The test hides it by registering
  a `migratetest.MockMigrationManager` (`app/app_test.go:83`).
- **Next step**: register under `gas.MigrationManager`, and make the test
  exercise the app's own registration.

## KI-038: `database` Close leaks the pgx pool when the sql.DB close fails

- **Status**: open
- **Area**: `database`.
- **Found**: 2026-10-21, docs audit.
- **Detail**: `Service.Close` returns as soon as `s.db.Close()` fails
  (`database/service.go:238-240`), before the `s.pool.Close()` below it
  (`database/service.go:246`), so in pgx mode the pool stays open.
- **Next step**: close both and join the errors.

## KI-039: `gas.WithServiceInstance` with a nil interface panics at Start

- **Status**: open
- **Area**: core, `service_registry.go`.
- **Found**: 2026-10-21, docs audit.
- **Detail**: registering a nil interface value, such as a
  `gas.ConfigProvider` variable holding nil passed to
  `gas.WithServiceInstance[gas.ConfigProvider]`, stores a zero
  `reflect.Value`. Start then panics in `claimInstanceInit`
  (`service_registry.go:354`, `v.Interface()`) with `reflect: call of
  reflect.Value.Interface on zero Value`, confirmed by a probe.
- **Next step**: reject a nil instance at registration with an error.

## KI-040: `apikey` Generate panics on a short KeyLength from WithConfig

- **Status**: open
- **Area**: `auth/apikey`.
- **Found**: 2026-10-21, docs audit.
- **Detail**: Generate base64-encodes `KeyLength` random bytes and slices
  `fullKey[:len(Prefix)+8]` (`auth/apikey/apikey.go:241`). Below 6 bytes the
  encoded key is under 8 characters, so the slice panics. A bound config is
  checked by `validate:"min=16"` (`auth/apikey/config.go:26`), but a config
  given with WithConfig is used as given.
- **Next step**: check the length in Init or Generate and return an error.

## KI-041: `log.WithEventInitialCapacity` with a negative value panics

- **Status**: open
- **Area**: `log`.
- **Found**: 2026-10-21, docs audit.
- **Detail**: the option stores the value as given (`log/slog.go:39`), and
  every event then calls `make(..., 0, capacity)` (`log/slog.go:59`
  onward), which panics with `makeslice: cap out of range` on the first log
  call, confirmed by a probe.
- **Next step**: reject or clamp a negative capacity.

## KI-042: `log.Handler` cannot be built outside the package

- **Status**: open
- **Area**: `log`.
- **Found**: 2026-10-21, docs audit.
- **Detail**: `log/doc.go` says `Handler` is exported so it can be composed
  into custom handler chains, but its fields are unexported
  (`log/httpsink.go:38-43`) and no constructor returns one:
  `NewShippingHandler` returns a `slog.Handler` (`log/shipping.go:180-184`).
  A zero `Handler` has a nil sender.
- **Next step**: add a constructor, or unexport the type and fix the doc.

## KI-043: `database` health errors use the wrong prefix

- **Status**: open
- **Area**: `database`.
- **Found**: 2026-10-21, docs audit.
- **Detail**: `CheckHealth` and `CheckReady` return `database: closed` and
  `database: not initialized` (`database/service.go:273, 276, 286`), while
  every other error of the package starts with the service name,
  `gas/database`. `database/doc.go` documents the exception.
- **Next step**: use the service name, and drop the exception from the doc.

## KI-044: the `apikey` MySQL migration fails on MySQL 8.4

- **Status**: open
- **Area**: `auth/apikey/db/mysql`.
- **Found**: 2026-10-21, docs claim tests.
- **Detail**: the migration `create api keys` (Version 20250323002,
  `auth/apikey/db/mysql/20250323002_create_api_keys.up.sql:8`) declares
  `scopes TEXT NOT NULL DEFAULT ''`. MySQL rejects a literal default on a
  TEXT column: `Error 1101 (42000): BLOB, TEXT, GEOMETRY or JSON column
  'scopes' can't have a default value`. The session and token stores
  migrate cleanly. Claim test: `TestStoresSupportMySQL` in
  `docs/snippets/claims/guides-auth`, with a DSN of
  `multiStatements=true&parseTime=true`. Context: each auth MySQL
  migration file holds several statements (a CREATE TABLE and its CREATE
  INDEX lines), and `migrate` runs a file in one `ExecContext`
  (`migrate/manager.go:263`), so on MySQL the DSN needs
  `multiStatements=true` for any of them to apply.
- **Next step**: drop the default or use an expression default
  (`DEFAULT ('')`), and document the `multiStatements=true` requirement.
