# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.6.0] - 2026-11-08

v0.6.0 rewrites the module around one contract that every backend keeps,
written in the `storage` package's `doc.go` and checked by a conformance
suite that runs against memory, dir, an S3 fake and LocalStack. Each
package's `doc.go` is its behavioral contract and lists its errors
verbatim; read it before upgrading: most of the API changed.

### Breaking changes

- **The backends are services again.** `dir.New`, `memory.New`, `s3.New`
  and `s3.NewPresigner` return constructors of the shape
  `func(gas.ConfigProvider, gas.Logger) (*T, error)`, and each backend
  implements `gas.Service` (`Name`, `Init`, `Close`) and
  `gas.ReadyReporter` (`CheckReady`). Every method but `Init`, `Close`
  and `Name` returns `storage.ErrNotInitialized` before the first
  successful `Init`, and `storage.ErrClosed` after `Close` until `Init`
  succeeds again; `Close` waits for the calls in flight, and cancels them
  when its context ends. The buckets and objects outlive a restart.
  `dir.NewProvider` and `s3.NewProvider` are removed, and
  `s3.ProviderOption` is now `s3.Option`. A constructor fails with
  `storage.ErrNilLogger` for a nil logger, `storage.ErrNilOption` for a
  nil option and `storage.ErrNilConfig` for `WithConfig(nil)`.
- **Configuration is resolved on each `Init`.** Each backend has a
  `Config` with a `Storage` field of type `Settings`, a `DefaultConfig`,
  and a `WithConfig` option (`WithPresignerConfig` for the presigner)
  that replaces binding from the `gas.ConfigProvider`. `Init` checks the
  `validate` tags, then the backend's rules, such as that a configured
  `storage.bucket` keeps the bucket rules; a failure wraps
  `storage.ErrInvalidConfig`, and a missing `ConfigProvider` without
  `WithConfig` is `storage.ErrNoConfigProvider`.
- **`storage.Provider` changes.** `CreateBucket` and `DeleteObject`
  return only an error, `PutObject` returns `*PutObjectOutput` (`ETag`,
  `Version`, `Size`) instead of `*ObjectInfo`, and the interface gains
  `HeadBucket`, `DeleteBucket` and `CopyObject`. Every implementation must
  follow.
- **`storage.Bucket` replaces `BucketInfo` and `BucketSummary`.** It has
  `CreationDate`, `Name` and `Region`. `HeadBucket` returns it, and
  `ListBucketsOutput.Buckets` is `[]Bucket`. `ID` and `Location` are gone.
- **`ListBucketsOutput.ContinuationToken` is renamed
  `NextContinuationToken`**, as on `ListObjectsOutput`. On both, it is
  empty on the last page and only there.
- **`PutObjectInput` embeds `ObjectHeaders` and `WriteCondition`.**
  `ObjectHeaders` holds `ContentType`, `CacheControl`,
  `ContentDisposition`, `ContentEncoding`, `ContentLanguage`, `Expires`
  and `Metadata`. Keyed composite literals that set `ContentType` or
  `Metadata` directly still compile, as do reads and assignments; unkeyed
  literals break.
- **`Expires` is a `*string`**, not a `*time.Time`, and must be an
  IMF-fixdate exactly as `http.TimeFormat` writes it, on every backend.
- **`ObjectInfo` embeds `HeaderValues`**, which holds the content type and
  every header field written, and gains `ETag`; `ObjectSummary` gains
  `ETag` too. The ETag is unquoted.
- **The `Response*` fields move from `HeadObjectInput` to
  `PresignDownloadURLInput`**, in the new embedded `ResponseHeaders`,
  with `ResponseExpires` a `*string`. `HeadObject` and `GetObject` no
  longer take them.
