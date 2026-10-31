---
name: gas-template
description: >
  Reference documentation for the gas/template Go package
  (github.com/gasmod/gas/template), template storage and retrieval for the Gas
  ecosystem. Use this skill when writing, reviewing, or debugging Go code that
  uses gas/template for storing and retrieving raw template content with
  in-memory, directory, database, or composite backends. Covers the memory, dir,
  db, and composite sub-packages, templatetest mock, gas.TemplateProvider
  implementation, sentinel errors, DI wiring, multi-database support
  (PostgreSQL, MySQL, SQLite), namespace isolation, embedded fs.FS registration,
  and migration registration with gas/migrate. Make sure to use this skill
  whenever working with template storage in the Gas ecosystem, even if the user
  doesn't explicitly mention gas/template; any code that imports
  gasmod/gas/template or references gas.TemplateProvider should trigger this
  skill.
---

# Gas Template Package Reference

Template storage service for the Gas ecosystem. The stores are byte storage
behind `gas.TemplateProvider`; they never parse or execute a template, which
gas/ui and gas/email do. Each store lives in its own package.

```
import template "github.com/gasmod/gas/template"
import "github.com/gasmod/gas/template/memory"
import "github.com/gasmod/gas/template/dir"
import tmplfs "github.com/gasmod/gas/template/fs"
import tmpldb "github.com/gasmod/gas/template/db"
import "github.com/gasmod/gas/template/composite"
import "github.com/gasmod/gas/template/templatetest"
```

## Backends

| Backend    | Package                  | Service name       | Use case                                         |
|------------|--------------------------|--------------------|--------------------------------------------------|
| Memory     | `gas/template/memory`    | none               | Development, tests, content built at run time    |
| Directory  | `gas/template/dir`       | `gas/template/dir` | Files on disk through `os.Root`, overlay in RAM  |
| FS         | `gas/template/fs`        | none               | Read-only over any `fs.FS` / `embed.FS`          |
| Database   | `gas/template/db`        | `gas/template/db`  | Edited at run time (PostgreSQL, MySQL, SQLite)   |
| Composite  | `gas/template/composite` | none               | A writable store over read-only fallbacks        |

Every `NewStore` returns a constructor in the shape the container expects
(`func(...) (*Store, error)`), including memory, fs and composite. The dir and
db stores also implement `gas.Service` (Name, Init, Close).

## TemplateProvider Interface

Defined in the gas core package:

```go
type TemplateProvider interface {
    Get(ctx context.Context, name string) ([]byte, error)
    List(ctx context.Context) ([]string, error)
    Register(ctx context.Context, name string, content []byte) error
    RegisterFS(ctx context.Context, fsys fs.FS) error
}
```

## Names, Extensions, Size

- A name is a slash path that `fs.ValidPath` accepts, valid UTF-8, no NUL, no
  backslash, and ending in one of the store's extensions. `pages/home.html`
  is a name; `/pages/home.html`, `./home.html`, `pages/../home.html` are not.
  Every method checks the name first and returns an error wrapping
  `ErrInvalidName` (the reason, never the name).
- Names are compared byte for byte in every store, so `Home.html` and
  `home.html` are two templates, even on a case-insensitive file system.
- Extensions default to `template.DefaultExtension` (`.html`). Each store has
  `WithExtensions(exts ...string)`, such as `".html", ".txt"` for email
  bodies. Get, List, Register and RegisterFS all apply the set: Get of
  `secret.env` is `ErrInvalidName`, and List never returns it.
- Every store but composite and templatetest holds templates of at most
  `template.DefaultMaxSize` (16 MiB) unless `WithMaxSize(n int64)` gives
  another (at least 1). Larger content is `ErrTooLarge`, on Register,
  RegisterFS and Get.
- Helpers: `template.CheckName(name, exts)`, `template.HasExtension(name, exts)`,
  `template.CheckExtensions(exts)`, `template.CheckMaxSize(n)`,
  `template.TooLarge(name, max)`.

## Contract (every store)

- `Get` returns a copy of the content, or an error wrapping
  `ErrTemplateNotFound`.
