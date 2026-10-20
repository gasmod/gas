---
name: gas-storage
description: >
  Reference documentation for the gas/storage Go module
  (github.com/gasmod/gas/storage), object storage for the Gas ecosystem. Use
  this skill when writing, reviewing, or debugging Go code that stores,
  reads, or lists objects or buckets through storage.Provider (including
  ListObjects pagination, prefixes, and delimiters) or presigns URLs through
  storage.Presigner, on AWS S3 or S3-compatible backends (MinIO, LocalStack)
  or on a local directory. Covers the storage interfaces and input types,
  listing, sentinel errors, the s3 and dir backends, DI wiring, configuration, and the
  storagetest mocks. Use it whenever code imports gasmod/gas/storage or any
  of its packages, even if the user does not mention it by name.
---

# Gas Storage Reference

```go
import "github.com/gasmod/gas/storage"
import storages3 "github.com/gasmod/gas/storage/s3"
import storagedir "github.com/gasmod/gas/storage/dir"
import "github.com/gasmod/gas/storage/storagetest"
```

## Backends

| Backend   | Package       | Implements                              | Use case                                    |
|-----------|---------------|-----------------------------------------|---------------------------------------------|
| S3        | `storage/s3`  | `storage.Provider`, `storage.Presigner` | Production, any S3-compatible store         |
| Directory | `storage/dir` | `storage.Provider`                      | Development, tests, single-host deployments |

Services depend on the interfaces, never on a backend package.

## Interfaces

```go
type Provider interface {
    CreateBucket(ctx context.Context, input *CreateBucketInput) (*Bucket, error)
    HeadBucket(ctx context.Context, input *HeadBucketInput) (*Bucket, error)
    DeleteBucket(ctx context.Context, input *DeleteBucketInput) error
    ListBuckets(ctx context.Context, input *ListBucketsInput) (*ListBucketsOutput, error)

    HeadObject(ctx context.Context, input *HeadObjectInput) (*ObjectInfo, error)
    PutObject(ctx context.Context, input *PutObjectInput) (*ObjectInfo, error)
    GetObject(ctx context.Context, input *GetObjectInput) (*Object, error)
    DeleteObject(ctx context.Context, input *DeleteObjectInput) (*ObjectInfo, error)
    ListObjects(ctx context.Context, input *ListObjectsInput) (*ListObjectsOutput, error)
}

type Presigner interface {
    PresignUploadURL(ctx context.Context, input *PresignUploadURLInput) (*PresignURLResponse, error)
    PresignDownloadURL(ctx context.Context, input *PresignDownloadURLInput) (*PresignURLResponse, error)
}
```

Every object input embeds `BaseObjectInput`:

```go
type BaseObjectInput struct {
    Bucket *string // nil uses the configured default bucket (config key storage.bucket)
    Key    string
}
```

- `HeadObjectInput` and `GetObjectInput` add `Version`, `PartNumber`, `Range` and `Response*` fields.
- `PutObjectInput` adds `Body io.Reader` (nil is an empty object), `ContentLength`, and an embedded `ObjectHeaders`: `ContentType`, `CacheControl`, `ContentDisposition`, `ContentEncoding`, `ContentLanguage` and `Expires` (all `*string`) and `Metadata map[string]string`. Set the headers directly (`storage.PutObjectInput{BaseObjectInput: ..., ContentType: &ct}`, which go 1.27 accepts for promoted fields) or through `ObjectHeaders: storage.ObjectHeaders{...}`. Every header a Put sets reads back on `ObjectInfo`. `Expires` is text: s3 accepts only an IMF-fixdate (`Wed, 21 Oct 2026 07:28:00 GMT`, weekday matching the date), the one form the SDK sends unchanged, and returns `ErrInvalidHeader` for anything else (RFC 850, asctime, a wrong weekday), sending nothing; dir stores any text. `ContentLength` nil means unknown; when set, the body must match it exactly or `PutObject` fails with `ErrContentLengthMismatch` and stores nothing (both backends).
- `DeleteObjectInput` adds `Version`.
- `PresignUploadURLInput` and `PresignDownloadURLInput` wrap the put and get inputs and add `TTL *time.Duration`. Nil or zero means 15 minutes on s3. A TTL that is negative, under one second, or over seven days fails with `storage.ErrInvalidTTL` and nothing is signed. s3 signs whole seconds and truncates any fraction (1.9s signs as 1s).