- **Sentinel errors.** The texts no longer start with `storage: `, and
  every error a backend returns starts with its service name and the
  operation, such as `gas/storage/memory: get object: object not found`.
  - `ErrKeyNotFound` and `ErrNotFound` are replaced by one
    `ErrObjectNotFound`, which `HeadObject` and `GetObject` both return.
  - `ErrInputRequired` is renamed `ErrNilInput`, `ErrInvalidOption`
    `ErrNilOption`, and `ErrInvalidContinuationToken` `ErrInvalidToken`.
  - `ErrInvalidLimit` and `ErrInvalidTTL` are removed: an argument out of
    its bounds is `ErrInvalidArgument`.
  - `ErrInvalidRange` now means a `Range` that is not one byte range; a
    range that selects no byte is the new `ErrRangeNotSatisfiable`.
  - `dir.ErrRootRequired` and `dir.ErrStaleData` are removed. A data file
    of a replaced object that cannot be removed no longer fails the
    write: it is logged at warn level and passed to the skip handler.
- **Inputs keep S3's rules on every backend, before any I/O.** A key
  that holds a control character, the tab included, is
  `ErrInvalidKey`. Metadata names must be HTTP tokens and are stored and
  returned in lower case, so two names that differ only in case are
  refused; values hold only printable ASCII, space and tab, and are never
  encoded; names and values hold at most 2048 bytes together
  (`ErrInvalidMetadata`). Header field values keep the same character
  rules and hold at most 1024 bytes (`ErrInvalidHeader`). A `Range` that is not one byte range is
  `ErrInvalidRange` rather than ignored. The checks are exported (see
  Added).
- **Presigned uploads need a `ContentLength`**, 0 to
  `MaxPresignUploadSize` (5 GiB), which the URL signs, as it signs the
  Content-Type at every length. A TTL out of 1s to 7 days is
  `ErrInvalidArgument`.
- **Continuation tokens are opaque and sealed.** A token holds its
  listing's last entry sealed with a key each `Init` draws at random,
  bound to the listing that issued it, on every backend, s3 included. A
  token from before a restart, from another `Provider` or from another
  listing is `ErrInvalidToken`.
- **dir has a new layout.** Keys and bucket names are still stored under
  hashes, now with a key index, a lifecycle lock per bucket and a layout
  marker. `Init` refuses a directory that holds buckets but no marker, as
  one written by an earlier version, with the new `dir.ErrOldLayout`;
  copy its objects with that version's store. `dir.Provider.Root` is
  removed.
- **dir reads its directory from `storage.dir`.** `Init` opens it with
  `os.OpenRoot` and `Close` closes it. `WithRoot` is still accepted, and
  `storage.dir` must then be empty; the store never closes a root given
  to `WithRoot`.
- **s3 options.** `WithClient` takes an `s3.API`, the interface of the
  requests the store sends, so an `*s3.Client` or a fake fits, and
  `WithPresignClient` an `s3.PresignAPI`. `WithUploaderClient` and
  `WithPresignerS3Client` are removed. A nil client fails `Init` with
  `s3.ErrNilClient`. `Provider.Client` returns nil when the store was
  given another `API`.
- **s3 `Settings`.** `SecretAccessKey` and `SessionToken` are
  `s3.Secret` (`gas.Secret`). `Init` fails with `s3.ErrNoRegion` when
  neither the `region` setting nor the AWS SDK's default chain gives a
  region, and with `s3.ErrNoCredentials` when it cannot retrieve
  credentials. Static credentials are used only when both
  `accesskeyid` and `secretaccesskey` are set.
- **storagetest.** `Calls` is a method that returns a deep copy, and each
  recorded call holds a deep copy of its input. With a field nil, a
  method that returns an output returns a zero one rather than nil, but
  `HeadObject` and `GetObject`, which return `storage.ErrObjectNotFound`. The `Fn` fields follow
  the new method signatures.

### Added

- **`memory`**, a store in the process's memory with S3's rules, for
  tests and local development: settings `storage.bucket`,
  `storage.maxbytes` (default 256 MiB) and `storage.maxobjects` (default
  100000), at most 10000 buckets, and `memory.ErrStorageFull` for a write
  past them.