- `List` returns every name Get can read, once, sorted in byte order; an
  empty store returns an empty, non-nil list.
- `Register` stores a copy, replacing a template of that name. Nil content is
  stored as empty.
- `RegisterFS` walks an fs.FS from its root and registers each regular file
  with one of the store's extensions, under its path. It reads every file
  before it registers any, so a failure or canceled context registers
  nothing. Symlinks and other files are skipped; a file with a matching
  extension but a bad name is an error. A nil fs.FS is `ErrNilFS`.
- A store with a lifecycle (dir, db) returns `ErrNotInitialized` before its
  first successful Init, `ErrClosed` after Close until Init runs again, and
  `ErrAlreadyInitialized` from Init while open.

## Sentinel Errors

The root `template` package defines (test with `errors.Is`):

```go
template.ErrTemplateNotFound   // the store has no template of the name
template.ErrInvalidName        // CheckName or a store's limit refused a name
template.ErrInvalidExtension   // bad extension set given to a store
template.ErrReadOnly           // Register/RegisterFS on a read-only store (fs)
template.ErrNilFS              // nil fs.FS given to a store or RegisterFS
template.ErrNotInitialized     // lifecycle store used before Init
template.ErrAlreadyInitialized // Init on an open store
template.ErrClosed             // lifecycle store used after Close
template.ErrRead               // a file store could not read a template
template.ErrList               // a file store could not walk its files
template.ErrTooLarge           // over the store's maximum size
template.ErrInvalidMaxSize     // WithMaxSize under 1 byte
```

There is no `IsNotFound` helper; use `errors.Is(err, template.ErrTemplateNotFound)`.
Every error starts with `template.ErrorPrefix` (`"template: "`) once.

## Memory Backend

```go
func NewStore(opts ...Option) func() (*Store, error) // WithExtensions, WithMaxSize
```

A map guarded by a read-write lock; no lifecycle. RegisterFS stores all files
under one hold of the lock, so a reader sees all or none.

## Directory Backend

```go
func NewStore(path string, opts ...Option) func() (*Store, error) // WithExtensions, WithMaxSize
```

A `gas.Service` named `dir.ServiceName` (`"gas/template/dir"`). The
constructor opens nothing; `Init` opens the directory with `os.OpenRoot` and
returns an error wrapping `dir.ErrDirUnavailable` on failure (and can be
retried). An empty path is `dir.ErrEmptyDir`.

```go
gas.WithSingletonService[gas.TemplateProvider](dir.NewStore("templates"))
```

By hand, call Init and Close yourself:

```go
store, err := dir.NewStore("templates")()
if err != nil {
    return err
}
if err := store.Init(ctx); err != nil {
    return err
}
defer store.Close(ctx)
b, err := store.Get(ctx, "pages/home.html")
```

### Behavior

- **No cache:** every Get and List reads the directory afresh, so edits show
  at once.
- **Sandboxed:** reads go through `os.Root`; files and directories open
  without blocking, and a FIFO, device or file swapped in meanwhile is
  `ErrRead`, never a read that waits.
- **Symlinks:** a symlink to a file is followed only when every target is a
  relative path inside the directory that ends in one of the extensions and
  passes through no directory symlink; otherwise it is neither listed nor
  read. Directory symlinks are never walked. Hard links cannot be told apart
  from files: keep the directory unwritable by anyone untrusted.
- **Overlay:** Register and RegisterFS keep templates in memory over the
  directory (a registered name hides the file); the directory is never
  written. Close drops the overlay.
- **List** fails with `ErrList` (never a partial list) when a directory
  cannot be read or the root was removed or moved.
- **One per container:** the worker tracks services by name, and every dir
  Store is named `gas/template/dir`, so only the first registered one is
  closed at shutdown. For more directories, chain fs stores over
  `os.Root`'s FS with composite.

## FS Backend

```go
func NewStore(fsys fs.FS, opts ...Option) func() (*Store, error) // WithExtensions, WithMaxSize
```

