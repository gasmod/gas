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
  `TestKI032FragmentEmpty` in
  `docs/snippets/claims/project-known-issues` (the fragment of a page
  that defines only `content` is empty).
- **Next step**: decide what a fragment of a convention page is (the
  `content` block, say) and render that, or change the documented pattern.

## KI-033: `template/dir.Store` cannot be registered as `gas.TemplateProvider`

- **Status**: open
- **Area**: `template/dir`.
- **Found**: 2026-10-21, docs claim tests.
- **Detail**: `Store` declares `Close() error` (`template/dir/store.go:186`)
  and no `Name` or `Init`, so the container rejects it at Start:
  `*dir.Store declares Close but does not implement gas.Service`. The
  package comment (`template/dir/doc.go`) documents the limitation. Claim
  test: `TestKI033DirStoreRejected` in
  `docs/snippets/claims/project-known-issues`.
- **Next step**: give `Store` the `gas.Service` shape, or document another
  way to wire it.

## KI-034: `gas.Email.Headers` is never sent by `email/ses`

- **Status**: open
- **Area**: `email/ses`.
- **Found**: 2026-10-21, docs claim tests.
- **Detail**: `Service.Send` (`email/ses/ses.go:201` onward) builds an SES
  `SendEmail` request from the recipients, subject and bodies and never
  reads `Headers` (`providers.go:124`), so custom headers are dropped
  without an error. Claim test: `TestKI034HeadersDropped` in
  `docs/snippets/claims/project-known-issues` (the header is absent from
  the SES request form).
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
  mounted. Claim test: `TestKI035PanicLoggedWithoutRequestID` in
  `docs/snippets/claims/project-known-issues`.
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
  migrate cleanly. Claim test: `TestKI044APIKeyMigrationFailsOnMySQL` in
  `docs/snippets/claims/project-known-issues`, with a DSN of
  `multiStatements=true&parseTime=true`. Context: each auth MySQL
  migration file holds several statements (a CREATE TABLE and its CREATE
  INDEX lines), and `migrate` runs a file in one `ExecContext`
  (`migrate/manager.go:263`), so on MySQL the DSN needs
  `multiStatements=true` for any of them to apply.
- **Next step**: drop the default or use an expression default
  (`DEFAULT ('')`), and document the `multiStatements=true` requirement.

## KI-045: sub-routers drop the parent's validator and form decoder

- **Status**: open
- **Area**: core, `router.go`.
- **Found**: 2026-10-21, docs claim tests.
- **Detail**: `NewRouter` sets `validator` and `formDecoder`
  (`router.go:190-191`; fields at `router.go:100-101`), the decoder with
  `SetAliasTag("form")` and `IgnoreUnknownKeys(true)`. `newSubRouter`
  (`router.go:205-228`) copies neither, so handlers registered inside
  `Group` or `Route` are adapted with nil (`router.go:406-407`), and the
  Context falls back per request to `schema.NewDecoder()` and a fresh
  validator (`context.go:449-460`): no `form` tag alias, unknown keys
  rejected, and validation rules registered on the router's validator
  absent. Repro: a field tagged `form:"user_name"`, POST
  `user_name=bob&extra=1`: 200 `bob` on a top-level route, 400
  `invalid_form` under `Route` or `Group`.
- **Next step**: copy both fields from the parent in `newSubRouter`.

## KI-048: `migrate` on MySQL requires `parseTime=true`

- **Status**: open
- **Area**: `migrate/db/mysql`.
- **Found**: 2026-10-21, docs claim tests.
- **Detail**: `GasMigrations.AppliedAt` is a `time.Time`
  (`migrate/db/mysql/models.go:17`), scanned at
  `migrate/db/mysql/queries.sql.go:39` and `:83`. Without `parseTime=true`
  in the DSN the driver returns `DATETIME` as bytes, so the first
  `RunPending` succeeds and every later read of the tracking table fails:
  `gas/migrate: failed to query applied migrations: sql: Scan error on
  column index 5, name "applied_at": unsupported Scan, storing
  driver.Value type []uint8 into type *time.Time`.
- **Next step**: scan `applied_at` in a form that works without
  `parseTime`, or check the DSN and fail with a clear error; document the
  requirement either way.

## KI-049: the `template/db` MySQL migration needs `multiStatements=true`

