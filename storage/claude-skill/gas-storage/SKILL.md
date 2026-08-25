---
name: gas-storage
description: >
  Reference documentation for the gas/storage Go package
  (github.com/gasmod/gas/storage) — the object storage service for the Gas
  ecosystem. Use this skill when writing, reviewing, or debugging Go code that
  uses gas/storage for file/object storage with AWS S3 or S3-compatible
  backends (MinIO, LocalStack, DigitalOcean Spaces). Covers the s3 sub-package,
  storagetest mock, gas.StorageProvider implementation, sentinel errors, DI
  wiring, configuration binding, presigned URLs, and direct S3 client access. Make sure to use this skill
  whenever working with object storage in the Gas ecosystem, even if the user
  doesn't explicitly mention gas/storage — any code that imports
  gasmod/gas/storage or references gas.StorageProvider should trigger this skill.
---

# Gas Storage Package Reference

Object storage service for the Gas ecosystem. Provides a `gas.StorageProvider`
implementation backed by AWS S3 and S3-compatible services.

```
import storage "github.com/gasmod/gas/storage"
import storages3 "github.com/gasmod/gas/storage/s3"
import "github.com/gasmod/gas/storage/storagetest"
```

## Backends

| Backend | Package             | Service name     | Use case                                     |
|---------|---------------------|------------------|----------------------------------------------|
| S3      | `gas/storage/s3`    | `gas/storage/s3` | Production, any S3-compatible object storage |

Implements `gas.Service` and `gas.StorageProvider`.

## StorageProvider Interface

Defined in the gas core package:

```go
type StorageProvider interface {
    Upload(ctx context.Context, key string, data io.Reader, opts ...StorageOption) error
    Download(ctx context.Context, key string, opts ...StorageOption) (*StorageObject, error)
    Delete(ctx context.Context, key string, opts ...StorageOption) error
    Head(ctx context.Context, key string, opts ...StorageOption) (*ObjectInfo, error)
    PresignDownloadURL(ctx context.Context, key string, expires time.Duration, opts ...StorageOption) (string, error)
    PresignUploadURL(ctx context.Context, key string, expires time.Duration, opts ...StorageOption) (string, error)
}
```

### StorageObject

Returned by `Download`. Carries the response body alongside metadata the
backend provides.

```go
type StorageObject struct {
    Body        io.ReadCloser
    ContentType string
    Size        int64
    Metadata    map[string]string // provider-specific extras
}
```

### ObjectInfo

Returned by `Head`. Carries object metadata without the body.

```go
type ObjectInfo struct {
    ContentType  string
    Size         int64
    Metadata     map[string]string
    LastModified time.Time
}
```

### StorageOption

Functional options for all `StorageProvider` methods:

```go
gas.InBucket(name string)                    // override the default bucket for this operation
gas.WithContentType(ct string)               // set content type on Upload
gas.WithMetadata(m map[string]string)        // attach arbitrary key-value metadata on Upload
```

Implementations unpack options via `gas.ApplyStorageOptions(opts)`.

## Sentinel Errors

The root `storage` package defines sentinel errors:

```go
storage.ErrKeyNotFound // Download or Head returns this when the key does not exist
storage.ErrClosed      // returned when an operation is attempted on a closed service
```

## S3 Backend

### Constructor

```go
func New(opts ...Option) func(gas.ConfigProvider, gas.Logger) *Service
```

`New` captures options and returns a DI-injectable constructor. The returned
func receives `gas.ConfigProvider` and `gas.Logger` from the DI container.

### Options

| Option                    | Description                                                 |
|---------------------------|-------------------------------------------------------------|
| `WithConfig(cfg *Config)` | Set configuration explicitly (skips config binding from DI) |

### Lifecycle (gas.Service)

| Method  | Signature   | Description                                            |
|---------|-------------|--------------------------------------------------------|
| `Name`  | `() string` | Returns `"gas/storage/s3"`                             |
| `Init`  | `() error`  | Validates config, loads AWS config, creates S3 client  |
| `Close` | `() error`  | Marks service as closed                                |

Also implements `gas.ReadyReporter`:

| Method       | Signature                      | Description                                                            |
|--------------|--------------------------------|------------------------------------------------------------------------|
| `CheckReady` | `(ctx context.Context) error`  | Issues `HeadBucket` against the configured default bucket; returns nil |

- Returns `storage.ErrClosed` if the service is closed, or an error if the
  client is not initialized.
- When `Storage.Bucket` is empty (per-call bucket mode), `CheckReady` returns
  `nil` as soon as `Init` has completed — there is no single bucket to probe.
- **IAM:** the caller's principal MUST have `s3:ListBucket` on the configured
  bucket. Without it, `HeadBucket` returns 403 and readiness probes fail.
  Narrowly-scoped S3 policies that only grant `s3:GetObject`/`s3:PutObject`
  must be extended with `s3:ListBucket` on the bucket ARN.

### Behavior

- **Upload:** Stores an object in S3 using `PutObject`. Honors
  `gas.WithContentType` and `gas.WithMetadata` options. Returns
  `storage.ErrClosed` if the service is closed.
- **Download:** Retrieves an object from S3 using `GetObject`. Returns a
  `*gas.StorageObject` with `Body`, `ContentType`, `Size`, and `Metadata`.
  Returns `storage.ErrKeyNotFound` if the key does not exist (detects
  `types.NoSuchKey`). Returns `storage.ErrClosed` if the service is closed.