- **`CopyObject`** on every backend and on `storagetest.MockProvider`
  (`CopyObjectFn`), with `CopyObjectInput` and `CopyObjectOutput`. It
  copies an object within a bucket or to another, with the source's
  header fields and metadata, or with `Replace` those of the input, and
  writes the copy whole or not at all. A copy onto itself without
  `Replace`, header fields or metadata set without `Replace`, and a source
  longer than `MaxCopySize` (5 GiB) are `ErrInvalidArgument`. dir never
  holds two keys' locks at once; s3 sends S3's CopyObject pinned to the
  source's ETag, and starts again when the source changed meanwhile.
  `storage.CheckCopy` and `storage.CheckCopySize` hold the rules, and the
  `s3.API` interface includes `CopyObject`.
- **`Config()`** on `dir.Provider`, `memory.Provider` and `s3.Provider`:
  a copy of the configuration the last successful `Init` bound, defaults
  included, or the zero `Config` before one. A failed `Init` and `Close`
  keep it.
- **`HeadBucket` and `DeleteBucket`** on every backend, with
  `HeadBucketInput` and `DeleteBucketInput`, and the sentinels
  `ErrBucketNotEmpty` and `ErrAccessDenied`. Deleting a missing bucket is
  `ErrBucketNotFound`, unlike deleting a missing key. dir's
  `DeleteBucket` refuses only a bucket that holds an object, and then
  removes nothing; what no read can use is removed with the bucket and
  passed to the skip handler. `CreateBucket` and `DeleteBucket` of one
  name never interleave, across processes too.
- **Conditional writes.** `WriteCondition.IfNoneMatch` (`*` only) writes
  only a key that holds no object, and `IfMatch` only over the object
  with that ETag, quoted or not; `IfMatch` of a missing key is
  `ErrObjectNotFound`. A condition that does not hold is
  `ErrPreconditionFailed`, and nothing is written; one that cannot be
  evaluated is `ErrInvalidCondition`. dir evaluates the condition under
  the key's lock, so it holds across processes; s3 sends `If-None-Match`
  and `If-Match`, and `PresignUploadURL` signs them.
- **Header fields.** `CacheControl`, `ContentDisposition`,
  `ContentEncoding` and `ContentLanguage`, stored by every backend and
  returned with `ContentType` and `Expires`.
- **`ETag`** on `ObjectInfo`, `ObjectSummary`, `PutObjectOutput` and
  `CopyObjectOutput`: the hex MD5 of the contents on memory and dir, and
  on s3 for an object written in one part without SSE-KMS.
- **The input rules, exported.** `CheckKey`, `CheckBucket`,
  `ResolveBucket`, `CheckObject`, `CheckRead`, `CheckPut`, `CheckDelete`,
  `CheckCopy`, `CheckCopySize`, `CheckHeaders`, `CheckResponseHeaders`,
  `CheckMetadata`, `CheckCondition`, `CheckLength`, `CheckListObjects`,
  `CheckListBuckets`, `CheckPresignUpload`, `CheckPresignDownload`,
  `CheckPresignTTL`, `ParseRange` with `ByteRange`, and `ParseExpires`,
  with the request types they return and the limits as constants
  (`MaxKeyLength`, `MinBucketLength`, `MaxBucketLength`,
  `MaxMetadataSize`, `MaxHeaderSize`, `MaxPartNumber`, `MaxListKeys`,
  `MaxListBuckets`, `DefaultContentType`, `DefaultPresignTTL`,
  `MinPresignTTL`, `MaxPresignTTL`, `MaxPresignUploadSize`,
  `MaxCopySize`).
- **Sentinels** `ErrInvalidMetadata`, `ErrInvalidHeader`,
  `ErrInvalidCondition`, `ErrInvalidArgument`, `ErrPreconditionFailed`,
  `ErrRangeNotSatisfiable`, `ErrBackend`, `ErrAlreadyInitialized`,
  `ErrClosed`, `ErrNilLogger`, `ErrNilConfig`, `ErrNoConfigProvider` and
  `ErrInvalidConfig` in `storage`.