- **Status**: open
- **Area**: `template/db/mysql`.
- **Found**: 2026-10-21, docs claim tests.
- **Detail**: the migration `create templates table` (Version
  20260322235959,
  `template/db/mysql/20260322235959_create_templates.up.sql`) holds a
  CREATE TABLE and a CREATE INDEX, and `migrate` runs a file in one
  `ExecContext` (`migrate/manager.go:263`). Without `multiStatements=true`
  in the DSN, `RunPending` fails with `Error 1064` near `'CREATE INDEX
  idx___gas_templates_namespace'`. Same mechanism as KI-044.
- **Next step**: split the statements, or document the
  `multiStatements=true` requirement.

## KI-050: `RequestLogger[T]` silently keeps a concrete scoped `T`

- **Status**: open
- **Area**: core, `middleware.go`, `service_registry.go`.
- **Found**: 2026-10-21, docs claim tests.
- **Detail**: the per-request logger comes from `With().Logger()`
  (`middleware.go:137-139`), whose type is the backend's, and
  `replaceScoped` (`service_registry.go:741-757`) replaces the scoped slot
  only when that type is assignable to `T` (`service_registry.go:747`).
  For a concrete `T` such as `struct{ gas.Logger }` the slot is kept with
  no error or log, so handlers and services resolving `T` get the original
  logger without `request_id`. The logger put on the request context with
  `WithLogger` (`middleware.go:147`) does carry it.
- **Next step**: reject a `T` the derived logger cannot be assigned to
  when the middleware is built, or log it.

## KI-051: `storage/s3` option doc comments say a nil client is ignored

- **Status**: open
- **Area**: `storage/s3`.
- **Found**: 2026-10-21, docs claim tests.
- **Detail**: `WithClient`, `WithPresignerS3Client` and
  `WithPresignClient` assign their value, nil included
  (`storage/s3/provider.go:32-34`, `storage/s3/presigner.go:41-49`), so a
  later nil discards an earlier client and the constructor builds one from
  config (`storage/s3/provider.go:78`, `storage/s3/presigner.go:90`). Their
  doc comments say "A nil client is ignored." (or "A nil presigner is
  ignored.").
- **Next step**: skip a nil value in the options, or fix the comments.

## KI-052: `cache/valkey` Init's context does not bound the dial

- **Status**: open
- **Area**: `cache/valkey`.
- **Found**: 2026-10-21, docs claim tests.
- **Detail**: `connect` builds the client with `valkey.NewClient`, which
  takes no context (`cache/valkey/service.go:150-156`), so against a
  server that accepts and never answers, Init runs until
  `cache.dialtimeout` ends the attempt, whatever its context's deadline.
  Measured: 30s with a 300ms context and a 30s `dialtimeout`.
- **Next step**: bound the dial by the context, for example through the
  client option's dial function.

## KI-053: `ui` keeps serving errors after a failed recompile

- **Status**: open
- **Area**: `ui`.
- **Found**: 2026-10-21, docs claim tests.
- **Detail**: `Engine.Build` sets `built` to true only after a successful
  compile (`ui/render.go:128-132`); a failure returns early
  (`ui/render.go:100`, `:107`, `:123`) and leaves the previous cache in
  place but `built` false, as `AddFuncs` set it (`ui/render.go:88`).
  `Engine.lookup` rebuilds whenever `built` is false or `devMode` is on
  (`ui/render.go:194-201`) and returns the build error before it reads the
  cache. So after one failed recompile, for example `RegisterFuncs`
  triggering a rebuild while another page has a parse error, every later
  render in production returns that error, and the previously compiled
  templates, still in memory, are never served.
- **Next step**: serve the previous templates after a failed rebuild, or
  stop rebuilding until something changes, and decide whether the render
  should still report the error.

## KI-054: `RestartService` revives routes the restarted Init did not re-register

- **Status**: open
- **Area**: core, `router.go`.
- **Found**: 2026-10-21, docs claim tests.
- **Detail**: `RemoveByService` (`router.go:596`) moves the service's
  routes into `r.removed`, which only overlays them with 503 handlers on
  each rebuild (`router.go:738`). The route ops themselves stay in
  `pendingOps` (`router.go:686` `applyOp`), and `buildMux`
  (`router.go:708`) replays all of them. On restart, the service's first
  `Handle` runs `recordBookkeeping`, whose `delete(r.removed, name)`
  (`router.go:507`) drops the overlay for all of the service's routes.
  Repro: a service registers GET `/a` and GET `/b` in its first Init and
  only `/a` in later ones. After Start both answer 200; after
  `CloseService` both answer 503 and `Routes()` is empty; after
  `RestartService` `/a` answers 200 and `/b` answers 200 from the first
  Init's handler, while `Routes()` lists only `/a`. A path re-registered
  with a different handler is likely affected too; that case is untested.
- **Next step**: drop a closed service's route ops from `pendingOps` (or
  mark them dead) so a restart replays only what its Init registers.

