# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed

- **BREAKING: the SES constructors return an error.** `ses.New` and
  `ses.NewWithCustomProvider` now return `func(<dependencies>) (*Service,
  error)`, the `CONVENTIONS.md` shape. They cannot fail yet, so they return
  nil; the change is the shape. Registering them with
  `gas.WithSingletonService` is unchanged, since the container accepts a
  trailing error. Code that calls the returned function directly must take
  the second result (gas#106).
- **BREAKING: `ses.Config.Validate` is removed; the rules are validate
  tags.** `Settings.Region` and `Settings.FromEmail` carry
  `validate:"required"`. `ConfigProvider.Bind` already validates the struct it
  binds, so `Init` no longer runs a check of its own. A config passed with
  `WithConfig` is used as given, as `gas.Config` is when an App has no
  `ConfigProvider`. The error for an invalid bound config is now the
  validator's (`Field validation for 'Region' failed on the 'required' tag`)
  under `gas/email/ses: config binding:` instead of `Email.Region must not be
  empty` (gas#116).
- `ses.Service` no longer logs an error it also returns: an invalid
  configuration and a failed send. The caller that handles the error logs it
  once. An invalid configuration is returned as
  `gas/email/ses: invalid configuration: <cause>`.
- **BREAKING:** `ses.Service` implements the context-aware `gas.Service`:
  `Init(ctx)` and `Close(ctx)`. `Init` loads the AWS configuration and
  fetches the credentials under the caller ctx. `Close` ignores ctx.
- `Init` fetches the AWS credentials, so a missing or broken credential setup
  (no static keys, no environment variables, profile, web identity, or
  instance role) fails startup with an error naming the credential lookup,
  instead of failing the first call. With the default chain this can make a
  network call at startup (for example to the instance metadata service).
  Not applied when `WithClient` supplies the client.

### Security

- **Requires Go 1.27.3.** The `go` directive moves from 1.27 to 1.27.3,
  the first 1.27 release with the fix for GO-2026-6809 (CVE-2026-111943):
  `crypto/x509` now rejects intermediate certificates known to be
  compromised. A build with an older toolchain switches to 1.27.3 under
  `GOTOOLCHAIN=auto` and fails under `GOTOOLCHAIN=local`.

## [0.3.0] - 2026-07-03

First open source release. Versions prior to 0.3.0 were developed in a private
repository; this entry summarizes the package as published.

### Added

- **`ses.Service`** — an AWS SES-backed implementation of `gas.EmailProvider`
  and `gas.ReadyReporter` (`CheckReady` returns `email.ErrClosed` once
  `Close` has been called, so probes depool the pod during shutdown/drain).
  Constructed via `ses.New(opts...)` for DI-based injection of
  `gas.TemplateProvider`, `gas.ConfigProvider`, and `gas.Logger`, with a
  generic `ses.NewWithCustomProvider[T]` variant for custom
  `gas.TemplateProvider` implementations.
- **`Send`** — sends a `gas.Email` (`From`, `ReplyTo`, `Subject`, `TextBody`,
  `HTMLBody`, `To`/`Cc`/`Bcc`) directly through SES, falling back to the
  configured `Settings.FromEmail` when `From` is empty.
- **`SendFromTemplate`** — renders a `gas.TemplatedEmail`'s
  `SubjectTemplate`/`TextTemplate` (via `text/template`) and `HTMLTemplate`
  (via `html/template`) against arbitrary `Data`, fetching the raw template
  content from the injected `gas.TemplateProvider`, then sends the result.
- **Configuration** — `Config`/`Settings` (`Region`, `FromEmail`,
  `Endpoint`, `AccessKeyID`, `SecretAccessKey`), bindable via
  `gas.ConfigProvider` when `WithConfig` is not supplied, with `Region` and
  `FromEmail` required on `Validate`.
- **Custom endpoint support** — `Settings.Endpoint` overrides the SES base
  endpoint (e.g. for LocalStack or other SES-compatible test servers).
- **Credential handling** — `AccessKeyID`/`SecretAccessKey` configure static
  AWS credentials; when either is empty, the default AWS credential chain
  is used instead.
- **`WithClient`** to inject a pre-configured SES client (or any type
  satisfying the minimal `sesClient` interface), and **`Client()`** to
  access the underlying `*ses.Client` directly for advanced operations
  beyond the `gas.EmailProvider` interface.
- **`email.ErrClosed`** sentinel error (with `email.IsErrClosed` helper),
  returned by `Send`, `SendFromTemplate`, and `CheckReady` once the service
  has been closed.
- **`emailtest` package** with `MockEmail`, a configurable mock of
  `gas.EmailProvider` and `gas.ReadyReporter` that records calls
  (`CallCount`, `Reset`) and delegates to per-method `Fn` fields when set.

[Unreleased]: https://github.com/gasmod/gas/email/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/gasmod/gas/email/releases/tag/v0.3.0

