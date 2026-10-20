# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Breaking changes

- **`gas.StorageProvider` is replaced by interfaces in this module.**
  `storage.Provider` covers `CreateBucket`, `ListBuckets`, `HeadObject`,
  `PutObject`, `GetObject`, `DeleteObject` and `ListObjects`. `storage.Presigner` covers
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
- **Listing** in both backends. `ListObjects` returns one page of a
  bucket's objects with S3 ListObjectsV2 semantics (`Prefix`, `Delimiter`
  and common prefixes, `StartAfter`, `MaxKeys` up to 1000, and a
  continuation token), and `ListBuckets` one page of buckets (`Prefix`,
  `MaxBuckets` up to 10000, and a continuation token). s3 requests
  URL-encoded keys and decodes them, so keys with characters XML cannot
  carry are listed. dir reads every name in the bucket on each call, and
  records each bucket's name under `.gas/buckets` when `CreateBucket`
  runs; a bucket created before that, or whose record a crash left
  damaged, is left out of `ListBuckets` until `CreateBucket` of it runs
  again. `storagetest.MockProvider` gains
  `ListBucketsFn` and `ListObjectsFn`.
  - `storage.ErrInvalidLimit`, for a `MaxKeys` under 1 or a `MaxBuckets`
    outside 1 to 10000.
  - `storage.ErrInvalidContinuationToken`, for an empty token, or one the
    dir backend did not issue.
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
- **One unnameable bucket no longer makes `dir.ListBuckets` fail.** A bucket
  whose name record is missing (one created by a version that kept none) or
  holds a name that hashes to another bucket (one a crash left damaged) is
  left out of the listing instead of failing the whole call, so the other
  buckets stay listable; `CreateBucket` of that bucket rewrites the record
  and brings it back, and its objects were reachable through the other
  methods throughout. A name record that cannot be read at all, or that is
  not a regular file, still fails the call, and those errors now name the
  record by its path under the root rather than by the bucket's hash alone.
  Note that a bucket left out is left out silently: this release has no way
  to report it, since the backend takes no logger and the listing output
  carries no field for it.

### Known limitations

- **One unreadable object still makes `dir.ListObjects` fail for its whole
  bucket**, hiding every other object in it. It is the same shape as the
  `ListBuckets` fault fixed above, and is left as it stands: unreadable
  object metadata means real corruption, and skipping it silently would hide
  data loss. The `dir` package documentation states the behaviour.

## [0.3.0] - 2026-07-03

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

[Unreleased]: https://github.com/gasmod/gas/storage/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/gasmod/gas/storage/releases/tag/v0.3.0