## KI-056: `Routes()` misreports global named middleware for routes registered before the `Use`

- **Status**: open
- **Area**: core, `router.go`.
- **Found**: 2026-10-21, docs claim tests.
- **Detail**: `Handle` records a route's bookkeeping when it is called
  (`router.go:427`), and `recordBookkeeping` copies `r.scopeMiddleware`
  as it stands then (`router.go:500-503`). A top-level `Use` of named
  middleware appends to `scopeMiddleware` when it is called
  (`router.go:295`), while group bodies run at Seal, and `newSubRouter`
  copies `scopeMiddleware` (`router.go:206-207`) after every top-level
  `Use`. So a top-level route registered before the `Use` is reported
  without the middleware, and a grouped route registered before it is
  reported with it. Repro: `Register(nil, "audit")`; `Get /before`;
  `Group{ Get /group-before }`; `UseMiddlewareByName("audit")`;
  `Get /after`; Start. All three responses carry audit's header, but
  `Routes()` returns `map[gas/router:[{GET /before []} {GET /after
  [audit]} {GET /group-before [audit]}]]`. This contradicts the
  order-independence the router's doc comment states
  (`router.go:56-61`). Serving is correct; only `Routes()` and the
  route-map log are wrong.
- **Next step**: compute a route's middleware names when the tree is
  built, after every pending `Use` is known, rather than at the `Handle`
  call.

## KI-057: `migrate` keeps a duplicate of a service's migrations on every restart

- **Status**: open
- **Area**: `migrate`, `migrate/manager.go`.
- **Found**: 2026-10-22, docs review.
- **Detail**: services register their migrations in `Init`, and
  `RestartService` re-runs `Init` on the same instance. `Register` and
  `RegisterSlice` append to `s.migrations[service.Name()]` without
  checking for an identical entry (`migrate/manager.go:16-31`), so each
  restart adds another copy. `registeredMigrations` collapses identical
  copies when it reads them (`migrate/manager.go:371-377`), so nothing
  runs twice, but the slice grows by one copy per restart for the life of
  the process.
- **Next step**: skip an identical migration (same service, version,
  description, up and down) at registration time, and keep the
  read-time check for conflicting content.

## KI-058: `FieldError` godoc names only the `json` tag

- **Status**: open
- **Area**: core, `errors.go`.
- **Found**: 2026-10-23, docs review.
- **Detail**: the `FieldError` godoc (`errors.go:28-29`) says `Field`
  carries "the name the client sent (the json tag)". The validator's tag
  name function (`validation.go:16-27`) uses the `json` tag, falls back
  to the `form` tag when there is none, and falls back to the Go field
  name when neither is set or the `json` tag is `-`. The errors page
  (`core/errors.mdx`) describes the full rule; the godoc does not.
- **Next step**: state the `json`, then `form`, then Go field name order
  in the godoc.

## KI-062: `Config.Set` with a dotted key does nothing over a value

- **Status**: open
- **Area**: `config`, `config.go`.
- **Found**: 2026-10-28, maputil rebuild.
- **Detail**: `Set` (`config/config.go:123`) finds the parent map with
  `maputil.FindNestedMap(c.values, pathParts, true)`
  (`config/config.go:134`), which returns nil when a level on the way
  holds a value rather than a map, and `Set` then returns without a word.
  Repro: `c.Set("db", "scalar")`, then `c.Set("db.host", "h")`:
  `Get("db")` is still `"scalar"` and `Get("db.host")` is nil. `Set` has
  no error to report it with. `SetDefault` has the same path
  (`config/config.go:77`), where leaving a set key alone is intended. The
  package documentation states the behavior, and `TestConfig_SetUnderValue`
  (`config/test/config_test.go`) pins it.
- **Next step**: decide whether `Set` replaces the value with a map, as
  its doc says, or reports the failure, which needs an error return.

## KI-063: `Set` and `SetDefault` store map values without normalizing keys

- **Status**: open
- **Area**: `config`, `config.go`.
- **Found**: 2026-10-28, maputil rebuild.
- **Detail**: every other way into the tree lowercases and trims keys
  with `maputil.NormalizeKeys`, and `Get` and `Find` lowercase the path
  they are given (`keyToPathParts`, `config/config.go:314`). `Set` and
  `SetDefault` store a copy of `value` with its keys as given
  (`config/config.go:80`, `:136`), so a map value with uppercase keys
  cannot be reached by key. Repro:
  `c.Set("app", map[string]any{"Name": "x"})`: `Get("app")` is
  `map[Name:x]` and `Get("app.name")` is nil. Two keys of such a map that
  differ only in case are also not reported as a conflict, which
  `SetDefaults` and the providers do. `Bind` still fills a field from
  `Name`, because it matches keys without regard to case.