- **`PresignURLResponse.Expires`**, when the URL stops being valid at the
  latest.
- **dir:** `WithSkipHandler`, which receives the path and the reason of
  each entry the store passes over, removes or leaves behind;
  `ErrCorrupt`, for an object that cannot be read; `ErrUnnamedBucket`,
  for a bucket `ListBuckets` cannot name; `ErrNilRoot`;
  `ServiceName`; and the `storage.sweeplimit` setting
  (`DefaultSweepLimit`, 100000). `Init` removes the leftovers of crashed
  writes and indexes again an object that lost its index entry, each
  under its lock taken without waiting. A `GetObject` body is an
  `io.ReadSeeker`, a ranged one seeking within the bytes selected.
- **s3:** the settings `storage.usepathstyle` (default true when
  `endpoint` is set), `storage.optimeout`, the bound of each request,
  retries included (`DefaultOpTimeout`, 10s), and `storage.maxretries`
  (`DefaultMaxRetries`, 2, at most `MaxRetries`, 10), which holds whatever
  `AWS_MAX_ATTEMPTS` and `AWS_RETRY_MODE` say; the sentinels `ErrConnect`,
  `ErrThrottled`, `ErrWrongRegion`, `ErrConflict` and
  `ErrUploadNotAborted`; `ServiceName` and `PresignerName`; and
  `Presigner.CheckReady`. `Provider.CheckReady` sends a `HeadBucket` of
  the configured bucket. An upload in parts that fails is aborted with
  `AbortMultipartUpload` on a context of its own.

### Changed

- **`CheckBucket` no longer refuses S3's reserved starts and ends**, such
  as `xn--` and `-s3alias`: S3 refuses them itself, and the dir and memory
  backends accept them.
- **dir syncs every write to disk.** Once `PutObject` or `CopyObject`
  returns, the object survives a crash of the process and a loss of power,
  on a file system that honors fsync; on darwin a sync is F_FULLFSYNC.
- **dir lists from a key index**, reading the metadata of the objects of
  the page only, rather than every name in the bucket on each call.
- **dir opens only regular files.** A metadata file, a data file and a
  name record are opened only when they are regular files, never through
  a symlink at their name, and without blocking on a FIFO or a device; a
  metadata file or a name record is read no further than 64 KiB.
- **dir checks the context between reads of a body**, so a `PutObject`
  whose context ends stops reading its body and writes nothing.
- **The AWS SDK modules are updated**, and the transfer manager is pinned
  at v0.4.19.

### Fixed

- **dir stores `Expires`.** It used to be accepted and dropped.
- **One bucket dir cannot name no longer fails `ListBuckets`.** A bucket
  whose name record is missing or holds another name is left out and
  passed to the skip handler with `ErrUnnamedBucket` until `CreateBucket`
  of it rewrites the record.
- **One object dir cannot read no longer fails `ListObjects`.** It is left
  out and passed to the skip handler with `ErrCorrupt`.
- **A nil client given to an s3 option fails `Init`** with
  `s3.ErrNilClient` (KI-051). It used to discard a client an earlier
  option gave, while the doc comments said it was ignored.

### Security

- **Errors and logs hold no application data.** No error a backend
  returns and no log it writes holds a key, a bucket name, metadata, a
  header value, a path, an endpoint, a URL, an upload ID, a continuation
  token or a credential. s3 no longer keeps the AWS SDK's error, which
  holds the URL, nor S3's reply message, and gives the AWS SDK a logger
  that discards everything.
- **s3 secrets are redacted.** The secret key and the session token print,
  marshal to JSON or YAML, and log with log/slog as `[redacted]`, and
  encoding/gob fails on them.
- **s3 caps its replies.** The client `Init` builds reads no reply past
  16 MiB but an object's contents.