`GetObject` returns `*storage.Object`, which embeds `ObjectInfo` (`LastModified`, `Metadata`, `ContentType`, `CacheControl`, `ContentDisposition`, `ContentEncoding`, `ContentLanguage`, `Expires`, `ETag`, `Version`, `Size`; the header fields are plain strings) and adds a `Body io.ReadCloser` that the caller must close.

`PutObjectInput` also embeds `WriteCondition{IfMatch, IfNoneMatch *string}`. `IfNoneMatch: new("*")` writes only a key with no object; `IfMatch: &etag` (quoted or not) writes only over the object with that ETag. A condition that does not hold is `ErrPreconditionFailed` and writes nothing; `IfMatch` of a missing key is `ErrKeyNotFound`; an `IfNoneMatch` other than `"*"`, an empty or `"*"` `IfMatch`, or both set is `ErrInvalidCondition`, before anything is sent. dir checks the condition under the key's lock, so it holds across processes; an object with no recorded ETag (older dir versions) matches no `IfMatch`. `PresignUploadURL` signs the condition.

`ETag` is unquoted and `PutObject` returns it on both backends. Compare it only for equality: dir gives the hex MD5 of the body, but an S3 object uploaded in parts has an ETag ending in `-N`, and one encrypted with SSE-KMS has no MD5 ETag. An object written by a dir version that recorded no ETag reads back with an empty one. `ObjectSummary` (listing) carries `Key`, `ETag`, `Size` and `LastModified`.

## Listing

Both list methods return one page, following S3 ListObjectsV2 and ListBuckets. Entries come in byte order of their UTF-8 names.

```go
type ListObjectsInput struct {
    Bucket            *string // nil uses the configured default bucket
    ContinuationToken *string // NextContinuationToken of the previous page; "" is ErrInvalidContinuationToken
    Delimiter         *string // rolls keys up into CommonPrefixes
    MaxKeys           *int32  // nil = 1000, over 1000 = 1000, under 1 = ErrInvalidLimit
    Prefix            *string
    StartAfter        *string // applies to keys before they are rolled up
}
type ListObjectsOutput struct {
    NextContinuationToken string // empty when IsTruncated is false
    Objects               []ObjectSummary // Key, ETag, Size, LastModified
    Skipped               int // entries left out as unreadable, whole bucket (every page repeats it, plus that page's own objects damaged mid-listing); 0 on s3
    CommonPrefixes        []string
    IsTruncated           bool
}

type ListBucketsInput struct {
    ContinuationToken *string // NextContinuationToken of the previous page
    MaxBuckets        *int32 // nil = 10000, outside 1..10000 = ErrInvalidLimit
    Prefix            *string
}
type ListBucketsOutput struct {
    NextContinuationToken string   // empty on the last page, and only there
    Buckets               []Bucket // Name, CreationDate, ID, Region
    Skipped               int      // buckets left out as unnameable; 0 on s3
}

type Bucket struct {
    CreationDate time.Time
    Name         string
    ID           string // the ARN on s3; empty on dir
    Region       string // empty on dir
}
```

Page through objects:

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

- Objects and common prefixes count together toward `MaxKeys`, and each common prefix appears once across all pages.
- Tokens are opaque and belong to the backend that issued them.
- `ListObjects` of a missing bucket is `ErrBucketNotFound`.

## Sentinel errors

Match them with `errors.Is`; never compare messages.