- **Next step**: normalize map values in `Set` and `SetDefault` the way
  `SetDefaults` does, which needs a way to report a conflict.

## KI-071: JSON numbers lose digits and turn into exponents on the way to a field

- **Status**: decision needed
- **Area**: `config`, `internal/jsonobj` and `secretsmanager`, with the
  exponent written by `internal/maputil`.
- **Found**: 2026-10-28, second maputil review.
- **Detail**: the JSON provider decodes with `jsonobj.Decode`, which
  calls `json.Unmarshal` into an `any`
  (`config/internal/jsonobj/jsonobj.go:13`), so every number is a
  float64. An integer past 2⁵³ loses its last digits before `Bind` sees
  it, with no error: `{"id": 1234567890123456789}` binds to an `int64`
  as `1234567890123456768`. A string field takes a number formatted with
  `fmt.Sprint` (`config/internal/maputil/convert.go:105`), which writes a
  float64 of 10⁶ or more, or below 10⁻⁴, with an exponent:
  `{"account": 1234567}` binds to a string as `"1.234567e+06"`. Both
  repros ran through `config.New` with the JSON provider. The second is
  what the package documentation says, but it is unlikely to be what a
  user wants. The Secrets Manager provider decodes JSON secrets the same
  way, with `jsonobj.Decode` at the root and `json.Unmarshal` at a key
  (`config/secretsmanager/fetch.go:80`), so it hits both:
  `{"account_id": 123456789012}` binds to a string as
  `"1.23456789012e+11"`, reproduced through `config.New` with the provider
  on a fake client. The env and `.env` providers give strings, so neither
  reaches these paths.
- **Next step**: decide where to fix it. Decoding with `UseNumber` in
  `jsonobj` and at a key in the Secrets Manager provider keeps the digits, but leaves `json.Number` in `any`
  fields and `Get` results unless the providers turn each one into an
  `int64` when it is a whole number that fits and a `float64` otherwise,
  which changes `Get` from `float64` to `int64` for whole numbers.
  Formatting whole floats without an exponent in `toString` fixes only the
  second.

## KI-080: `Set` and `SetDefault` store a key with an empty level

- **Status**: open
- **Area**: `config`, `config.go`.
- **Found**: 2026-10-28, config root rebuild.
- **Detail**: `keyToPathParts` (`config/config.go:314`) splits a key on
  dots and keeps empty levels, and `Set` and `SetDefault` add a map for
  each one. Repro: `c.Set("a..b", 1)` makes `Values()` hold
  `{"a": {"": {"b": 1}}}`, and `c.Set(" ", 1)` stores at the empty key.
  No provider can produce an empty key, as `maputil.NormalizeKeys` drops
  it, and `Bind` never reads one, so the value is reachable only by `Get`
  with the same malformed key. The Secrets Manager provider rejects such a
  key with `ErrInvalidKey`; `Set` has no error to report it with.
  `TestConfig_SetEmptyLevel` (`config/test/config_test.go`) pins the
  current behavior.
- **Next step**: decide with KI-062, which needs the same error return:
  report the key, or ignore it as the empty key is ignored.

## KI-081: `.env` syntax errors quote a character of the value

- **Status**: decision needed
- **Area**: `config`, `internal/dotenv`.
- **Found**: 2026-10-28, config root rebuild.
- **Detail**: an unexpected character is quoted in the error
  (`charErr`, used at `config/internal/dotenv/parser.go:102`, `:187`,
  `:190`), and the package documentation shows it (`2:5: unexpected
  character: 'y'`). When the character follows an unquoted value, it is
  often part of what the user meant as the value: `PASSWORD=correct
  horse` fails with `1:18: unexpected character: 'h', want newline: key:
  "PASSWORD"`, and `PASSWORD="abc"def` with `'d'`, both reproduced
  through `DotEnvProvider`. The package documentation says an error never
  includes a value from the file, and the config documentation names this
  as the one exception. The JSON and Secrets Manager providers give only
  an offset for a syntax error (`config/internal/jsonobj/jsonobj.go`).
- **Next step**: decide whether `.env` errors drop the character, as the
  JSON ones do, keeping the line and column, which already locate it.
