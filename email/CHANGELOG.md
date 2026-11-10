# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.6.0] - 2026-11-08

v0.6.0 rewrites the module around one contract, Amazon SES's rules,
that every sender keeps, written in the `email` package's `doc.go` and
checked by a shared conformance suite that runs against the memory
sender and an SES API v2 wire fake. Each package's `doc.go` is its
behavioral contract and lists its errors verbatim; read it before
upgrading.

### Added

- **`email/memory`, an in-process outbox** with the same rules and the
  same template rendering as the SES sender, for tests and local
  development: `Messages` returns copies of what was sent, normalized,
  and `Reset` empties the outbox; `MaxMessages` and `MaxBytes` limits
  (`ErrOutboxFull`). The outbox outlives `Close` and a restart.
- **Messages are checked before anything is sent**, as `email.CheckEmail`
  does: one address per entry, read with net/mail and normalized
  (display names kept and encoded as RFC 2047 when not ASCII, IDN
  domains converted to Punycode only when they convert back, ASCII
  domains in lower case, local parts ASCII only); a sender and 1 to 50
  recipients; a subject without control characters; at least one body;
  valid UTF-8; SES's header rules; a 40 MB size estimate.
- In `email`: `CheckEmail`, `NormalizeAddress`, `CheckHeader`,
  `IsReservedHeader`, `MessageSize`, and the limits `MaxRecipients`,
  `MaxHeaders`, `MaxHeaderNameLength`, `MaxHeaderValueLength`,
  `MaxHeaderLength` and `MaxMessageSize`.
- Sentinel errors in `email`: `ErrNilMessage`, `ErrInvalidAddress`,
  `ErrNoRecipients`, `ErrTooManyRecipients`, `ErrInvalidSubject`,
  `ErrEmptyBody`, `ErrInvalidBody`, `ErrInvalidHeader`,
  `ErrMessageTooLarge`, `ErrTemplate`, `ErrNotInitialized`,
  `ErrAlreadyInitialized`, `ErrNilLogger`, `ErrNilTemplates`,
  `ErrNilConfig`, `ErrNoConfigProvider` and `ErrInvalidConfig`.
- **`gas.Email.Headers` is sent** by the SES sender, checked against
  SES's rules; a header that would set an address, the subject, the MIME
  structure, or redirect replies, receipts or bounces is refused
  (KI-034).
- `ses` sentinels `ErrNoRegion`, `ErrNoCredentials`, `ErrNilClient`,
  `ErrConnect`, `ErrCommand`, `ErrRejected`, `ErrNotVerified`,
  `ErrThrottled`, `ErrSendingPaused` and `ErrAccessDenied`: a failed
  send carries a fixed reason told by the network error or the SES error
  code, never the AWS SDK's error.
- `ses` settings: `SessionToken`, `OpTimeout` (the bound of each send,
  retries included, default 10s) and `MaxRetries` (0 to 10, default 2),
  which holds even when `AWS_MAX_ATTEMPTS` or `AWS_RETRY_MODE` is set.
- `Service.Config` on both senders returns a copy of the configuration
  the last successful `Init` resolved, defaults included, with
  `FromEmail` normalized, or the zero `Config` before one; `Close` and a
  failed `Init` keep it. The SES secret key and session token stay a
  `Secret`.
- `ses.ServiceName`, `ses.DefaultOpTimeout`, `DefaultMaxRetries` and
  `MaxRetries`; `memory.ServiceName` and its defaults.

### Changed

- **BREAKING: `ses` sends with the SES API v2.** It uses
  `github.com/aws/aws-sdk-go-v2/service/sesv2` and one `SendEmail`
  request with Simple content. `Service.Client` returns an
  `*sesv2.Client`, and `WithClient` takes the exported `ses.API`, whose
  `SendEmail` is the SES API v2 one. The credentials need the
  `ses:SendEmail` permission.
- **BREAKING: what is sent is the normalized copy `CheckEmail`
  returns.** An empty body is left out of the message instead of sent as
  an empty part, and a subject that is not ASCII is sent as RFC 2047
  encoded words.
- **BREAKING: `SendFromTemplate` no longer changes the caller's
  message.** It renders into a copy, in the order subject, text, HTML
  (it was subject, HTML, text), into a buffer bounded by
  `MaxMessageSize`, and cuts one trailing CRLF or LF of a rendered
  subject. A failed template is `email.ErrTemplate`, and the error names
  the field and the template, never the data or the engine's error.
  With gas/template v0.6.0 a store holds only names ending in one of its
  extensions, `.html` by default, where v0.5.6 stores read or stored any
  name, so a subject or text template named `.txt` now fails as
  `template failed: subject "welcome-subject.txt": fetch failed`. Build
  the store with `.txt` among its extensions, such as
  `memory.NewStore(memory.WithExtensions(".html", ".txt"))` of
  gas/template/memory.
- **BREAKING: lifecycle sentinels.** Before the first successful `Init`
  every method returns `email.ErrNotInitialized` (`Send` panicked and
  `CheckReady` reported ready); `Init` on an initialized sender returns
  `email.ErrAlreadyInitialized`; after `Close`, until `Init` succeeds
  again, `email.ErrClosed`. `Close` then `Init` restarts a sender.
