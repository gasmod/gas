# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
