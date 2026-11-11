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

- **Go 1.27.0 or later is required**, down from 1.27.3, as for the core:
  build apps with Go 1.27.4 or later, for the `net/http` fix of
  [GO-2026-6859](https://pkg.go.dev/vuln/GO-2026-6859).
- Dependencies updated, and the internal modules required at v0.8.0.

## [0.7.0] - 2026-11-11

### Changed

- The internal modules are required at v0.7.0.

## [0.6.3] - 2026-11-10

### Changed

- The internal modules are required at v0.6.3.

## [0.6.2] - 2026-11-10

### Changed

- The internal modules are required at v0.6.2.

## [0.6.1] - 2026-11-10

### Changed

- Dependencies updated, and the internal modules required at v0.6.1.

## [0.6.0] - 2026-11-08

v0.6.0 rewrites the module around one store contract, which the
`template` package's `doc.go` documents and a shared conformance suite
checks against every store. Each package's `doc.go` is its behavioral
contract and lists its errors verbatim; read them before upgrading.

### Changed

- **BREAKING: every constructor returns a constructor.**
  `memory.NewStore` and `composite.NewStore` now return
  `func() (*Store, error)`, as `dir.NewStore`, `fs.NewStore` and
  `db.NewStore` already did; call the result, or register it with
  `gas.WithSingletonService`. Every constructor checks its arguments:
  `fs.NewStore` returns `template.ErrNilFS` for a nil `fs.FS`,
  `dir.NewStore` returns `dir.ErrEmptyDir` for an empty path,
  `composite.NewStore` returns `composite.ErrNilProvider` for a nil
  provider, and `db.NewStore` returns `db.ErrNilDatabase`,
  `db.ErrNilLogger` or `db.ErrNilMigrations` for a nil dependency.
- **BREAKING: `dir.Store` is a `gas.Service`.** It has `Name`,
  `Init(ctx)` and `Close(ctx)` in place of `Close() error`, so it can be
  registered as `gas.TemplateProvider` (KI-033). `Init` opens the
  directory, which used to open on first use, and fails with an error
  wrapping `dir.ErrDirUnavailable` that never gives the path. Before
  the first successful `Init` every method returns
  `template.ErrNotInitialized`, after `Close` `template.ErrClosed`, and
  `Init` on an open store `template.ErrAlreadyInitialized`; `Init` may
  open a closed store again, which used to stay closed for good.
- **BREAKING: names are checked.** Every method of every store that
  takes a name checks it with `template.CheckName` before anything else
  and refuses, with an error wrapping `template.ErrInvalidName`, a name
  that is empty, not valid UTF-8, holds a NUL byte or a backslash, is
  not a clean slash path as `fs.ValidPath` accepts, or does not end in
  one of the store's extensions. Names are compared byte for byte on
  every store, also on a file system or a database that ignores case.
- **BREAKING: the extension set applies to every method.** A store reads
  and writes only names that end in one of its extensions, `.html` by
  default; Get of any other name is `template.ErrInvalidName`, where the
  `dir` and `fs` stores used to read any file and the `memory` and `db`
  stores stored any name. The new `WithExtensions` option of the
  `memory`, `fs`, `dir` and `db` stores sets others, such as `.html` and
  `.txt` for emails.
- **BREAKING: templates have a maximum size.** Every store but
  `composite` and the mock holds templates of at most 16 MiB unless its new
  `WithMaxSize` option gives another; `Register`, `RegisterFS` and Get
  refuse a larger one with an error wrapping `template.ErrTooLarge`,
  without reading a larger file or row whole.
- **BREAKING: content is copied.** `Register` keeps a copy of what it is
  given and Get returns a copy, so the caller may change either slice;
  the `memory` store and the `dir` overlay used to keep and return the
  caller's slice. Nil content is stored as empty, and Get returns empty
  content as an empty, non-nil slice.
- **BREAKING: `List` returns exactly what Get can read.** It returns the
  names once each, sorted in byte order, and an empty, non-nil list for
  an empty store. The `dir` and `fs` stores leave out symlinks they do
  not follow and anything but a regular file.
- **BREAKING: read and walk failures are reported.** On the `dir` and
  `fs` stores, a file that is there but cannot be read is an error
  wrapping `template.ErrRead`, where it used to be
  `template.ErrTemplateNotFound`, and a walk that fails is an error
  wrapping `template.ErrList`; `dir.Store.List` used to ignore walk
  errors and return a partial or empty list. `dir.Store.List` reports a
  directory removed or moved since `Init` as `template: list failed:
  directory removed or moved`, on macOS and Linux alike.
- **BREAKING: `RegisterFS` registers all or nothing.** It reads every
  file before it registers any, checking the context before each one,
  so a failure or a canceled context registers nothing; it used to
  register files one at a time and ignore the context. It registers
  regular files only, never symlinks, and a file with one of the
  store's extensions whose name `CheckName` refuses is an error, not
  skipped. A nil `fs.FS` is `template.ErrNilFS`. The `db` store
  registers the files in one transaction.
- **BREAKING: `composite` returns its providers' errors.** Get goes to
  the next provider only on an error wrapping
  `template.ErrTemplateNotFound` or `template.ErrInvalidName`; any other
  error is returned at once and unchanged, where Get used to treat every
  failure as not found. `List` fails with the error of the first
  provider that fails, where it used to return the names of the
  providers that did not. `Register` and `RegisterFS` return the
  writable provider's error unchanged, no longer wrapped in `composite:
  register:`.
- **BREAKING: error texts.** Every error starts with `template: `, once,
  and gives what it adds after the sentinel, such as the quoted name in
  `template: not found: "pages/home.html"`. `template.ErrReadOnly` reads
  `template: read-only store` (was `template: read-only provider`), and
  `template.ErrClosed` `template: store closed` (was `template: store is
  closed`). Errors never hold a database's connection details or an
  absolute path outside a store's root.
- **BREAKING: `db` takes a context on `Exists` and `Delete`.**
  `Exists(ctx, name)` and `Delete(ctx, name)` replace `Exists(name)` and
  `Delete(name)`, which used `context.Background()`.
- **BREAKING: `db` accepts three driver names.** `Init` picks the SQL
  from the provider's `Driver`: `postgres`, `mysql` or `sqlite`, the
  names gas/database reports; `pgx` and `sqlite3` are no longer
  accepted. Any other driver is an error wrapping
  `db.ErrUnsupportedDriver`, which no longer quotes the driver. `Init`
  returns `db.ErrNilDatabase` when the provider's DB is nil.
- **BREAKING: `db` has a lifecycle.** Before the first successful `Init`
  every method returns `template.ErrNotInitialized`, where it used to
  panic; after `Close`, `template.ErrClosed` until `Init` runs again;
  and `Init` on an open store returns `template.ErrAlreadyInitialized`.
- **BREAKING: `db` statement errors give no driver text.** A failed
  statement is an error wrapping `db.ErrQuery` that names the operation
  and the template, then gives the driver's code only (`mysql error`
  and its number, `postgres error` and its SQLSTATE, `sqlite error` and
  its result code), since a driver's text can hold the database's user,
  address and name. A provider's `SafeError(error) error`, as
  gas/database's Service has, is applied first, and an ended context,
  `sql.ErrConnDone` and `sql.ErrTxDone` are kept, wrapped.
- **BREAKING: `db` names are exact and limited.** Names and namespaces
  are at most 255 characters on every dialect; a longer name is
  `template.ErrInvalidName` before any statement runs, and
  `db.WithNamespace` refuses an empty, invalid or longer namespace with
  an error wrapping `db.ErrInvalidNamespace`. On MySQL names are
  compared byte for byte after the new migration, where the default
  collation ignored case and accents. MySQL must be 8.0.19 or later;
  MariaDB is not supported.
- **`db` drops sqlc** for plain SQL per dialect. `List` sorts the names
  in Go, in byte order, whatever the database's collation, and leaves
  out a name the store would refuse. `Init` logs `store initialized`
  with `dialect` and `namespace`, in place of `template store
  initialized` with `driver`.
- **`db` registers a second migration**, `20261031120000`, `exact
  template names`, next to `20260322235959`, which is unchanged. It
  drops the index on `namespace`, which the unique key on namespace and
  name already serves, on every dialect. On MySQL it also changes
  `namespace` and `name` to the utf8mb4_0900_bin collation and
  `content` from MEDIUMBLOB to LONGBLOB, lifting the 16 MiB cap, in one
  ALTER TABLE run under STRICT_ALL_TABLES. Both migrations are owned by
  the store under `db.ServiceName`. Reverting it on MySQL fails, and
  leaves the migration dirty, when the rows do not fit the old columns;
  see the package documentation.
- **`dir` reads safely.** Get looks a name up through the root without
  following symlinks, opens the file without blocking, and checks that
  what it opened is the regular file it looked up, so a FIFO, a device
  or a file swapped in meanwhile is an error wrapping
  `template.ErrRead`, never a read that waits. A symlink to a file is
  followed only when every target is a relative path inside the
  directory that ends in one of the store's extensions; a symlink to a
  directory is neither walked nor passed through.
- **`fs` never follows a symlink it can tell is one**, neither on the
  way to a name nor as the template itself.
- **`templatetest.MockTemplate` behaves as an empty store by default.**
  With a field nil, Get returns an error wrapping
  `template.ErrTemplateNotFound` (was nil content and a nil error) and
  `List` an empty, non-nil list (was nil). **BREAKING:** `Calls` is a
  method that returns a copy of the recorded calls, in place of an
  exported field; the content given to `Register` is recorded as a
  copy; and the mock is safe for concurrent use.

### Added

- `template.ErrInvalidName`, `ErrInvalidExtension`, `ErrNilFS`,
  `ErrNotInitialized`, `ErrAlreadyInitialized`, `ErrRead`, `ErrList`,
  `ErrTooLarge` and `ErrInvalidMaxSize`, and `template.ErrorPrefix`.
- `template.CheckName`, `HasExtension`, `CheckExtensions`,
  `CheckMaxSize` and `TooLarge`, the checks every store applies, and
  `template.DefaultExtension` (`.html`) and `template.DefaultMaxSize`
  (16 MiB).
- `WithExtensions` and `WithMaxSize` options on the `memory`, `fs`,
  `dir` and `db` stores.
- `dir.ServiceName`, `dir.ErrEmptyDir` and `dir.ErrDirUnavailable`.
- `db.ServiceName`, `db.TableName`, `db.DefaultNamespace`,
  `db.ErrNilDatabase`, `db.ErrNilLogger`, `db.ErrNilMigrations`,
  `db.ErrInvalidNamespace`, `db.ErrUnsupportedDriver` and `db.ErrQuery`.
- `composite.ErrNilProvider`.

### Removed

- **BREAKING:** `template.IsNotFound`; use
  `errors.Is(err, template.ErrTemplateNotFound)`.
- **BREAKING:** the sqlc-generated packages `db/postgres`, `db/mysql`
  and `db/sqlite`; those directories now hold the migrations only.
- The `claude-skill/gas-template` skill file.

### Fixed

- `dir.Store` can be registered as `gas.TemplateProvider` (KI-033).
- The `db` MySQL migrations apply without `multiStatements=true` in the
  DSN, with gas/migrate v0.6.0, which sends a MySQL migration one
  statement at a time (KI-049).
- `composite` no longer hides a provider's failure as not found, and
  `dir.Store.List` no longer returns a partial or empty list when it
  cannot walk the directory.
- `RegisterFS` no longer leaves part of an `fs.FS` registered when one
  of its files fails.

### Security

- **Get reads template files only.** The `dir` and `fs` stores used to
  read any file under their root by name, so Get of `secret.env` on a
  directory store returned it; Get now refuses a name without one of
  the store's extensions, and `dir` follows no symlink to a file
  without one, such as `alias.html -> secret.env`.
- **No read takes the app's memory or blocks.** The maximum size bounds
  every read, and `dir` opens files without blocking and refuses
  anything but a regular file.
- **Errors leak nothing.** `db` errors give no driver text, and no
  error holds an absolute path outside a store's root.
- **Requires Go 1.27.3.** The `go` directive moves from 1.27 to 1.27.3,
  the first 1.27 release with the fix for GO-2026-6809 (CVE-2026-111943):
  `crypto/x509` now rejects intermediate certificates known to be
  compromised. A build with an older toolchain switches to 1.27.3 under
  `GOTOOLCHAIN=auto` and fails under `GOTOOLCHAIN=local`.

## [0.5.6] - 2026-10-19

The tags `template/v0.4.1` to `template/v0.5.6` shipped their changes under
`[Unreleased]`. This section holds them, and covers every change since
0.3.0, which was released from the separate
`github.com/gasmod/gas-template` repository.

### Changed

- **BREAKING: import path.** The module moved from
  `github.com/gasmod/gas-template` to `github.com/gasmod/gas/template`, in
  template/v0.4.1.
- **BREAKING: Go 1.27 is required.** The `go` directive moves from 1.26.1
  to 1.27, in template/v0.5.0, as core now uses generic methods.
- **BREAKING: the store constructors return an error.** `db.NewStore`,
  `dir.NewStore` and `fs.NewStore` now return `func(<dependencies>) (*Store,
  error)`, the `CONVENTIONS.md` shape. They cannot fail yet, so they return
  nil; the change is the shape. Registering them with
  `gas.WithSingletonService` is unchanged, since the container accepts a
  trailing error. Code that calls the returned function directly must take
  the second result. `memory.NewStore` and `composite.NewStore`, which apps
  build directly, are unchanged (gas#106).
- **BREAKING:** `db.Store` implements the context-aware `gas.Service`
  (`Init(ctx context.Context) error`, `Close(ctx context.Context) error`).
  Both are synchronous and do not use the context. `dir.Store.Close` is an
  `io.Closer` and keeps its `Close() error` signature.
- After `dir.Store.Close`, `Register`, `RegisterFS`, and a `Get` of an
  overlay template return `template.ErrClosed`; they used to keep working.
  `Close` also drops the overlay and waits for disk reads in flight, and a
  closed store cannot be opened again.

### Added

- `template.ErrClosed`, returned by every `dir.Store` operation after
  `Close`.

### Fixed

- `dir.Store` `Get` and `List` no longer panic with a nil pointer
  dereference after `Close`; they return `template.ErrClosed`. `Close` no
  longer races with concurrent reads.

## 0.3.0 - 2026-07-03

First open source release. Versions prior to 0.3.0 were developed in a private
repository; this entry summarizes the module as published.

### Added

- **`gas.TemplateProvider` implementation** across four backends — `memory`,
  `dir`, `db`, and `fs` — plus a `composite` store that chains them.
- **`memory.Store`** — an in-memory, concurrency-safe store for templates
  registered programmatically.
- **`dir.Store`** — reads `.html` templates from a directory on disk,
  sandboxed to the root via `os.Root`, with an in-memory overlay so
  `Register`/`RegisterFS` calls can add or override templates without
  touching disk.
- **`fs.Store`** — a read-only store backed by any `fs.FS` (e.g.
  `embed.FS`); `Register` and `RegisterFS` return `template.ErrReadOnly`,
  meant to be wrapped in a `composite.Store` with a writable provider for
  mutability.
- **`composite.Store`** — checks a writable provider first, then each
  reader in order, for both `Get` and a merged, deduplicated `List`;
  `Register`/`RegisterFS` delegate to the writable provider.
- **`db.Store`** — a database-backed store with `Get`, `List`, `Register`,
  `RegisterFS`, plus `Exists` and `Delete`, scoped by a configurable
  `WithNamespace` so multiple stores can share the `__gas_templates` table.
- **sqlc-generated multi-dialect adapters** for PostgreSQL, MySQL, and
  SQLite, selected automatically from the underlying database driver.
- **Migration registration** with `gas/migrate` — `db.Store.Init` registers
  the `__gas_templates` table migration for the resolved dialect.
- **`RegisterFS`** on every backend — recursively walks an `fs.FS` and
  registers every `.html` file found, keyed by its slash-separated path.
- **Sentinel errors** — `ErrTemplateNotFound` (with an `IsNotFound` helper)
  and `ErrReadOnly`.
- **`templatetest.MockTemplate`** — a configurable mock of
  `gas.TemplateProvider` with per-method function fields and a recorded
  `Calls` slice for assertions.
- **DI wiring** — `dir.NewStore`, `fs.NewStore`, and `db.NewStore` all
  return DI-injectable constructors for use with `gas.WithSingletonService`.

[Unreleased]: https://github.com/gasmod/gas/compare/template/v0.8.0...HEAD
[0.8.0]: https://github.com/gasmod/gas/releases/tag/template/v0.8.0
[0.7.0]: https://github.com/gasmod/gas/releases/tag/template/v0.7.0
[0.6.3]: https://github.com/gasmod/gas/releases/tag/template/v0.6.3
[0.6.2]: https://github.com/gasmod/gas/releases/tag/template/v0.6.2
[0.6.1]: https://github.com/gasmod/gas/releases/tag/template/v0.6.1
[0.6.0]: https://github.com/gasmod/gas/releases/tag/template/v0.6.0
[0.5.6]: https://github.com/gasmod/gas/releases/tag/template/v0.5.6