- **Requires Go 1.27.3.** The `go` directive moves from 1.27 to 1.27.3,
  the first 1.27 release with the fix for GO-2026-6809 (CVE-2026-111943):
  `crypto/x509` now rejects intermediate certificates known to be
  compromised. A build with an older toolchain switches to 1.27.3 under
  `GOTOOLCHAIN=auto` and fails under `GOTOOLCHAIN=local`.

## [0.5.6] - 2026-10-19

### Breaking changes

- **`storage.Provider` gains `ListBuckets` and `ListObjects`.** Every
  implementation of the interface must add them.

### Added

- **Listing** in both backends. `ListObjects` returns one page of a
  bucket's objects with S3 ListObjectsV2 semantics (`Prefix`, `Delimiter`
  and common prefixes, `StartAfter`, `MaxKeys` up to 1000, and a
  continuation token), and `ListBuckets` one page of buckets (`Prefix`,
  `MaxBuckets` up to 10000, and a continuation token). s3 requests
  URL-encoded keys and decodes them, so keys with characters XML cannot
  carry are listed. dir reads every name in the bucket on each call, and
  records each bucket's name under `.gas/buckets` when `CreateBucket`
  runs; a bucket created before that, or whose record a crash left
  damaged, fails `ListBuckets` until `CreateBucket` of it runs again. `storagetest.MockProvider` gains
  `ListBucketsFn` and `ListObjectsFn`.
  - `storage.ErrInvalidLimit`, for a `MaxKeys` under 1 or a `MaxBuckets`
    outside 1 to 10000.
  - `storage.ErrInvalidContinuationToken`, for an empty token, or one the
    dir backend did not issue.

## [0.5.5] - 2026-10-19

The tags `storage/v0.4.1` to `storage/v0.5.5` shipped their changes under
`[Unreleased]`, and the entries were first written at 0.5.5. This section
holds them, and covers every change since 0.3.0, which was released from
the separate `github.com/gasmod/gas-storage` repository.

### Breaking changes

- **Import path.** The module moved from `github.com/gasmod/gas-storage` to
  `github.com/gasmod/gas/storage`, in storage/v0.4.1. The old module path is
  no longer published.
- **Go 1.27 is required.** The `go` directive moves from 1.26.1 to 1.27, in
  storage/v0.5.0, as core now uses generic methods.
- **`gas.StorageProvider` is replaced by interfaces in this module.**
  `storage.Provider` covers `CreateBucket`, `HeadObject`, `PutObject`,
  `GetObject` and `DeleteObject`. `storage.Presigner` covers
  `PresignUploadURL` and `PresignDownloadURL`. Each method takes an input
  struct (`PutObjectInput` and so on) in place of a key and
  `gas.StorageOption`s. `BaseObjectInput.Key` is a `string`, and a nil
  `Bucket` means the configured default bucket.
- **Constructors.** `s3.New` and `s3.Service` are replaced by
  `s3.NewProvider` / `s3.Provider` and `s3.NewPresigner` / `s3.Presigner`,
  each a constructor taking `gas.ConfigProvider` and returning an error:
  `func(gas.ConfigProvider) (*Provider, error)`. The replacements for
  `WithConfig` are `WithClient` and `WithUploaderClient` (provider), and
  `WithPresignerS3Client` and `WithPresignClient` (presigner). Configuration
  is always bound from `gas.ConfigProvider`, and `gas.Logger` is no longer
  needed.
- **Lifecycle.** The providers no longer implement `gas.Service` or
  `gas.ReadyReporter`. `Name`, `Init`, `Close` and `CheckReady` are gone.
- **Removed sentinels:**
  - `storage.ErrClosed`.
  - `storage.ErrBucketRequired`. A missing bucket is now
    `storage.ErrInvalidBucket`.
- **`s3.Config`.** `Storage.Region` is optional: empty uses the AWS SDK's
  default resolution. `Config.Validate`, `DefaultConfig` and the `Settings`
  type are removed.
