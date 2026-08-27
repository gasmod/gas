# gas/storage

[![Test](https://github.com/gasmod/gas/actions/workflows/test.yml/badge.svg)](https://github.com/gasmod/gas/actions/workflows/test.yml) [![Go Reference](https://pkg.go.dev/badge/github.com/gasmod/gas/storage.svg)](https://pkg.go.dev/github.com/gasmod/gas/storage) ![Go Version](https://img.shields.io/github/go-mod/go-version/gasmod/gas?filename=storage/go.mod) [![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)

Part of the [Gas](../README.md) monorepo · [All modules](../README.md#modules) · [Examples](../example/README.md)

Storage service for the [Gas](../README.md) framework. Provides a `gas.StorageProvider` implementation
backed by AWS S3 (and S3-compatible services like MinIO, LocalStack, DigitalOcean Spaces).

## Install

```bash
go get github.com/gasmod/gas/storage
```

`s3.New()` returns a DI constructor that takes `gas.ConfigProvider` and `gas.Logger`, so register both alongside the storage service (see [gas/config](../config/README.md) and [gas/log](../log/README.md)).

## Backends

| Backend | Package                            | Use case                                     |
|---------|------------------------------------|----------------------------------------------|
| S3      | `github.com/gasmod/gas/storage/s3` | Production, any S3-compatible object storage |

The S3 backend implements `gas.Service` and `gas.StorageProvider`.

## Usage

### S3 backend

```go
package main

import (
	"log"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/config/providers"
	gaslog "github.com/gasmod/gas/log"
	storages3 "github.com/gasmod/gas/storage/s3"
)

func main() {
	cfg := config.New(config.WithProvider(providers.NewEnvProvider()))
	if err := cfg.Load(); err != nil {
		log.Fatal(err)
	}

	app := gas.NewApp(
		gas.WithServiceInstance[gas.ConfigProvider](cfg),
		gas.WithSingletonService[gas.Logger](gaslog.NewSlogLogger()),

		gas.WithSingletonService[gas.StorageProvider](storages3.New()),
	)

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
```

With custom configuration:

```go
cfg := storages3.DefaultConfig()
cfg.Storage.Region = "eu-west-1"
cfg.Storage.Bucket = "my-bucket"
cfg.Storage.AccessKeyID = "AKIA..."
cfg.Storage.SecretAccessKey = "secret"

storages3.New(storages3.WithConfig(cfg))
```

With a custom endpoint (MinIO, LocalStack, etc.):

```go
cfg := storages3.DefaultConfig()
cfg.Storage.Bucket = "local-bucket"
cfg.Storage.Endpoint = "http://localhost:4566"
cfg.Storage.AccessKeyID = "test"
cfg.Storage.SecretAccessKey = "test"

storages3.New(storages3.WithConfig(cfg))
```

### Dependency injection

Services receive storage through `gas.StorageProvider` via constructor injection:

```go
type Service struct {
	storage gas.StorageProvider
}

func New(storage gas.StorageProvider) *Service {
	return &Service{storage: storage}
}

func (s *Service) Init() error {
	ctx := context.Background()
	_ = s.storage.Upload(ctx, "hello.txt", strings.NewReader("world"),
		gas.WithContentType("text/plain"),
	)
	return nil
}
```

### Operations

`gas.StorageProvider` covers the six operations most applications need. Every one takes optional
`gas.StorageOption` values at the end.

| Method                                            | Returns                | Notes                                                        |
|---------------------------------------------------|------------------------|--------------------------------------------------------------|
| `Upload(ctx, key, data io.Reader, opts...)`       | `error`                | Streams the reader to the object store                       |
| `Download(ctx, key, opts...)`                     | `*gas.StorageObject`   | Body, content type, size, and metadata; caller closes `Body` |
| `Head(ctx, key, opts...)`                         | `*gas.ObjectInfo`      | Metadata without the body: size, content type, last modified |
| `Delete(ctx, key, opts...)`                       | `error`                |                                                              |
| `PresignDownloadURL(ctx, key, expires, opts...)`  | `string`               | Time-limited URL a browser can GET directly                  |
| `PresignUploadURL(ctx, key, expires, opts...)`    | `string`               | Time-limited URL a browser can PUT directly                  |

| Option                          | Applies to           | Effect                                        |
|---------------------------------|----------------------|-----------------------------------------------|
| `gas.InBucket(name)`            | all                  | Overrides the default bucket for one call     |
| `gas.WithContentType(ct)`       | `Upload`, presigning | Sets the object's content type                |
| `gas.WithMetadata(map)`         | `Upload`             | Attaches user metadata to the object          |

Presigned URLs let clients transfer bytes straight to S3, so large files never pass through your server:

```go
// Hand the browser a URL it can PUT the file to, valid for 15 minutes.
url, err := s.storage.PresignUploadURL(ctx, "uploads/"+id, 15*time.Minute,
	gas.WithContentType("image/png"),
)

// ...and a URL it can download from later, from a different bucket.
url, err = s.storage.PresignDownloadURL(ctx, "uploads/"+id, time.Hour,
	gas.InBucket("archive-bucket"),
)
```

`Download` returns an open body that the caller must close:

```go
obj, err := s.storage.Download(ctx, "uploads/"+id)
if err != nil {
	return err // storage.ErrKeyNotFound if the object is gone
}
defer obj.Body.Close()
```

### Direct S3 client access

For advanced S3 operations beyond the `StorageProvider` interface, type-assert to access the underlying client:

```go
type S3Provider interface {
	Client() *s3.Client
}

func (s *Service) Init() error {
	if sp, ok := s.storage.(S3Provider); ok {
		client := sp.Client()
		// use client for multipart uploads, bucket operations, etc.
	}
	return nil
}
```

## Config

If `WithConfig` is not provided, the backend automatically binds configuration from the `gas.ConfigProvider` injected
via DI. This lets you drive storage settings from environment variables or a config file without any explicit wiring.

### S3 config

| Field                     | Description                                           |
|---------------------------|-------------------------------------------------------|
| `Storage.Region`          | AWS region for the S3 bucket (required)               |
| `Storage.Bucket`          | Default S3 bucket name (optional; if unset, every call must pass `gas.InBucket(...)`) |
| `Storage.AccessKeyID`     | AWS access key (empty = use default credential chain) |
| `Storage.SecretAccessKey` | AWS secret access key                                 |
| `Storage.Endpoint`        | Custom S3 endpoint; enables path-style when set       |

## Readiness

The S3 backend implements `gas.ReadyReporter`. When a default `Storage.Bucket`
is configured, `CheckReady` issues a `HeadBucket` against it so the Kubernetes
readiness probe only passes once credentials are valid and the bucket is
reachable from the pod.

**IAM requirement:** the pod's principal MUST have `s3:ListBucket` on the
configured bucket. Without it, `HeadBucket` returns 403 and the pod will
never become ready. If your policy scopes S3 permissions narrowly (e.g.
only `s3:GetObject`/`s3:PutObject` on a prefix), add `s3:ListBucket` on the
bucket ARN:

```json
{
  "Effect": "Allow",
  "Action": "s3:ListBucket",
  "Resource": "arn:aws:s3:::your-bucket"
}
```

When no default bucket is configured (per-call bucket mode), `CheckReady`
returns success as soon as `Init` has completed — there is no single bucket
to probe, and reachability is the caller's responsibility.

## Testing

The `storagetest` package provides a mock implementation of `gas.StorageProvider`:

```go
import "github.com/gasmod/gas/storage/storagetest"

mock := &storagetest.MockStorage{}
mock.UploadFn = func(ctx context.Context, key string, data io.Reader, opts ...gas.StorageOption) error {
	return nil
}

// pass mock as gas.StorageProvider
// assert calls:
if mock.CallCount("Upload") != 1 {
	t.Error("expected one Upload call")
}
```

## Sentinel Errors

The root `storage` package defines three sentinel errors:

```go
storage.ErrKeyNotFound    // returned by Download or Head when the key does not exist
storage.ErrClosed         // returned when an operation is attempted on a closed service
storage.ErrBucketRequired // no default Storage.Bucket configured and no gas.InBucket() passed
```