- **BREAKING: `email.ErrClosed` is `service closed`** (was
  `email: service is closed`). Every error a sender returns starts with
  its service name and the operation, such as
  `gas/email/ses: send: rejected (MessageRejected)`, and wraps a
  sentinel.
- **BREAKING: `Close` drains.** It refuses new sends at once and returns
  once every send in flight has returned; when its context ends first,
  it cancels them, which return `email.ErrClosed`, and returns an error
  wrapping the context's error. `Close` used to only set a flag.
- **BREAKING: every method honors its context**, and returns the
  context's error, wrapped, when it has ended before the call.
- **BREAKING: `ses.Settings.Region` and `FromEmail` are no longer
  required.** Without a region the AWS SDK's default chain decides, and
  a region it does not find fails `Init` with `ses.ErrNoRegion`. Without
  `FromEmail`, every message needs a `From`. `FromEmail`, when set, must
  be one email address, and `Init` normalizes it.
- **BREAKING: `ses.Settings.SecretAccessKey` is a `ses.Secret`**, an
  alias of `gas.Secret`. Configuration binds into it as into a string.
- **BREAKING: `ses.Settings.Endpoint` must be an `http` or `https` URL
  with a host and nothing after it but `/`.**
- **BREAKING: the constructor checks its dependencies.** It fails with
  `email.ErrNilTemplates` for a nil `gas.TemplateProvider`,
  `email.ErrNilLogger` for a nil logger, and `email.ErrNilConfig` for
  `WithConfig(nil)`; a nil `ConfigProvider` is accepted, and `Init` then
  requires `WithConfig`. A nil client given to `WithClient` fails `Init`
  with `ses.ErrNilClient`.
- **BREAKING: a configuration given to `WithConfig` is validated** as a
  bound one is, copied, and never changed. `Init` resolves the
  configuration afresh each time it runs.
- With only one of the static keys set, `ses` uses the default chain
  and logs `static credentials incomplete, using the default chain` at
  warn level. A failure to retrieve credentials is `ses.ErrNoCredentials`.
- `ses.Service.Client` returns nil when the sender is not initialized,
  after `Close` included.
- The logs change: `service initialized` and `service closed`, at info
  level (they were `ses email service initialized` and
  `ses email service closed`). The `ses` initialized log carries whether
  an endpoint, a client and a default sender were given, the credential
  source, `op_timeout` and `max_retries` besides the region.
- **BREAKING: `emailtest.MockEmail.Calls` is a method.** The exported
  `Calls` field is gone; `Calls()` returns a deep copy. Each call records
  a copy of the message, which shares no slice or map with the caller's.

### Removed

- **BREAKING:** `ses.NewWithCustomProvider`. `ses.New` takes any
  `gas.TemplateProvider`.
- **BREAKING:** `email.IsErrClosed`; use `errors.Is(err, email.ErrClosed)`.
- **BREAKING:** `ses.Config` no longer embeds `gas.WithGasEnv`, which
  core removes.
- The `claude-skill/gas-email` directory.

### Fixed

- `ses.Service` methods no longer panic with a nil pointer dereference
  before `Init`; they return `email.ErrNotInitialized` (KI-036).
- Custom headers are no longer dropped without an error (KI-034).
- A restart no longer races sends in flight.

### Security

- No error a sender makes and no log it writes holds an address, a
  subject, a body, a header value, template data, an endpoint or a
  credential. The AWS SDK's default logger, which writes raw transport
  errors holding the endpoint, is silenced.
- Addresses, display names, the subject and header values are checked
  so that a CR or an LF cannot reach a header of the message.
- The secret key and the session token no longer print through fmt,
  encoding/json, YAML encoders or log/slog, which write them as
  `[redacted]`, and refuse gob encoding. The access key ID is never
  logged.
- The client `Init` builds reads no reply past 1 MiB, so an endpoint
  cannot make it read without bound.
- **Requires Go 1.27.3.** The `go` directive moves from 1.27 to 1.27.3,
  the first 1.27 release with the fix for GO-2026-6809 (CVE-2026-111943):
  `crypto/x509` now rejects intermediate certificates known to be
  compromised. A build with an older toolchain switches to 1.27.3 under
  `GOTOOLCHAIN=auto` and fails under `GOTOOLCHAIN=local`.

## [0.5.6] - 2026-10-19

The tags `email/v0.4.1` to `email/v0.5.6` shipped their changes under
`[Unreleased]`. This section holds them, and covers every change since
0.3.0, which was released from the separate
`github.com/gasmod/gas-email` repository.

### Changed

- **BREAKING: import path.** The module moved from
  `github.com/gasmod/gas-email` to `github.com/gasmod/gas/email`, in
  email/v0.4.1.
- **BREAKING: Go 1.27 is required.** The `go` directive moves from 1.26.1
  to 1.27, in email/v0.5.0, as core now uses generic methods.
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

## 0.3.0 - 2026-07-03

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

[Unreleased]: https://github.com/gasmod/gas/compare/email/v0.6.0...HEAD
[0.6.0]: https://github.com/gasmod/gas/releases/tag/email/v0.6.0
[0.5.6]: https://github.com/gasmod/gas/releases/tag/email/v0.5.6
