# Provider Interfaces & Supporting Types

Read this file when you need the exact signatures for provider interfaces,
logging types, or supporting structs like Email and Migration.

## Table of Contents

- [DatabaseProvider](#databaseprovider)
- [CacheProvider](#cacheprovider)
- [JobQueueProvider](#jobqueueprovider)
- [EmailProvider](#emailprovider)
- [ConfigProvider](#configprovider)
- [TemplateProvider](#templateprovider)
- [HealthProvider & HealthReporter](#healthprovider--healthreporter)
- [ReadyProvider & ReadyReporter](#readyprovider--readyreporter)
- [Logger](#logger)
- [LoggerContext](#loggercontext)
- [LogEvent](#logevent)
- [MigrationManager](#migrationmanager)
- [Principal & Metadata](#principal--metadata)
- [Supporting Types](#supporting-types)

---

## DatabaseProvider

```go
type DatabaseProvider interface {
	DB() *sql.DB
	Driver() string
	Ping(ctx context.Context) error
	Query(ctx context.Context, query string, args ...any) (Rows, error)
	Exec(ctx context.Context, query string, args ...any) (Result, error)
	BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)
	WithTx(ctx context.Context, opts *sql.TxOptions, fn func(*sql.Tx) error) (err error)
}
```

## CacheProvider

```go
type CacheProvider interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	Delete(ctx context.Context, key string) error
	Exists(ctx context.Context, key string) (bool, error)
}
```

## JobQueueProvider

Pull-based async job/message queue abstraction. Consumers call `Dequeue` in
their own worker loop and acknowledge results with `Ack`/`Nack`.

```go
type JobQueueProvider interface {
	Enqueue(ctx context.Context, queue string, payload []byte, opts ...EnqueueOption) error
	Dequeue(ctx context.Context, queue string, maxMessages int, wait time.Duration) ([]Job, error)
	Ack(ctx context.Context, queue string, job Job) error
	Nack(ctx context.Context, queue string, job Job) error
}
```

### EnqueueOption

Functional options for `Enqueue`:

```go
gas.WithDelay(d time.Duration)              // initial visibility delay
gas.WithGroupID(id string)                  // FIFO ordering (SQS: MessageGroupId)
gas.WithDedupeID(id string)                 // deduplication (SQS: MessageDeduplicationId)
gas.WithJobAttributes(attrs map[string]string) // provider-specific metadata
```

Implementations unpack options via
`gas.ApplyEnqueueOptions(opts) (delay, groupID, dedupeID, attrs)`, each the zero
value when no option sets it.

### Job

```go
type Job struct {
	ID            string
	ReceiptHandle string            // opaque token used by Ack/Nack
	Attributes    map[string]string // provider-specific metadata
	Body          []byte
}
```

## EmailProvider

```go
type EmailProvider interface {
	Send(ctx context.Context, msg *Email) error
	SendFromTemplate(ctx context.Context, msg *TemplatedEmail) error
}
```

## Object storage

Object storage is not a core interface. `storage.Provider` and
`storage.Presigner` live in `github.com/gasmod/gas/storage`; see the
gas-storage skill for their signatures, input types, and sentinel errors.

## ConfigProvider

```go
type ConfigProvider interface {
	SetDefault(key string, value any) error // sets key unless the config holds one
	SetDefaults(values any) error           // struct or map, as SetDefault per key
	Set(key string, value any) error
	Bind(dest any) error                    // fills a struct pointer and validates it
	Get(key string) any                     // nil if absent
	Find(key string) (value any, exist bool)
	Values() map[string]any                 // a copy of every value
}
```

Core defines it so the root module imports no gas module; `*config.Config`
implements it structurally. There are no bind options: `Bind` always
validates. Loading sources is done on the concrete config before registering
it and is not part of the interface (see the gas-config skill).

An App binds its `gas.Config` through it at `Start`: `Env` from the key
`gasenv`, where the config module's `EnvExtension` stores what it resolved from
`GAS_ENV`. A provider that resolves the environment another way must store it
at `gasenv`.

## TemplateProvider

Abstracts template storage and retrieval. Implementations may be backed by a
filesystem, database, memory, or any combination. Consumers such as
the ui module (HTML rendering) and `EmailProvider` (email templating) resolve
raw template content through this interface. HTML rendering is not a core
interface: `ui.Provider` lives in the gas/ui module (see the gas-ui skill).

```go
type TemplateProvider interface {
	Get(ctx context.Context, name string) ([]byte, error)              // raw template content by name
	List(ctx context.Context) ([]string, error)                        // all available template names
	Register(ctx context.Context, name string, content []byte) error   // add or replace a template
	RegisterFS(ctx context.Context, fsys fs.FS) error                  // walk an fs.FS and register all templates found
}
```

## HealthProvider & HealthReporter

Services opt into health reporting by implementing `HealthReporter`. The
`Worker` satisfies `HealthProvider`: it walks all active services and
concurrently invokes `CheckHealth` on any that implement `HealthReporter`,
returning a map of service name to result (nil = healthy); the results of two
services with one name are joined. A panic inside a reporter is recovered and
reported as an error wrapping `gas.ErrPanic`. `HealthProvider` and
`ReadyProvider` are pre-registered in the DI container: resolve the narrow
interface instead of depending on `*Worker`. Core registers no health routes.

```go
// Implemented by services that can report their own health.
// Nil means healthy; non-nil describes the failure.
type HealthReporter interface {
	CheckHealth(ctx context.Context) error
}

// Aggregator across all active HealthReporters. Worker satisfies this.
// Keys are service names; non-reporter services are omitted.
type HealthProvider interface {
	CheckHealth(ctx context.Context) map[string]error
}
```

## ReadyProvider & ReadyReporter

Same shape as health, but for readiness (a service can be healthy yet not
ready: warming caches, pending migrations, etc.).

```go
type ReadyReporter interface {
	CheckReady(ctx context.Context) error
}

type ReadyProvider interface {
	CheckReady(ctx context.Context) map[string]error
}
```

## Logger

```go
type Logger interface {
	Trace(msg string) LogEvent
	Debug(msg string) LogEvent
	Info(msg string) LogEvent
	Warn(msg string) LogEvent
	Error(msg string) LogEvent
	With() LoggerContext
	Flush()
}
```

A Worker flushes its Logger at the end of `Shutdown`, after the final
`shutdown complete` line, and closes it last when it is a service.

`gas.NopLogger` discards everything; its zero value is ready to use and there
is no constructor function (`&gas.NopLogger{}`).

Context helpers for storing/retrieving a Logger in `context.Context`:

```go
gas.WithLogger(ctx context.Context, l Logger) context.Context
gas.LoggerFromContext(ctx context.Context) Logger // returns nil if absent
```

### Request fields

Loggers are never changed in place. `With()` returns a `LoggerContext` whose
`Logger()` produces a NEW Logger carrying the fields. For request fields,
middleware derives that logger and stores it on the request context; code
downstream reads it back:

```go
// Typical middleware pattern:
logger := base.With().Str("user_id", userID).Logger()
next.ServeHTTP(w, r.WithContext(gas.WithLogger(r.Context(), logger)))

// In a handler or service:
gas.LoggerFromContext(r.Context()).Info("note created").Send()
```

`gas.RequestLogger` already does this for `request_id`, and for a scoped
request-logger type it also puts the result in the request scope, so code
that takes that type as a dependency gets the fields.

## LoggerContext

Returned by `With()`. Field methods:

```go
Str(key, val string)
Int(key string, val int)
Int64(key string, val int64)
Float64(key string, val float64)
Bool(key string, val bool)
Err(key string, val error)
Duration(key string, val time.Duration)
Any(key string, val any)
```

Terminal: `Logger() Logger` returns the new sub-logger.

## LogEvent

A single structured log entry. Same field methods as LoggerContext, finalized
with `Send()`. Call `Send` once; do not use an event after `Send` or from two
goroutines.

```go
type LogEvent interface {
	Str(key, val string) LogEvent
	Int(key string, val int) LogEvent
	Int64(key string, val int64) LogEvent
	Float64(key string, val float64) LogEvent
	Bool(key string, val bool) LogEvent
	Err(key string, val error) LogEvent
	Duration(key string, val time.Duration) LogEvent
	Any(key string, val any) LogEvent
	Send()
}
```

## MigrationManager

```go
type MigrationManager interface {
	Service
	Register(service Service, m Migration)
	RegisterSlice(service Service, migrations []Migration)
	RegisterFS(service Service, fsys fs.FS) error
	RunPendingContext(ctx context.Context) error // apply unapplied migrations in global version order
	DownContext(ctx context.Context, n int) error // reverse the last n applied
}
```

`Worker.Start` resolves exactly `gas.MigrationManager` and calls
`RunPendingContext` with its own ctx, after `SystemAllServicesInitialized`
and before the ready hooks; a failure fails `Start`
(`gas: run migrations: ...`). Services register their migrations in `Init`.
A dirty migration blocks the run until it is resolved.

## Principal & Metadata

Principal represents an authenticated identity. PrincipalMetadata provides
read-only access to arbitrary key-value metadata attached at construction time.

```go
type Principal interface {
	Subject() string        // stable user identifier
	Scheme() string         // auth method: "jwt", "session", "apikey"
	CredentialID() string   // specific credential: session ID, JWT jti, API key ID
	Metadata() PrincipalMetadata
}

type PrincipalMetadata interface {
	Value(key string) any
}
```

`BasePrincipalMetadata` is a map-based implementation of `PrincipalMetadata`:

```go
type BasePrincipalMetadata map[string]any
```

Context helpers:

```go
gas.WithPrincipal(ctx context.Context, p Principal) context.Context
gas.PrincipalFromContext(ctx context.Context) (Principal, bool)  // false if absent, nil, or a typed-nil pointer
```

In a handler, `ctx.Principal()` returns the principal, or a 401 `*gas.Error`
with `gas.CodeUnauthorized` when none is present, so the handler can return
the error directly.

The `Authenticator`, `Authorizer`, and `PrincipalRevoker` interfaces live in
the auth module (`github.com/gasmod/gas/auth`), not in core. See the gas-auth
skill.

Type-safe metadata access:

```go
gas.MetadataValue[T any](m PrincipalMetadata, key string) (T, bool)
```

## Supporting Types

```go
type Email struct {
	From     string
	ReplyTo  string
	Subject  string
	TextBody string
	HTMLBody string
	Headers  map[string]string
	To       []string
	Cc       []string
	Bcc      []string
}

type TemplatedEmail struct {
	Data            any
	SubjectTemplate string
	TextTemplate    string
	HTMLTemplate    string
	Email           // embedded; its fields are promoted
}

type Migration struct {
	Version     string
	Service     Service // owning service; gas/migrate's Register* methods set it
	Description string
	Up          string
	Down        string
}

type Rows interface {
	Next() bool
	Scan(dest ...any) error
	Close() error
	Err() error
}

type Result interface {
	RowsAffected() (int64, error)
	LastInsertId() (int64, error)
}

type Job struct {
	ID            string
	ReceiptHandle string            // opaque token used by Ack/Nack
	Attributes    map[string]string // provider-specific metadata
	Body          []byte
}

type EnqueueOption func(*enqueueOptions) // see WithDelay, WithGroupID, WithDedupeID, WithJobAttributes

type RegisteredRoute struct {
	Method     string
	Path       string   // full path
	Middleware []string // names of the named middleware that apply, top-level first
}
```
