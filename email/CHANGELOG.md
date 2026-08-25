# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