- **`storagetest`.** `MockStorage` is replaced by `MockProvider` and
  `MockPresigner`.
- **`dir` backend.** Rewritten on a hashed on-disk layout and incompatible
  with directories written by earlier versions.
  - `dir.Service`, `dir.New`, `dir.WithPresigner`, `dir.ErrInvalidPath` and
    `dir.ErrNoPresigner` are removed.
  - Keys are opaque strings: `../x`, `/abs` and keys differing only in case
    are distinct objects.
  - Buckets are created with `CreateBucket`, not by making a directory.
- **Error messages** have the form `gas/storage/<backend>/<component>: <op>:
  <cause>` in both backends. The messages gas builds no longer quote the
  object key. An s3 error can still wrap an AWS SDK error whose text
  includes the request URL, with the bucket and key.

### Added

- **`dir.Provider`.** A filesystem backend rooted in an `os.Root`, with
  per-key locking that also excludes other processes, and atomic
  overwrites.
- **Sentinels:**
  - `storage.ErrBucketNotFound`, since storage/v0.5.2. Object operations
    return it when the bucket does not exist, except the s3 backend's
    `HeadObject`, which S3 answers for a missing bucket exactly as for a
    missing key, and which therefore returns `storage.ErrNotFound`.
  - `storage.ErrInputRequired`, `storage.ErrInvalidKey`,
    `storage.ErrInvalidBucket` and `storage.ErrNotFound`.
  - `storage.ErrBucketExists`. s3 `CreateBucket` returns it when the name
    belongs to another account (S3 BucketAlreadyExists). Creating a bucket
    the caller already owns succeeds in every backend and region, so the dir
    backend never returns it.
  - `dir.ErrStaleData`. `dir.PutObject` returns it together with the
    object info when the object was written but the data file it replaced
    could not be removed.
  - `dir.ErrRootRequired`.
  - `storage.ErrVersionNotFound`. s3 `GetObject` returns it for a
    `Version` that does not exist (S3 NoSuchVersion). It matches neither
    `ErrKeyNotFound` nor `ErrNotFound`. s3 `HeadObject` reports an unknown
    version as `ErrNotFound`, since HEAD responses carry no error code. A
    malformed version ID may get a 400 from AWS with no sentinel.
  - `storage.ErrInvalidOption`. Backend constructors return it for a nil
    option.
  - `storage.ErrInvalidRange`. `GetObject` and `HeadObject` return it for a
    `Range` that cannot be satisfied: its first byte at or past the end, or
    `bytes=-0`. s3 maps S3's 416 to it.
  - `storage.ErrNotSupported`. dir `HeadObject`, `GetObject` and
    `DeleteObject` return it when `Version` is set, and `HeadObject` and
    `GetObject` when `PartNumber` is set, since dir neither versions objects
    nor stores them in parts.
  - `storage.ErrContentLengthMismatch`. `PutObject` returns it in both
    backends when `ContentLength` is set and `Body` yields a different
    number of bytes, or when `ContentLength` is negative. Nothing is stored,
    and an object already under the key is unchanged. A nil `ContentLength`
    still means the length is unknown.
  - `storage.ErrInvalidTTL`. `Presigner` methods return it for a TTL that
    is negative, under one second, or over seven days.
- **`s3.Config` field** `Storage.SessionToken`.
- **Range reads in the dir backend.** `GetObject` and `HeadObject` honor
  `Range` with RFC 9110 single-range semantics, matching S3 on the cases
  where S3 follows the RFC: `Size` is the selected length, a header that is
  not one valid byte range is ignored, and an unsatisfiable one is
  `storage.ErrInvalidRange`.
- **`CreateBucket`** in both backends. s3 creates the bucket in the client's
  region, sending the location constraint S3 requires outside us-east-1 and
  `aws-global`. For a bucket the caller already owns it succeeds, and the
  returned `BucketInfo` may be empty.
- **LocalStack test suite** for the s3 backend, against
  `localstack/localstack:4.14.0` pinned by digest.

