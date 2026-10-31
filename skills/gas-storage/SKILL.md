---
name: gas-storage
description: >
  Reference documentation for the gas/storage Go module
  (github.com/gasmod/gas/storage), object storage for the Gas ecosystem. Use
  this skill when writing, reviewing, or debugging Go code that stores,
  reads, or lists objects or buckets through storage.Provider (including
  ListObjects pagination, prefixes, and delimiters) or presigns URLs through
  storage.Presigner, on AWS S3 or S3-compatible servers, on a local
  directory, or in memory. Covers the storage interfaces and input types,
  the input rules, listing, sentinel errors, the s3, dir and memory backends,
  DI wiring, configuration, and the storagetest mocks. Use it whenever code
  imports gasmod/gas/storage or any of its packages, even if the user does
  not mention it by name.
---

# Gas Storage Reference

```go
import "github.com/gasmod/gas/storage"
import storages3 "github.com/gasmod/gas/storage/s3"
import storagedir "github.com/gasmod/gas/storage/dir"
import storagemem "github.com/gasmod/gas/storage/memory"
import "github.com/gasmod/gas/storage/storagetest"
```

## Backends

| Backend   | Package          | Service name         | Implements                              | Use case                                |
|-----------|------------------|----------------------|-----------------------------------------|-----------------------------------------|
| S3        | `storage/s3`     | `gas/storage/s3`     | `storage.Provider`; `Presigner` type implements `storage.Presigner` | Production, any server that speaks S3's API |
| Directory | `storage/dir`    | `gas/storage/dir`    | `storage.Provider`                      | Single-host deployments, development    |
| Memory    | `storage/memory` | `gas/storage/memory` | `storage.Provider`                      | Tests and local development             |

Every backend also implements `gas.Service` and `gas.ReadyReporter`. The
rules are S3's and are checked before any I/O on every backend, so code
tested on memory runs on s3. Services depend on the interfaces, never on a
backend package.

## Interfaces

```go
type Provider interface {
    CreateBucket(ctx context.Context, in *CreateBucketInput) error
    HeadBucket(ctx context.Context, in *HeadBucketInput) (*Bucket, error)
    DeleteBucket(ctx context.Context, in *DeleteBucketInput) error
    ListBuckets(ctx context.Context, in *ListBucketsInput) (*ListBucketsOutput, error)

    HeadObject(ctx context.Context, in *HeadObjectInput) (*ObjectInfo, error)
    PutObject(ctx context.Context, in *PutObjectInput) (*PutObjectOutput, error)
    GetObject(ctx context.Context, in *GetObjectInput) (*Object, error)
    DeleteObject(ctx context.Context, in *DeleteObjectInput) error
    ListObjects(ctx context.Context, in *ListObjectsInput) (*ListObjectsOutput, error)
}

type Presigner interface {
    PresignUploadURL(ctx context.Context, in *PresignUploadURLInput) (*PresignURLResponse, error)
    PresignDownloadURL(ctx context.Context, in *PresignDownloadURLInput) (*PresignURLResponse, error)
}
```

### Inputs and outputs

- `BaseObjectInput{Bucket *string; Key string}` is embedded in every object
  input. A nil `Bucket` is the configured `storage.bucket`; none configured
  is `ErrInvalidBucket`. A pointer to `""` is an empty name, not the default.
  Promoted fields can be set directly:
  `&storage.GetObjectInput{Key: "cats/tom.jpg"}`.
- `HeadObjectInput` adds `PartNumber *int32`, `Range *string`,
  `Version *string`; `GetObjectInput` embeds `HeadObjectInput`.
- `PutObjectInput` embeds `BaseObjectInput`, `ObjectHeaders` and
  `WriteCondition`, and adds `Body io.Reader` (nil is an empty object) and
  `ContentLength *int64` (nil is unknown; when set, the body must match
  exactly, else `ErrContentLengthMismatch`, and nothing is stored).
