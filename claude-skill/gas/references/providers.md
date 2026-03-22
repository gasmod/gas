# Provider Interfaces & Supporting Types

Read this file when you need the exact signatures for provider interfaces,
logging types, or supporting structs like Email and Migration.

## Table of Contents

- [DatabaseProvider](#databaseprovider)
- [CacheProvider](#cacheprovider)
- [JobQueueProvider](#jobqueueprovider)
- [EmailProvider](#emailprovider)
- [StorageProvider](#storageprovider)
- [ConfigProvider](#configprovider)
- [UIProvider](#uiprovider)
- [Logger](#logger)
- [LoggerContext & MutableLoggerContext](#loggercontext--mutableloggercontext)
- [LogEvent](#logevent)
- [MigrationManager](#migrationmanager)
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

Implementations unpack options via `gas.ApplyEnqueueOptions(opts)`.

### Job

```go
type Job struct {
	ID            string
	Body          []byte
	ReceiptHandle string            // opaque token used by Ack/Nack
	Attributes    map[string]string // provider-specific metadata
}
```

## EmailProvider

```go
type EmailProvider interface {
	Send(ctx context.Context, msg *Email) error
	SendFromTemplate(ctx context.Context, msg *TemplatedEmail) error
}
```

## StorageProvider

```go
type StorageProvider interface {
	Upload(ctx context.Context, key string, data io.Reader) error
	Download(ctx context.Context, key string) (io.ReadCloser, error)
	Delete(ctx context.Context, key string) error
	PresignURL(ctx context.Context, key string, expires time.Duration) (string, error)
}
```

## ConfigProvider

```go
type ConfigProvider interface {
	SetDefault(key string, value any)
	SetDefaults(values any) error
	Set(key string, value any)
	Bind(dest any, options ...config.BindOption) error
	Get(key string) any
	Find(key string) (value any, exist bool)
	Values() map[string]any
}
```

## UIProvider

```go
type UIProvider interface {
	Render(w http.ResponseWriter, name string, data any) error
	RenderWithStatus(w http.ResponseWriter, status int, name string, data any) error
	RenderFragment(w http.ResponseWriter, name string, data any) error // renders without layout wrapper, useful for HTMX
	RegisterTemplate(name string, content []byte)
	RegisterTemplatesFS(fsys fs.FS) error
	RegisterFuncs(funcs template.FuncMap)
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
	SetBaseFields() MutableLoggerContext
	Flush()
}
```

Context helpers for storing/retrieving a Logger in `context.Context`:

```go
gas.WithLogger(ctx context.Context, l Logger) context.Context
gas.LoggerFromContext(ctx context.Context) Logger // returns nil if absent
```

### With() vs SetBaseFields()

`With()` always branches — it returns a `LoggerContext` whose `Logger()`
produces a NEW Logger instance. Use it when you want a sub-logger scoped to
a specific code path.

`SetBaseFields()` mutates the receiver in place via `Apply()`. Use it in
request-scoped middleware where one Logger instance is shared across the
whole request and you want every subsequent log call to carry fields like
request_id, user_id, trace_id automatically — without threading a new Logger
reference around.

```go
// Typical middleware pattern:
logger := gas.MustResolveFromRequestScope[gas.Logger](r)
logger.SetBaseFields().Str("request_id", reqID).Str("user_id", userID).Apply()
// All log calls for the rest of this request now carry request_id and user_id.
```

## LoggerContext & MutableLoggerContext

Both share the same field methods. `LoggerContext` (from `With()`) produces a
new Logger via `.Logger()`. `MutableLoggerContext` (from `SetBaseFields()`)
mutates the originating Logger via `.Apply()`.

Field methods (available on both):

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

Terminals:
- `LoggerContext.Logger() Logger` — returns a new branched Logger
- `MutableLoggerContext.Apply()` — mutates the originating Logger in place

## LogEvent

A single structured log entry. Same field methods as LoggerContext, finalized
with `Send()`.

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
	Register(service string, m Migration)
	RegisterSlice(service string, migrations []Migration)
	RegisterFS(service string, fsys fs.FS) error
	RunPending() error
	Down(n int) error
}
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
	SubjectTemplate string
	TextTemplate    string
	HTMLTemplate    string
	Data            any
	Email
}

type Migration struct {
	Version, Service, Description, Up, Down string
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
	Body          []byte
	ReceiptHandle string            // opaque token used by Ack/Nack
	Attributes    map[string]string // provider-specific metadata
}

type EnqueueOption func(*enqueueOptions) // see WithDelay, WithGroupID, WithDedupeID, WithJobAttributes

type RegisteredRoute struct {
	Method     string
	Path       string
	Middleware []string
}
```