| Sentinel                     | Returned when                                                                                                                                                                                                                                   |
|------------------------------|-------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `storage.ErrNotInitialized`  | a provider or presigner was not built by its constructor (zero value or nil pointer)                                                                                                                                                            |
| `storage.ErrInputRequired`   | the input is nil                                                                                                                                                                                                                                |
| `storage.ErrInvalidKey`      | the key is empty, longer than 1024 bytes, not valid UTF-8, or cleans to `.` or ends in a `.` path element (`./`, `a/.`). Only `/` separates elements, on every OS: `a\.`, `.\` and `\` are valid keys, on Windows too                           |
| `storage.ErrInvalidBucket`   | the bucket name is invalid, or neither the input nor the config names one                                                                                                                                                                       |
| `storage.ErrBucketNotFound`  | an object operation, or `ListObjects`, targets a missing bucket (s3 HeadObject excepted)                                                                                                                                                                           |
| `storage.ErrKeyNotFound`     | `GetObject` of a missing key                                                                                                                                                                                                                    |
| `storage.ErrNotFound`        | `HeadObject` of a missing object. On s3 this also covers a missing bucket, because S3 HEAD responses have no body                                                                                                                               |
| `storage.ErrPreconditionFailed` | `PutObject` whose `WriteCondition` does not hold. Nothing is written                                                                                                                                                                         |
| `storage.ErrInvalidCondition` | `PutObject` or `PresignUploadURL` with an `IfNoneMatch` other than `"*"`, an empty or `"*"` `IfMatch`, or both set. Nothing is sent                                                                                                         |
| `storage.ErrBucketNotEmpty`  | `DeleteBucket` of a bucket that holds objects. Nothing is deleted                                                                                                                                                                                |
| `storage.ErrAccessDenied`    | s3 `HeadBucket` or `DeleteBucket` answered 403: a bucket another account owns, or one the caller lacks permission on. dir never returns it                                                                                                       |
| `storage.ErrBucketExists`    | s3 `CreateBucket` of a name another account owns (S3 BucketAlreadyExists). `CreateBucket` of a bucket the caller already owns succeeds in both backends and every region                                                                                     |
| `storage.ErrVersionNotFound` | s3 `GetObject` of a `Version` that does not exist (S3 NoSuchVersion). Matches neither `ErrKeyNotFound` nor `ErrNotFound`. s3 `HeadObject` reports an unknown version as `ErrNotFound`, since HEAD responses have no body. A malformed version ID may get a 400 from AWS with no sentinel. On dir, `Version` is `ErrNotSupported`|
| `storage.ErrInvalidOption`   | a backend constructor was given a nil option. The message names its zero-based position: `gas/storage/s3/provider: storage: invalid option: option 1 is nil`                                                                                    |
| `storage.ErrInvalidLimit`    | `ListObjects` with `MaxKeys` under 1, or `ListBuckets` with `MaxBuckets` outside 1 to 10000. Nothing is sent                                                                                                                                     |
| `storage.ErrInvalidContinuationToken` | a list `ContinuationToken` that is set but empty, or one the dir backend did not issue. s3 may reject a token S3 did not issue with an error that carries no sentinel                                                                   |
| `storage.ErrInvalidHeader`   | s3 `PutObject` or `PresignUploadURL` with an `Expires` that is not an IMF-fixdate. Nothing is sent or signed. dir stores any text                                                                                                                  |
| `storage.ErrInvalidTTL`      | a `Presigner` method was given a TTL that is negative, under one second, or over seven days                                                                                                                                                     |
| `storage.ErrNotSupported`  | dir `HeadObject` or `GetObject` with `Version` or `PartNumber` set, or `DeleteObject` with `Version` set: dir neither versions objects nor stores them in parts                                                                                   |
| `storage.ErrContentLengthMismatch` | `PutObject` with `ContentLength` set and a `Body` that is shorter or longer, or a negative length, on both backends. Nothing is stored; the previous object stays                                                                         |
| `storage.ErrInvalidRange`  | `GetObject` or `HeadObject` with a `Range` that cannot be satisfied: its first byte is at or past the end, or `bytes=-0` (S3 416 InvalidRange)                                                                                                    |

`DeleteObject` of a missing key succeeds on both backends. `DeleteBucket` of a missing bucket is `ErrBucketNotFound` on both, as on S3, and `HeadBucket`/`DeleteBucket` take an explicit `Bucket` (`HeadBucketInput{Bucket: "b"}`): the configured bucket is not a fallback.

Every error has the form `gas/storage/<backend>/<component>: <op>: <cause>`, for example `gas/storage/s3/provider: get: storage: key not found: ...`. Errors never quote the object key from the backend's own code, but s3 errors carry the AWS SDK error, and that can include the request URL.

## S3 backend

```go
func NewProvider(opts ...ProviderOption) func(gas.ConfigProvider) (*Provider, error)
func NewPresigner(opts ...PresignerOption) func(gas.ConfigProvider) (*Presigner, error)
```

The container passes both the registered `gas.ConfigProvider`, which they bind into `s3.Config`; a missing one is the container's `no registration for gas.ConfigProvider` error at startup, and a nil one passed directly returns an error. Neither sends a request at construction. A nil option makes the constructor return an error wrapping `storage.ErrInvalidOption` instead of panicking. An option given a nil client (`WithClient(nil)` and so on) is ignored, and the constructor builds that client as if the option were absent.

| Option                                        | For       | Description                                            |
|-----------------------------------------------|-----------|--------------------------------------------------------|
| `WithClient(*s3.Client)`                      | Provider  | Use this S3 client instead of building one from config |
| `WithUploaderClient(*transfermanager.Client)` | Provider  | Use this transfer manager for `PutObject`              |
| `WithPresignerS3Client(*s3.Client)`           | Presigner | Build the presign client from this S3 client           |
| `WithPresignClient(*s3.PresignClient)`        | Presigner | Use this presign client                                |

Config keys, all under `storage`:

| Key                                              | Meaning                                                                                            |
|--------------------------------------------------|----------------------------------------------------------------------------------------------------|
| `bucket`                                         | Default bucket for inputs with a nil `Bucket`. Optional; must be a valid bucket name if set        |
| `region`                                         | AWS region. Empty uses the SDK's default resolution                                                |
| `accessKeyID`, `secretAccessKey`, `sessionToken` | Static credentials, used when `accessKeyID` is set. Otherwise the default credential chain applies |
| `endpoint`                                       | Custom endpoint for S3-compatible services. Setting it enables path-style addressing               |

Behavior:
- `PutObject` uploads through the SDK transfer manager. It accepts any reader, including one of unknown length, and switches to multipart for large bodies.
- S3 errors map to sentinels: NoSuchBucket to `ErrBucketNotFound`, NoSuchKey to `ErrKeyNotFound`, NoSuchVersion to `ErrVersionNotFound`, a HEAD 404 to `ErrNotFound`, InvalidRange (GET 416) and RequestedRangeNotSatisfiable (HEAD 416) to `ErrInvalidRange`, and BucketAlreadyExists to `ErrBucketExists`. A ranged GetObject or HeadObject reports the range's length as `Size`; `Object` has no total-size or Content-Range field. The SDK error stays in the chain for `errors.As`.
- `CreateBucket` creates the bucket in the client's region, sending that region as the location constraint outside us-east-1 and `aws-global` (S3 rejects the request there without it); with no configured region it uses the region the SDK resolved. It returns a `Bucket` with the `Name` and the ARN as `ID`; `CreationDate` and `Region` are empty, and S3's `Location` header is not reported. A bucket the caller already owns is success, and its `Bucket` may hold only the `Name`: us-east-1 is said to answer 200 with the ARN, other regions answer BucketAlreadyOwnedByYou with none (not verified against AWS). `ListBuckets` fills `Name` and `CreationDate`, and `ID` and `Region` when S3 sends them.
- `HeadBucket` returns `Name`, the ARN as `ID` and `Region`; no `CreationDate` (S3 does not report one). `HeadBucket` and `DeleteBucket` map a missing bucket to `ErrBucketNotFound`, BucketNotEmpty to `ErrBucketNotEmpty`, and any 403 to `ErrAccessDenied`; a 301 for a bucket in another region has no sentinel.
- `PutObject` returns the `ETag` and `Version` only. `HeadObject` and `GetObject` return `Expires` as the text S3 sent.
- Presign TTL: nil or zero signs 15 minutes, passed to the SDK explicitly, so it also overrides a default expiry set on a presign client given with `WithPresignClient`. Valid TTLs run from 1 second to 7 days inclusive (the AWS SigV4 range); anything else is `ErrInvalidTTL`. `X-Amz-Expires` is whole seconds, truncated toward zero.
- `ListObjects` sends ListObjectsV2 with `encoding-type=url` and decodes keys and common prefixes, so keys with characters XML 1.0 cannot carry (such as `\x01`) are listed intact. `ListBuckets` lists the buckets the credentials own.
- `Provider.Client()` and `Presigner.PresignClient()` return the underlying SDK clients, for operations the interfaces do not cover.

## Directory backend

```go
func NewProvider(opts ...Option) func(gas.ConfigProvider) (*Provider, error)
func WithRoot(root *os.Root) Option // required
```

- **Config.** The provider binds `storage.bucket` as the default bucket.
- **Root.** The caller opens the `os.Root` and closes it once the provider is no longer used. The provider never closes it.
- **Buckets.** Create them with `CreateBucket`; making a directory does not create one. `CreateBucket` of an existing bucket succeeds and keeps its objects; dir never returns `ErrBucketExists`.
- **Keys.** Keys are opaque strings, hashed on disk: `../x`, `/abs` and keys that differ only in case are distinct objects on every filesystem.
- **Writes.** Overwrites are atomic: a reader sees the old object or the new one.
- **Locking.** Operations on one key are serialized with a file lock, which also excludes other processes sharing the directory.
- **Versions and parts.** Objects are not versioned or stored in parts: `Version` on Head, Get or Delete, and `PartNumber` on Head or Get, return `storage.ErrNotSupported`, after key and bucket validation and before any disk access.
- **Range.** Get and Head honor `Range` with RFC 9110 single-range semantics: one `bytes=first-last`, `bytes=first-` or `bytes=-suffix` range, clamped to the object, with `Size` set to its length; anything else is ignored (whole object); a first byte at or past the end, or `bytes=-0`, is `ErrInvalidRange`. S3 (LocalStack at least) is more lenient on some malformed headers (a unit other than bytes, spaces around `=` or `-`, a `+` sign), and answers 416 on an empty object where dir returns the empty body for an ignored header; dir follows RFC 9110.
- **Ignored fields.** `Response*`. `ObjectInfo` carries no `Version`.
- **Put result.** `PutObject` returns the object as `HeadObject` then reads it: `ETag` (hex MD5 of the body), size, modification time, content type, metadata and headers. `DeleteObject` returns an empty `ObjectInfo`.
- **Bucket fields.** `CreateBucket`, `HeadBucket` and `ListBuckets` fill `Name` and `CreationDate`; `ID` and `Region` are empty. `HeadBucket` of a bucket whose name record is missing or damaged succeeds with a zero `CreationDate`.
- **DeleteBucket.** Refuses a bucket that holds an object, removing nothing. Unreadable metadata, metadata whose data file is gone, and temp files of crashed writes do not count: they are removed with the bucket once no object was found, so they can never make it undeletable. A file in the bucket's metadata directory that dir did not write fails the call. A write of an existing key in progress is waited for, so a slow `PutObject` holds `DeleteBucket` (and a same-name `CreateBucket`, which waits for the delete) until it ends or ctx is done. A write of a new key still copying its body is not waited for: the bucket is deleted and that `PutObject` returns `ErrBucketNotFound`. A bucket created again gets a new `CreationDate`, except after a crash between the delete's commit and its removal of the name record.
- **Content type.** A Put without one sniffs it from the body.
- **Listing.** Each call reads every name in the bucket (or every bucket's name record), so a page costs time in proportion to the bucket. Continuation tokens are not interchangeable with S3's. `CreateBucket` records the bucket's name under `.gas/buckets` the first time it runs, and that record's modification time is `CreationDate`. A bucket whose name record is missing (created by an earlier version) or damaged (a crash left it empty) is left out of `ListBuckets` until `CreateBucket` of it runs again, which rewrites the record; its objects stay reachable through the other methods meanwhile. An object that cannot be read (metadata that does not decode or holds another key, or a data file that is gone) is left out of `ListObjects`; an I/O error reading metadata still fails the call, and an object deleted while the page is built is left out. Each listing counts what it left out in `Skipped`, over the whole bucket (or every bucket), so every page repeats it.
- **Skip handler.** `dir.WithSkipHandler(func(path string, err error))` hears about each entry a listing leaves out and each piece of debris `DeleteBucket` removes or leftover it leaves: `path` is relative to the root, and `err` wraps `dir.ErrCorrupt` (an object that cannot be read) or `dir.ErrUnnamedBucket` (a bucket that cannot be named), or is a plain error. It runs synchronously, once per entry per call (so once per page), and may call the provider: `DeleteBucket` calls it only after releasing its locks. Paths never appear in the listing output.
- **`dir.ErrCorrupt`.** `HeadObject` and `GetObject` of an object that cannot be read return an error wrapping it; it matches none of the not-found sentinels. A `PutObject` of the key replaces the object.
- **`dir.ErrStaleData`.** `PutObject` returns it together with the `ObjectInfo` when the object was written but the data file it replaced could not be removed. The object reads back as written.
- **`dir.ErrRootRequired`.** `NewProvider` returns it when no root was given, including `WithRoot(nil)`. It wraps `storage.ErrNotInitialized`.
- **Nil option.** `NewProvider` given a nil `Option` returns an error wrapping `storage.ErrInvalidOption` and creates nothing in the root.

## Wiring

```go
app := gas.NewApp(
    gas.WithSingletonService[storage.Provider](storages3.NewProvider()),
    gas.WithSingletonService[storage.Presigner](storages3.NewPresigner()),
)
```

Directory backend:

```go
root, err := os.OpenRoot("/var/lib/myapp/storage")
if err != nil {
    return err
}
defer root.Close()