- **Delete:** Removes an object from S3 using `DeleteObject`. Deleting a
  non-existent key does not error. Returns `storage.ErrClosed` if closed.
- **Head:** Retrieves object metadata (content type, size, last modified,
  metadata) without downloading the body. Returns `storage.ErrKeyNotFound` if
  the key does not exist. Returns `storage.ErrClosed` if the service is closed.
- **PresignDownloadURL:** Generates a presigned GET URL valid for the given
  duration. Returns `storage.ErrClosed` if the service is closed.
- **PresignUploadURL:** Generates a presigned PUT URL valid for the given
  duration. Honors `gas.WithContentType` and `gas.WithMetadata` options.
  Returns `storage.ErrClosed` if the service is closed.
- **Bucket resolution:** All methods accept `gas.InBucket(name)` to override
  (or supply) the bucket for a single operation. The default
  `Config.Storage.Bucket` is optional — if it is empty, every call must pass
  `gas.InBucket(...)` or the operation returns `storage.ErrBucketRequired`.
- **Credentials:** When `AccessKeyID` is set, static credentials are used.
  When empty, the AWS default credential chain is used (environment variables,
  IAM roles, etc.).
- **Custom endpoint:** When `Endpoint` is set, path-style addressing is
  automatically enabled for S3-compatible services.

### Direct Client Access

```go
func (s *Service) Client() *s3.Client
```

For operations beyond `StorageProvider` (multipart uploads, bucket operations,
object metadata), define a local interface and type-assert:

```go
type S3Provider interface {
    Client() *s3.Client
}
```

### Config

```go
type Config struct {
    env.WithGasEnv
    Storage Settings
}

type Settings struct {
    Region            string        // required
    Bucket            string        // optional default; if unset, every call must pass gas.InBucket(...)
    AccessKeyID       string        // empty = use default credential chain
    SecretAccessKey   string        // AWS secret access key
    Endpoint          string        // optional; enables path-style when set
}

func DefaultConfig() *Config
func (c *Config) Validate() error  // rejects empty Region (Bucket is optional)
```

## Test Mock

The `storagetest` package provides `MockStorage`, a configurable mock of
`gas.StorageProvider` for use in unit tests.

```go
import "github.com/gasmod/gas/storage/storagetest"
```

### MockStorage

```go
type MockStorage struct {
    UploadFn             func(ctx context.Context, key string, data io.Reader, opts ...gas.StorageOption) error
    DownloadFn           func(ctx context.Context, key string, opts ...gas.StorageOption) (*gas.StorageObject, error)
    DeleteFn             func(ctx context.Context, key string, opts ...gas.StorageOption) error
    HeadFn               func(ctx context.Context, key string, opts ...gas.StorageOption) (*gas.ObjectInfo, error)
    PresignDownloadURLFn func(ctx context.Context, key string, expires time.Duration, opts ...gas.StorageOption) (string, error)
    PresignUploadURLFn   func(ctx context.Context, key string, expires time.Duration, opts ...gas.StorageOption) (string, error)
    CheckReadyFn         func(ctx context.Context) error
    Calls                []Call
}
```

`MockStorage` also implements `gas.ReadyReporter` — wire `CheckReadyFn` to
simulate readiness states in tests.

Each method delegates to its `Fn` field if set, otherwise returns zero value.
All calls are recorded in `Calls` for assertions. Thread-safe.

| Method                  | Description                                    |
|-------------------------|------------------------------------------------|
| `Reset()`               | Clear all recorded calls                       |
| `CallCount(method) int` | Count calls by method name (e.g. `"Upload"`)   |

## DI Wiring Patterns

### S3 backend (production)

```go
app := gas.NewApp(
    gas.WithSingletonService[*storages3.Service](storages3.New()),
)
```

### With explicit config

```go
app := gas.NewApp(
    gas.WithSingletonService[*storages3.Service](
        storages3.New(storages3.WithConfig(&storages3.Config{
            Storage: storages3.Settings{
                Region:          "eu-west-1",
                Bucket:          "my-bucket",
                AccessKeyID:     "AKIA...",
                SecretAccessKey: "secret",
            },
        })),
    ),
)
```

### With custom endpoint (LocalStack / MinIO)

```go
app := gas.NewApp(
    gas.WithSingletonService[*storages3.Service](
        storages3.New(storages3.WithConfig(&storages3.Config{
            Storage: storages3.Settings{
                Region:          "us-east-1",
                Bucket:          "local-bucket",
                Endpoint:        "http://localhost:4566",
                AccessKeyID:     "test",
                SecretAccessKey: "test",
            },
        })),
    ),
)
```

### Consuming via gas.StorageProvider

Services receive storage through the provider interface, never importing
gas/storage backends directly:

```go
type Service struct {
    storage gas.StorageProvider
}

func New(storage gas.StorageProvider) *Service {
    return &Service{storage: storage}
}

func (s *Service) Init() error {
    // use s.storage.Upload, Download, Delete, Head, PresignDownloadURL, PresignUploadURL
    return nil
}
```

### Testing with MockStorage

```go
mock := &storagetest.MockStorage{}
mock.UploadFn = func(ctx context.Context, key string, data io.Reader, opts ...gas.StorageOption) error {
    return nil
}

// inject mock as gas.StorageProvider in tests
```