- `ObjectHeaders`: `ContentType`, `CacheControl`, `ContentDisposition`,
  `ContentEncoding`, `ContentLanguage`, `Expires` (all `*string`) and
  `Metadata map[string]string`. A nil ContentType is stored as
  `application/octet-stream`.
- `PutObjectOutput{ETag, Version string; Size int64}`.
- `DeleteObjectInput` adds `Version`. Bucket inputs
  (`CreateBucketInput`, `HeadBucketInput`, `DeleteBucketInput`) take
  `Bucket string`, with no default.
- `ObjectInfo` embeds `HeaderValues` (the header fields as plain strings) and
  has `LastModified`, `Metadata`, `ETag`, `Version`, `Size`. `Object` embeds
  `ObjectInfo` and adds `Body io.ReadCloser`, which the caller must close.
  With a `Range`, `Size` is the length of the bytes selected.
- `Bucket{CreationDate time.Time; Name, Region string}`.
- `ETag` is unquoted; compare it only for equality (an S3 multipart upload's
  is not an MD5).

## Input rules

Checked in this order: nil input (`ErrNilInput`), backend state, context,
then the rules below, all before any I/O.

- **Keys:** 1 to 1024 bytes of valid UTF-8, no control character (byte below
  0x20, tab included, or DEL), last `/`-separated element not `.`
  (`ErrInvalidKey`).
- **Buckets:** S3 general purpose rules: 3 to 63 bytes of `a-z0-9.-`, letter
  or digit first and last, no `..`, no reserved prefix or suffix, not an
  IPv4 address (`ErrInvalidBucket`).
- **Header fields:** set values are non-empty printable ASCII (space and tab
  allowed, not leading or trailing), at most 1024 bytes. `Expires` must be
  an IMF-fixdate as `http.TimeFormat` writes it
  (`Wed, 21 Oct 2015 07:28:00 GMT`). Encode non-ASCII yourself
  (`ErrInvalidHeader`).
- **Metadata:** names are HTTP tokens, stored and returned in lower case
  (two names differing only in case are refused); values follow the header
  value rules (no non-ASCII); names and values at most 2048 bytes together
  (`ErrInvalidMetadata`).
- **Conditions:** `WriteCondition{IfMatch, IfNoneMatch *string}`.
  `IfNoneMatch: new("*")` writes only when no object exists;
  `IfMatch: &etag` (quoted or not) writes only over that ETag, and is
  `ErrObjectNotFound` when there is none. A condition that fails is
  `ErrPreconditionFailed`, nothing written. Both set, `IfNoneMatch` other
  than `*`, or a bad `IfMatch` is `ErrInvalidCondition`.
- **Ranges:** one byte range, `bytes=first-last`, `bytes=first-` or
  `bytes=-suffix`. Anything else is `ErrInvalidRange` (refused, not
  ignored). A first byte at or past the end, or `bytes=-0`, is
  `ErrRangeNotSatisfiable`.
- **Versions and parts:** a set `Version` must not be empty; `PartNumber` is 1
  to 10000 and not with a `Range` (`ErrInvalidArgument`). On dir and memory,
  either is `ErrNotSupported`.

A backend never changes the caller's input or keeps its maps. A failed
PutObject leaves the previous object as it was.

## Listing

Both list methods return one page, as S3 ListObjectsV2 and ListBuckets do,
in byte order.

```go
type ListObjectsInput struct {
    Bucket            *string // nil uses the configured bucket
    ContinuationToken *string // NextContinuationToken of the previous page
    Delimiter         *string // rolls keys up into CommonPrefixes; set means non-empty
    MaxKeys           *int32  // nil = 1000; outside 1..1000 = ErrInvalidArgument
    Prefix            *string
    StartAfter        *string // applies to keys before they are rolled up
}
type ListObjectsOutput struct {
    NextContinuationToken string // empty on the last page, and only there
    Objects               []ObjectSummary // Key, ETag, Size, LastModified
    CommonPrefixes        []string
    IsTruncated           bool
}

type ListBucketsInput struct {
    ContinuationToken *string
    MaxBuckets        *int32 // nil = 10000; outside 1..10000 = ErrInvalidArgument
    Prefix            *string
}
type ListBucketsOutput struct {
    NextContinuationToken string
    Buckets               []Bucket
}
```