### Changed

- **Nil options.** `s3.NewProvider`, `s3.NewPresigner` and
  `dir.NewProvider` return an error wrapping `storage.ErrInvalidOption`,
  naming the option's position, when given a nil option, instead of
  panicking.
- **Presign TTL.** A nil or zero TTL signs an explicit 15-minute expiry
  instead of relying on the SDK default, so it also overrides a default
  set on a presign client given with `WithPresignClient`. A TTL that is
  negative, under one second, or over seven days returns
  `storage.ErrInvalidTTL` instead of being signed (or, for a negative TTL,
  failing with an unwrapped SDK error). Fractional seconds are truncated.

### Fixed

- **s3 error mapping:** `CreateBucket` errors are now mapped to storage
  sentinels.
- **Uninitialized providers:** a provider or presigner not built by its
  constructor, including a nil pointer, returns `storage.ErrNotInitialized`
  from every method instead of panicking.
- **Key validation is the same on every platform.** Keys are checked as
  `/`-separated paths with package `path`, not `path/filepath`, so on
  Windows a backslash no longer acts as a separator: `a\.` and `.\` are
  valid keys everywhere, and `a/.` and `./` are invalid everywhere.
- **s3 NoSuchVersion** is mapped to `storage.ErrVersionNotFound` instead of
  carrying no sentinel.

## 0.3.0 - 2026-07-03

First open source release. Versions prior to 0.3.0 were developed in a private
repository; this entry summarizes the package as published.

### Added

- **`s3.Service`** — an S3-backed implementation of `gas.StorageProvider`
  (and S3-compatible backends: MinIO, LocalStack, DigitalOcean Spaces via a
  configurable `Endpoint`, which also enables path-style addressing).
  Constructed via `s3.New(opts...)` for DI-based injection of
  `gas.ConfigProvider` and `gas.Logger`.
- **Core operations** — `Upload`, `Download`, `Delete`, and `Head` (metadata
  only, no body), all accepting `gas.StorageOption`s for per-call bucket,
  content type, and metadata.
- **Presigned URLs** — `PresignDownloadURL` and `PresignUploadURL` generate
  time-limited GET/PUT URLs for direct client access without proxying
  through the app.
- **Per-call bucket resolution** — `Storage.Bucket` is optional; when unset,
  each call must supply a bucket via `gas.InBucket()`, returning
  `storage.ErrBucketRequired` otherwise. When set, it acts as the default,
  overridable per call.
- **Direct client access** via `Client()`, returning the underlying
  `*s3.Client` for advanced operations not covered by the provider
  interface.
- **`gas.ReadyReporter` implementation** — `CheckReady` issues a
  `HeadBucket` against the configured default bucket to verify credentials
  and reachability; when no default bucket is configured, readiness
  succeeds once `Init` completes, since there is no single bucket to probe.
- **Configuration** — `Config`/`Settings` (`Region`, `Bucket`,
  `AccessKeyID`, `SecretAccessKey`, `Endpoint`), bindable via
  `gas.ConfigProvider` or set explicitly with `WithConfig`, with `Region`
  required and `Bucket` optional.
- **Sentinel errors** — `ErrKeyNotFound` (returned by `Download`/`Head` for
  missing keys), `ErrClosed` (operation attempted after `Close`), and
  `ErrBucketRequired`.
- **`storagetest` package** with `MockStorage`, a configurable mock of
  `gas.StorageProvider` (and `gas.ReadyReporter`) that records calls and
  delegates to per-method `Fn` fields for tests that need custom behavior.

[Unreleased]: https://github.com/gasmod/gas/compare/storage/v0.6.0...HEAD
[0.6.0]: https://github.com/gasmod/gas/compare/storage/v0.5.6...storage/v0.6.0
[0.5.6]: https://github.com/gasmod/gas/compare/storage/v0.5.5...storage/v0.5.6
[0.5.5]: https://github.com/gasmod/gas/releases/tag/storage/v0.5.5