app := gas.NewApp(
    gas.WithSingletonService[storage.Provider](storagedir.NewProvider(storagedir.WithRoot(root))),
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
    if errors.Is(err, storage.ErrKeyNotFound) {
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

`storagetest.MockProvider` and `storagetest.MockPresigner` are recording mocks.

- **Zero value.** Ready to use.
- **Delegation.** Each method records a `Call`, then delegates to its `Fn` field (`CreateBucketFn`, `HeadBucketFn`, `DeleteBucketFn`, `ListBucketsFn`, `HeadObjectFn`, `PutObjectFn`, `GetObjectFn`, `DeleteObjectFn`, `ListObjectsFn`, `PresignUploadURLFn`, `PresignDownloadURLFn`). When that field is nil it returns its zero results: `nil, nil`, or `nil` for `DeleteBucket`, which returns only an error.
- **Assertions.** `CallCount(method)` counts recorded calls, and `Reset()` clears them.
- **Concurrency.** Safe for concurrent use.

```go
mock := &storagetest.MockProvider{
    GetObjectFn: func(_ context.Context, _ *storage.GetObjectInput) (*storage.Object, error) {
        return nil, storage.ErrKeyNotFound
    },
}
app := gas.NewApp(gas.WithServiceInstance[storage.Provider](mock))
```