```go
in := &storage.ListObjectsInput{Prefix: &prefix, Delimiter: new("/")}
for {
    out, err := p.ListObjects(ctx, in)
    if err != nil {
        return err
    }
    // use out.Objects and out.CommonPrefixes
    if !out.IsTruncated {
        break
    }
    in.ContinuationToken = &out.NextContinuationToken
}
```

- Objects and common prefixes count together toward `MaxKeys`; each common
  prefix appears once across the pages.
- **Tokens are opaque and sealed with a key each Init draws**: a token is
  good only for the Provider that issued it, until its next Init. One that
  is empty, changed, from another listing, from before a restart or from
  another replica is `ErrInvalidToken`.
- A listing is not a snapshot.

## Presigning

`PresignUploadURLInput` embeds `BaseObjectInput`, `ObjectHeaders` and
`WriteCondition`, and adds `ContentLength *int64` (**required**, 0 to 5 GiB)
and `TTL *time.Duration`. `PresignDownloadURLInput` embeds `GetObjectInput`
and `ResponseHeaders` (`ResponseContentDisposition` and so on) and adds `TTL`.
A nil or zero TTL is 15 minutes; otherwise 1 second to 7 days, truncated to a
whole second (`ErrInvalidArgument`). `PresignURLResponse{SignedHeader
http.Header, Expires time.Time, URL, Method string}`: the client must send the
Method, URL and every SignedHeader field unchanged. Signing does no I/O. A URL
is a credential until it expires, and one signed with temporary credentials
holds their session token.

## Sentinel errors

Match them with `errors.Is`; never compare messages. All live in `storage`.

| Sentinel | Returned when |
|---|---|
| `ErrNilInput` | the input is nil |
| `ErrInvalidKey`, `ErrInvalidBucket`, `ErrInvalidHeader`, `ErrInvalidMetadata` | an input rule above |
| `ErrInvalidCondition`, `ErrPreconditionFailed` | a bad condition; a condition that does not hold |
| `ErrInvalidRange`, `ErrRangeNotSatisfiable` | a malformed range; a range past the end |
| `ErrInvalidToken` | a continuation token that is empty or not this listing's |
| `ErrInvalidArgument` | a length, part number, version, listing argument or TTL out of bounds |
| `ErrContentLengthMismatch` | the body is shorter or longer than `ContentLength` |
| `ErrNotSupported` | `Version` or `PartNumber` on dir or memory |
| `ErrObjectNotFound` | HeadObject or GetObject of a missing object; wraps `ErrBucketNotFound` too when the backend can tell the bucket is missing (s3 HeadObject cannot) |
| `ErrVersionNotFound` | a missing version on s3, with `ErrObjectNotFound` |
| `ErrBucketNotFound` | PutObject, DeleteObject, ListObjects, HeadBucket, DeleteBucket of a missing bucket |
| `ErrBucketNotEmpty` | DeleteBucket of a bucket holding an object; nothing deleted |
| `ErrBucketExists` | s3 CreateBucket of a name another account owns. Creating your own bucket again is success |
| `ErrAccessDenied` | the backend refused the credentials or permission |
| `ErrBackend` | any other failure, such as I/O or no reply |
| `ErrNotInitialized`, `ErrAlreadyInitialized`, `ErrClosed` | lifecycle: before Init (or a nil backend), Init while open, after Close |
| `ErrNilLogger`, `ErrNilOption`, `ErrNilConfig` | constructor got a nil logger, a nil option (position named), `WithConfig(nil)` |
| `ErrNoConfigProvider`, `ErrInvalidConfig` | Init could not resolve a valid configuration |

