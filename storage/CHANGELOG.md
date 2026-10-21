# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Breaking changes

- **`storage.Provider` gains `HeadBucket` and `DeleteBucket`.** Every
  implementation of the interface must add them.
- **`storage.Bucket` replaces `BucketInfo` and `BucketSummary`.** It has
  `Name`, `CreationDate`, `ID` and `Region`. `CreateBucket` returns
  `*Bucket`, and `ListBucketsOutput.Buckets` is `[]Bucket`. `Location` is
  gone: on s3 it held CreateBucket's `Location` response header, which is a
  path, not a place. dir no longer puts the bucket name in `ID`.
- **`ListBucketsOutput.ContinuationToken` is renamed
  `NextContinuationToken`**, as on `ListObjectsOutput`. On both, it is empty
  on the last page and only there.
- **`PutObjectInput` embeds `ObjectHeaders`**, which holds `ContentType`,
  `Metadata`, `Expires` and the new system headers. Keyed composite
  literals that set `ContentType` or `Metadata` directly still compile,
  since this module requires go 1.27, which accepts promoted fields in
  composite literals; so do reads and assignments. Unkeyed literals break.
- **`Expires` is a `*string`**, not a `*time.Time`, so a stored value that is
  not a date survives a round trip.

### Added

- **dir `GetObject` bodies are seekable for ranged reads too.** A ranged
  `Body` used to be a reader that could not seek, while a whole read gave
  an `*os.File`; both now implement `io.ReadSeeker`, a ranged one seeking
  within the returned bytes. The dir package documents it; the storage
  interface still promises only `io.ReadCloser`.
- **`Skipped` on `ListObjectsOutput` and `ListBucketsOutput`**: how many
  entries the listing left out because the backend could not read them,
  counted over the whole bucket (or every bucket), so every page repeats
  that count; a `ListObjects` page also adds any of its own objects found
  damaged while it was built. Always 0 on s3.
- **`dir.WithSkipHandler`**, which receives the path, relative to the
  root, and the reason of each entry a dir listing leaves out, each piece
  of debris `DeleteBucket` removes, and each file it leaves behind. Paths
  never appear in the listing output.
- **`dir.ErrCorrupt` and `dir.ErrUnnamedBucket`**, exported so callers can
  match them. `HeadObject` and `GetObject` of an object that cannot be read
  now return an error wrapping `dir.ErrCorrupt`; before, it matched no
  sentinel a caller could name. That includes an object whose metadata
  holds another key, which `GetObject` used to serve while a listing left
  it out.
- **Conditional writes.** `PutObjectInput` embeds `WriteCondition`:
  `IfNoneMatch` (`"*"` only) writes only a key with no object, and
  `IfMatch` only over the object with that ETag, quoted or not. A condition
  that does not hold is the new `storage.ErrPreconditionFailed`, and an
  `IfMatch` of a missing key `storage.ErrKeyNotFound`; nothing is written.
  A condition that cannot be evaluated is the new
  `storage.ErrInvalidCondition`. dir checks the condition under the key's
  lock, so it holds across processes sharing the root; an object without a
  recorded ETag matches no `IfMatch`. s3 sends `If-Match` and
  `If-None-Match`, and `PresignUploadURL` signs them.
- **`HeadBucket` and `DeleteBucket`** in both backends, with
  `HeadBucketInput` and `DeleteBucketInput`, and the sentinels
  `storage.ErrBucketNotEmpty` and `storage.ErrAccessDenied` (s3 only, for
  a 403). Deleting a missing bucket is `ErrBucketNotFound`, unlike
  deleting a missing key. dir's `DeleteBucket` refuses only a bucket that
  holds an object, and then removes nothing: unreadable metadata, metadata
  whose data file is gone and temp files of crashed writes do not count,
  and are removed with the bucket once no object was found, so none of
  them can make a bucket undeletable. It waits for a write of an existing
  key in progress rather than cutting it short; a write of a new key still
  copying its body finds the bucket gone and returns `ErrBucketNotFound`.
  It removes the bucket's name record, so a bucket created again gets a
  new `CreationDate`.
  `CreateBucket` and `DeleteBucket` of one name take a lock under
  `.gas/locks` and so never interleave, in one process or several.
  `storagetest.MockProvider` gains `HeadBucketFn` and `DeleteBucketFn`.
- **System headers.** `CacheControl`, `ContentDisposition`,
  `ContentEncoding` and `ContentLanguage` on `ObjectHeaders`, stored by both
  backends and returned on `ObjectInfo` with `Expires`.
- **`ObjectInfo.ETag` and `ObjectSummary.ETag`**, unquoted. `PutObject`
  returns it on both backends. dir computes the hex MD5 of the body as it
  writes it; an object written by an earlier version has an empty ETag.
- **dir `PutObject` returns the object it wrote**, exactly as `HeadObject`
  then reads it: ETag, size, modification time, content type, metadata and
  headers, with invalid UTF-8 already replaced and no map shared with the
  input. It used to return an empty `ObjectInfo`.
- **`storage.ErrInvalidHeader`.** s3 `PutObject` and `PresignUploadURL`
  return it for an `Expires` that is not an IMF-fixdate, such as
  `Wed, 21 Oct 2026 07:28:00 GMT` with the right weekday. The AWS SDK sends
  `Expires` only as a time and writes it in that form, so any other text,
  the RFC 850 and asctime forms included, would be stored rewritten.
  Nothing is sent or signed.
- **s3 `ListBuckets`** fills `ID` and `Region` from the ARN and region S3
  sends.

### Changed

- **dir object metadata** records the ETag and the system headers. An
  earlier version of the backend reading the same directory ignores those
  fields and sees the objects without them.

### Fixed

- **dir stores `Expires`.** It used to be accepted and dropped, and s3 sent
  it but had no field to return it in.
- **One unnameable bucket no longer makes `dir.ListBuckets` fail.** A bucket
  whose name record is missing (one created by a version that kept none) or
  holds a name that hashes to another bucket (one a crash left damaged) is
  left out of the listing instead of failing the whole call, so the other
  buckets stay listable; `CreateBucket` of that bucket rewrites the record
  and brings it back, and its objects were reachable through the other
  methods throughout. A name record that cannot be read at all, or that is
  not a regular file, still fails the call, and those errors now name the
  record by its path under the root rather than by the bucket's hash alone.
  It is counted in `Skipped` and reported to the skip handler.
- **One unreadable object no longer makes `dir.ListObjects` fail for its
  whole bucket.** An object whose metadata does not decode or holds another
  key, or whose data file is gone, is left out, counted in `Skipped` and
  reported to the skip handler, so the rest of the bucket stays listable
  and the loss is still visible. An I/O error reading metadata still fails
  the call. Each listing now stats every data file to find these.

### Security

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

[Unreleased]: https://github.com/gasmod/gas/compare/storage/v0.5.6...HEAD
[0.5.6]: https://github.com/gasmod/gas/compare/storage/v0.5.5...storage/v0.5.6
[0.5.5]: https://github.com/gasmod/gas/releases/tag/storage/v0.5.5