Read-only over any `fs.FS`. A nil fs.FS is `ErrNilFS`. `Register` and
`RegisterFS` return `template.ErrReadOnly`; wrap it in a `composite.Store`
with a writable provider for overrides. Symlink entries are neither listed
nor read. Over a directory on disk prefer the dir store, or pass
`os.Root`'s FS rather than `os.DirFS`, which follows symlinks out of its
directory.

```go
//go:embed templates
var embedded embed.FS

sub, err := fs.Sub(embedded, "templates")
if err != nil {
    return err
}
store, err := tmplfs.NewStore(sub)()
if err != nil {
    return err
}
content, err := store.Get(ctx, "pages/home.html") // templates/pages/home.html
```

## Database Backend

### Constructor

```go
func NewStore(opts ...Option) func(gas.DatabaseProvider, gas.Logger, gas.MigrationManager) (*Store, error)
```

The constructor checks its dependencies (`ErrNilDatabase`, `ErrNilLogger`,
`ErrNilMigrations`) and options and touches no database.

### Options

| Option                     | Description                                                   |
|----------------------------|---------------------------------------------------------------|
| `WithNamespace(ns string)` | Scope rows to a namespace (default `db.DefaultNamespace`, `"default"`); 1 to 255 characters, else `ErrInvalidNamespace` |
| `WithExtensions(exts...)`  | Extension set (default `.html`)                               |
| `WithMaxSize(n int64)`     | Maximum template size (default 16 MiB)                        |

### Lifecycle (gas.Service)