`DeleteObject` of a missing key succeeds. Errors start with the service name
and operation, such as `gas/storage/s3: get object: object not found`, and
never hold a key, bucket name, metadata, header value, path, endpoint, URL,
token or credential.

## S3 backend

```go
func New(opts ...Option) func(gas.ConfigProvider, gas.Logger) (*Provider, error)
func NewPresigner(opts ...PresignerOption) func(gas.ConfigProvider, gas.Logger) (*Presigner, error)
```

| Option                               | For       | Description                                       |
|--------------------------------------|-----------|---------------------------------------------------|
| `WithConfig(*Config)`                | Provider  | Use this config (a copy, same checks) instead of binding |
| `WithClient(API)`                    | Provider  | Use this `*s3.Client` or fake; region, endpoint, path style, credentials, retries then have no effect. Nil fails Init with `ErrNilClient` |
| `WithPresignerConfig(*Config)`       | Presigner | As `WithConfig`                                   |
| `WithPresignClient(PresignAPI)`      | Presigner | Use this `*s3.PresignClient` or fake              |

Config section `storage`, shared by store and presigner:

| Key | Meaning |
|---|---|
| `storage.region` | AWS region; empty uses the SDK chain; none found is `storages3.ErrNoRegion` |
| `storage.endpoint` | `http(s)` URL with host and nothing after but `/`, for S3-compatible servers or emulators |
| `storage.usepathstyle` | `*bool`; default true with `endpoint`, false without |
| `storage.bucket` | Configured bucket; checked by CheckReady; Init does not create it |
| `storage.accesskeyid`, `storage.secretaccesskey`, `storage.sessiontoken` | Static credentials, used only when both keys are set; the secret and token are `Secret` (`= gas.Secret`, redacted) |
| `storage.optimeout` | Bound of each request, retries included; default 10s |
| `storage.maxretries` | 0 to 10; default 2 |

Behavior:
- Init builds the client and retrieves credentials within `optimeout`
  (`storages3.ErrNoCredentials`); it sends no S3 request.
- `PutObject` uses the SDK transfer manager: bodies under 16 MiB in one
  request, larger ones in 8 MiB parts (ETag then not an MD5). A failed
  multipart upload is aborted; if the abort fails, the error wraps
  `storages3.ErrUploadNotAborted`.
- Replies map to sentinels (NoSuchBucket, NoSuchKey, NoSuchVersion,
  BucketAlreadyExists, BucketNotEmpty, PreconditionFailed, InvalidRange).
  Others carry a reason and the AWS code: `access denied`, `throttled`
  (`ErrThrottled`), `wrong region` (`ErrWrongRegion`), `conflict`
  (`ErrConflict`), `failed`; no reply wraps `ErrConnect`. The SDK error is
  not kept.
- `GetObject`'s Body is governed by GetObject's context alone and stays
  readable after Close.
- `CheckReady` sends HeadBucket of the configured bucket, if any.
- `Provider.Client() *s3.Client` and `Presigner.PresignClient()` return the
  SDK clients for operations the interfaces lack; their errors are the
  SDK's, unredacted.

## Directory backend

```go
func New(opts ...Option) func(gas.ConfigProvider, gas.Logger) (*Provider, error)
// options: WithConfig(*Config), WithRoot(*os.Root), WithSkipHandler(SkipHandler)
```

Config section `storage`: `storage.dir` (must exist; required unless
`WithRoot`, and then must be empty), `storage.bucket` (Init creates it when
missing), `storage.sweeplimit` (default 100000).

- **Root.** Init opens `storage.dir` with `os.OpenRoot` and Close closes it.
  With `WithRoot` the caller owns the root and closes it after Close.
- **Keys** are stored under hashes: `../x` names no path, and keys differing
  only in case are distinct on every file system.
- **Writes** are atomic and durable once PutObject returns; conditions are
  evaluated under a per-key file lock, so they hold across processes on one
  directory. A slow body holds its key's lock.