| Method  | Description                                                                  |
|---------|------------------------------------------------------------------------------|
| `Name`  | Returns `db.ServiceName`, `"gas/template/db"`                                |
| `Init`  | Picks the dialect from `Driver()`, takes the provider's DB, registers the migrations; runs no statement. Logs `store initialized` |
| `Close` | Marks the store closed; closes nothing (the DB is the provider's). Init may run again |

### Supported Drivers

`postgres`, `mysql`, `sqlite` (the names gas/database reports). Any other,
including the empty driver gas/database reports before its own Init, is
`db.ErrUnsupportedDriver`. MySQL must be 8.0.19 or later; MariaDB is not
supported.

### Migrations

Init registers two migrations under `gas/template/db`: `20260322235959`
(create templates table) and `20261031120000` (exact template names: drops the
namespace index; on MySQL switches names to `utf8mb4_0900_bin` and content to
`LONGBLOB`). Every store registers the same two, so several stores share one
table. Migrations run after every service's Init, so until then methods fail
with `db.ErrQuery`. Seed templates from a ready function:

```go
gas.WithReadyFunc(func(c *gas.ServiceContainer) error {
    store, err := c.Resolve[gas.TemplateProvider]() // the type the store is registered under
    if err != nil {
        return err
    }
    return store.RegisterFS(context.Background(), emailTemplates)
})
```

### Behavior

- Table `db.TableName`, `__gas_templates`: `namespace`, `name`, `content`,
  unique on `(namespace, name)`, plus `id`, `created_at`, `updated_at`.
- Names are at most 255 characters on every dialect (`ErrInvalidName`
  before any statement) and are exact (case and accents matter) on every
  dialect.
- `Get` has the database send no content over the maximum (`ErrTooLarge`).
- `List` sorts in Go, byte order, and leaves out rows whose name the store
  would refuse (such as another extension set's rows in the namespace).
- `Register` upserts in one statement; `RegisterFS` upserts every file in one
  transaction (all or none).
- `Exists(ctx, name) (bool, error)` reports whether a row is there.
- `Delete(ctx, name) error` removes it, or returns an error wrapping
  `ErrTemplateNotFound`.
- A failed statement is `db.ErrQuery`, which never wraps the driver's error:
  it names the operation and template and gives only a code (`postgres
  error 42P01`, `mysql error 1146`, `sqlite error 1`), or the provider's
  `SafeError` result, or a kept context / `sql.ErrConnDone` / `sql.ErrTxDone`.

### Namespaces and several stores

Stores of different namespaces share the table. The container builds one
service per registered type, so a second store needs a type of its own, and
since every db Store is named `gas/template/db`, only the first is closed at
shutdown (Close releases nothing, so this costs nothing).

## Composite Backend

```go
func NewStore(writable gas.TemplateProvider, readers ...gas.TemplateProvider) func() (*Store, error)
```

A nil provider (nil interface or typed nil pointer) is `composite.ErrNilProvider`.

- `Get` asks the writable provider, then each reader in order. It moves on
  only when a provider returns `ErrTemplateNotFound` or `ErrInvalidName`;
  any other error (closed store, database down) is returned at once,
  unchanged, so a failure is never hidden by a fallback.
- `List` merges all providers' names, once each, sorted. Any provider error
  stops it; there is no partial list.
- `Register` and `RegisterFS` go to the writable provider.
- No lifecycle: it neither initializes nor closes its providers. Register
  those as services of their own types so the container does.

## Test Mock

```go
type MockTemplate struct {
    GetFn        func(ctx context.Context, name string) ([]byte, error)
    ListFn       func(ctx context.Context) ([]string, error)
    RegisterFn   func(ctx context.Context, name string, content []byte) error
    RegisterFSFn func(ctx context.Context, fsys fs.FS) error
}
```

With a field nil, the method behaves as an empty store: Get returns an error
wrapping `ErrTemplateNotFound`, List an empty list, Register and RegisterFS
nil. Each call is recorded (arguments after the context; Register's content
copied).

| Method                  | Description                               |
|-------------------------|-------------------------------------------|
| `Calls() []Call`        | Copy of the recorded calls, in order      |
| `CallCount(method) int` | Count calls by method name (e.g. `"Get"`) |
| `Reset()`               | Forget all recorded calls                 |

Methods may run from several goroutines; setting an `Fn` field while a
method runs is a data race.

## DI Wiring Patterns

The container resolves by exact registered type, so register the store
consumers use under `gas.TemplateProvider`, and the db store's dependencies
under `gas.DatabaseProvider` and `gas.MigrationManager` (`database.New` also
needs a registered `gas.ConfigProvider` and `gas.Logger`).

### Memory backend (dev/test)

```go
app := gas.NewApp(
    gas.WithSingletonService[gas.TemplateProvider](memory.NewStore()),
)
```

### Database backend (production)

```go
app := gas.NewApp(
    gas.WithSingletonService[gas.DatabaseProvider](database.New()),
    gas.WithSingletonService[gas.MigrationManager](migrate.New()),
    gas.WithSingletonService[gas.TemplateProvider](
        tmpldb.NewStore(tmpldb.WithNamespace("emails")),
    ),
)
```

### Composite (database writable + embedded fallback)

```go
app := gas.NewApp(
    gas.WithSingletonService[gas.DatabaseProvider](database.New()),
    gas.WithSingletonService[gas.MigrationManager](migrate.New()),
    gas.WithSingletonService[*tmpldb.Store](tmpldb.NewStore()),
    gas.WithSingletonService[gas.TemplateProvider](
        func(dbStore *tmpldb.Store) (*composite.Store, error) {
            fsStore, err := tmplfs.NewStore(sub)()
            if err != nil {
                return nil, err
            }
            return composite.NewStore(dbStore, fsStore)()
        },
    ),
)
```

### Consuming via gas.TemplateProvider

```go
type Service struct {
    templates gas.TemplateProvider
}

func (s *Service) WelcomeTemplate(ctx context.Context) ([]byte, error) {
    content, err := s.templates.Get(ctx, "emails/welcome.html")
    if errors.Is(err, template.ErrTemplateNotFound) {
        return nil, gas.NotFound("template not found").WithCause(err)
    }
    return content, err
}
```

### Testing with MockTemplate

```go
mock := &templatetest.MockTemplate{
    GetFn: func(ctx context.Context, name string) ([]byte, error) {
        return []byte("<h1>Hello</h1>"), nil
    },
}
// inject mock as gas.TemplateProvider; assert with mock.CallCount("Get")
```

## Security

A stored template is code: gas/ui runs it with html/template and gas/email
runs subjects and text bodies with text/template, which escapes nothing.
Never let users write templates, and never take a template name from a
request; map user input to a fixed set of names.