- **Reads:** GetObject's Body is an `io.ReadSeeker` (a ranged body seeks
  within the range). An object whose files cannot be used wraps
  `storagedir.ErrCorrupt` and `storage.ErrBackend`, never
  `ErrObjectNotFound`.
- **Buckets:** every bucket belongs to the caller, so no `ErrBucketExists`.
  HeadBucket returns the name only.
- **Layout:** a directory with buckets but no layout marker (an earlier
  version's) fails Init with `storagedir.ErrOldLayout`. `WithRoot(nil)` is
  `storagedir.ErrNilRoot`.
- **Skip handler:** `SkipHandler func(path string, err error)` hears of each
  entry a listing leaves out (`ErrCorrupt`, `ErrUnnamedBucket`), debris
  DeleteBucket removes, and leftovers Init sweeps. Paths are hashes relative
  to the root; keep them private.
- For a bucket with very many keys, prefer s3.

## Memory backend

```go
func New(opts ...Option) func(gas.ConfigProvider, gas.Logger) (*Provider, error) // WithConfig
```

Config section `storage`: `storage.bucket` (Init creates it),
`storage.maxbytes` (default 256 MiB, at least 1 MiB), `storage.maxobjects`
(default 100000); at most 10000 buckets. Past a limit is
`storagemem.ErrStorageFull`. Buckets and objects outlive Close. GetObject's
Body is also an `io.Seeker`.

## Wiring

Each constructor also needs a registered `gas.ConfigProvider` (unless
`WithConfig`) and `gas.Logger`.

```go
app := gas.NewApp(
    gas.WithSingletonService[storage.Provider](storages3.New()),
    gas.WithSingletonService[storage.Presigner](storages3.NewPresigner()),
)
```

Directory backend, configured by `storage.dir`, or with an explicit config:

```go
cfg := storagedir.DefaultConfig()
cfg.Storage.Dir = "/var/lib/myapp/storage"
cfg.Storage.Bucket = "uploads"

app := gas.NewApp(
    gas.WithSingletonService[storage.Provider](storagedir.New(storagedir.WithConfig(cfg))),
)
```

Consuming:

```go
type Service struct {
    storage storage.Provider
}

func (s *Service) Read(ctx context.Context, key string) ([]byte, error) {
    obj, err := s.storage.GetObject(ctx, &storage.GetObjectInput{
        HeadObjectInput: storage.HeadObjectInput{
            BaseObjectInput: storage.BaseObjectInput{Key: key},
        },
    })
    if errors.Is(err, storage.ErrObjectNotFound) {
        return nil, gas.NotFound("file not found").WithCause(err)
    }
    if err != nil {
        return nil, err
    }
    defer obj.Body.Close()
    return io.ReadAll(obj.Body)
}
```

## Test mocks

`storagetest.MockProvider` and `storagetest.MockPresigner` are recording
mocks; the zero value is ready to use.

- **Delegation.** Each method records a `Call` (a copy of the input), then
  calls its `Fn` field (`CreateBucketFn`, `HeadBucketFn`, ..., `PresignUploadURLFn`,
  `PresignDownloadURLFn`).
- **Defaults** with a field nil: success with a zero output, never nil
  (HeadBucket returns the name asked for, lists return an empty page,
  PutObject a zero output without reading the body, presign a response with
  only `Method`), but **HeadObject and GetObject return
  `storage.ErrObjectNotFound`**.
- **Assertions.** `Calls()` (deep copy), `CallCount(method)`, `Reset()`.
- **Concurrency.** Methods may run concurrently; setting a field while a
  method runs is a data race. The mock checks no rule; to test the rules,
  use the memory backend.

```go
mock := &storagetest.MockProvider{
    GetObjectFn: func(_ context.Context, _ *storage.GetObjectInput) (*storage.Object, error) {
        return nil, storage.ErrObjectNotFound
    },
}
app := gas.NewApp(gas.WithServiceInstance[storage.Provider](mock))
```
