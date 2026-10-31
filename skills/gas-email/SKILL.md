---
name: gas-email
description: >
  Reference documentation for the gas/email Go package
  (github.com/gasmod/gas/email), the email sending service for the Gas
  ecosystem. Use this skill when writing, reviewing, or debugging Go code that
  uses gas/email for sending emails via AWS SES (API v2) or the in-memory
  outbox, including templated emails with gas.TemplateProvider integration.
  Covers the ses and memory sub-packages, emailtest mock, gas.EmailProvider
  and gas.ReadyReporter implementation, sentinel errors, address and header
  validation, DI wiring, configuration binding, static and default AWS
  credential chains, custom endpoint support for emulators, and template
  rendering with html/template and text/template. Make sure to use this skill
  whenever working with email sending in the Gas ecosystem, even if the user
  doesn't explicitly mention gas/email; any code that imports gasmod/gas/email
  or references gas.EmailProvider should trigger this skill.
---

# Gas Email Package Reference

Email sending service for the Gas ecosystem. Provides `gas.EmailProvider`
implementations backed by Amazon SES (API v2) and by an in-memory outbox,
plus a test mock. The rules are SES's, so a message the memory sender accepts
is one the SES sender would send.

```
import email "github.com/gasmod/gas/email"
import emailses "github.com/gasmod/gas/email/ses"
import emailmem "github.com/gasmod/gas/email/memory"
import "github.com/gasmod/gas/email/emailtest"
```

## Backends

| Backend | Package            | Service name       | Use case                                     |
|---------|--------------------|--------------------|----------------------------------------------|
| SES     | `gas/email/ses`    | `gas/email/ses`    | Production (SES API v2, or an emulator such as aws-ses-v2-local) |
| Memory  | `gas/email/memory` | `gas/email/memory` | Tests and local development (keeps messages) |

Both implement `gas.Service`, `gas.EmailProvider`, and `gas.ReadyReporter`.
`CheckReady` does no I/O: it fails only when the sender is not initialized or
its context has ended.

## EmailProvider Interface

Defined in the gas core package:

```go
type EmailProvider interface {
    Send(ctx context.Context, msg *Email) error
    SendFromTemplate(ctx context.Context, msg *TemplatedEmail) error
}
```

### Email

```go
type Email struct {
    From     string
    ReplyTo  string
    Subject  string
    TextBody string
    HTMLBody string
    Headers  map[string]string
    To       []string
    Cc       []string
    Bcc      []string
}
```

### TemplatedEmail

```go
type TemplatedEmail struct {
    Data            any    // data passed to Execute
    SubjectTemplate string // template name for subject (text/template)
    TextTemplate    string // template name for text body (text/template)
    HTMLTemplate    string // template name for HTML body (html/template)
    Email                  // embedded Email struct
}
```

All three template fields are optional. If a template field is empty, the
corresponding value on the embedded `Email` is used as-is.

## Contract (every sender)

- A nil error means **accepted for delivery**, not delivered.
- `Send` checks the message (as `email.CheckEmail` does) before sending; a
  message that fails is never sent. The sender's `FromEmail` setting is used
  when `From` is empty.
- What is sent is a normalized copy. **Send and SendFromTemplate never change
  the caller's message.**
- Order of checks: nil message (`ErrNilMessage`), then state, then context,
  then (SendFromTemplate) templates, then the message rules.
- Lifecycle: `ErrNotInitialized` before the first successful Init,
  `ErrAlreadyInitialized` from Init while open, `ErrClosed` after Close until
  Init succeeds again. Close waits for sends in flight; if its context ends
  first it cancels them and returns the context's error (SES may still have
  accepted them).
- Constructors refuse a nil `gas.TemplateProvider` (`ErrNilTemplates`), a nil
  logger (`ErrNilLogger`) and `WithConfig(nil)` (`ErrNilConfig`).

### Message rules

- **Addresses:** From, ReplyTo, and each To/Cc/Bcc entry hold **one**
  address, bare or with a display name (`Alice <alice@example.com>`).
  `email.NormalizeAddress` returns the form sent: non-ASCII display names as
  RFC 2047, non-ASCII domains as Punycode, ASCII domains lower-cased.
  Non-ASCII local parts are refused (no SMTPUTF8). Match allow/deny lists
  against `NormalizeAddress` output.
- At least one recipient (`ErrNoRecipients`), at most `email.MaxRecipients`,
  50, across To, Cc and Bcc (`ErrTooManyRecipients`).
- **Subject** required, valid UTF-8, no control characters (CR, LF, NUL...)
  (`ErrInvalidSubject`). At least one of TextBody/HTMLBody (`ErrEmptyBody`),
  each valid UTF-8 (`ErrInvalidBody`); an empty body is left out.
- **Headers** are sent: at most 15, names of printable ASCII but `:`, values
  of printable ASCII (encode non-ASCII yourself, such as with
  `mime.QEncoding`). Names the module or SES sets are refused in any case
  (`From`, `To`, `Subject`, `Reply-To`, `Return-Path`, `Content-*`,
  `Resent-*`, `X-SES-*`, ...; see `email.IsReservedHeader`)
  (`ErrInvalidHeader`). Check one with `email.CheckHeader(name, value)`.
- **Size:** an estimate (`email.MessageSize`) above `email.MaxMessageSize`
  (40,000,000 bytes) is `ErrMessageTooLarge`.

### Templates

`SendFromTemplate` fetches each named template from the `gas.TemplateProvider`
with `Get(ctx, name)` on every call, parses it alone (it cannot call another),
executes it with `Data` and renders into a copy: SubjectTemplate and
TextTemplate with text/template, HTMLTemplate with html/template. One trailing
newline of a rendered subject is cut. Fields render subject, text, HTML; the
first failure ends the call with nothing sent. A failure is `ErrTemplate`,
naming the field and template name but never the data or engine error. The
template stores accept only `.html` names by default; give the store
`WithExtensions(".html", ".txt")` to use `.txt` subject and text templates.

## Sentinel Errors

The root `email` package defines (test with `errors.Is`):

```go
email.ErrNilMessage, email.ErrInvalidAddress, email.ErrNoRecipients,
email.ErrTooManyRecipients, email.ErrInvalidSubject, email.ErrEmptyBody,
email.ErrInvalidBody, email.ErrInvalidHeader, email.ErrMessageTooLarge,
email.ErrTemplate,
email.ErrNotInitialized, email.ErrAlreadyInitialized, email.ErrClosed,
email.ErrNilTemplates, email.ErrNilLogger,
email.ErrNilConfig, email.ErrNoConfigProvider, email.ErrInvalidConfig
```

There is no `IsErrClosed` helper; use `errors.Is(err, email.ErrClosed)`.
Errors start with the service name and operation (`send` or `send from
template`) and never hold an address, subject, body, header value, template
data, endpoint or credential.

## SES Backend

### Constructor

```go
func New(opts ...Option) func(gas.TemplateProvider, gas.ConfigProvider, gas.Logger) (*Service, error)
```

The container must resolve `gas.TemplateProvider` itself (register the
template store under that type). There is no `NewWithCustomProvider`. A nil
ConfigProvider is accepted, and Init then requires `WithConfig`.

### Options

| Option                    | Description                                                      |
|---------------------------|------------------------------------------------------------------|
| `WithConfig(cfg *Config)` | Use this config (a copy, checked against the same tags) instead of binding |
| `WithClient(client API)`  | Use this client (`*sesv2.Client` or a fake of `emailses.API`); region, endpoint, credentials and `maxretries` then have no effect. Nil fails Init with `ErrNilClient` |

### Lifecycle (gas.Service)

| Method  | Description                                                      |
|---------|------------------------------------------------------------------|
| `Name`  | Returns `emailses.ServiceName`, `"gas/email/ses"`                |
| `Init`  | Resolves config, builds the client from the AWS default chain, retrieves credentials within `optimeout` (`ErrNoCredentials` when none, `ErrNoRegion` when no region); sends no SES request |
| `Close` | Refuses new sends, waits for sends in flight, closes idle connections |

### Behavior

- **Send** is one SESv2 `SendEmail` with Simple content; SES assembles the
  MIME. Non-ASCII subjects are sent as RFC 2047 encoded words. No
  configuration set, tags or attachments. The SES message ID is not kept.
- **Timeouts:** every send runs within its context and `optimeout`, retries
  included. SES has no idempotency key, so a retried send can deliver twice:
  set `maxretries` to 0 where a duplicate is worse than a failure.
- **No reply** wraps `emailses.ErrConnect` (`timed out`, `connection
  refused`, `unreachable`). An SES error reply wraps `emailses.ErrCommand`
  and, by kind, `ErrNotVerified`, `ErrRejected`, `ErrThrottled`,
  `ErrSendingPaused` or `ErrAccessDenied`. The SDK error is not reachable
  with `errors.As`.
- **Client accessor:** `Client() *sesv2.Client` returns the SDK client for
  operations beyond the interface (such as `GetAccount`), or nil when not
  initialized or `WithClient` gave another `API`. Its errors are the SDK's,
  unredacted.

### Config

Section `email`; keys `email.region`, `email.endpoint`, `email.fromemail`,
`email.accesskeyid`, `email.secretaccesskey`, `email.sessiontoken`,
`email.optimeout`, `email.maxretries`.

```go
type Config struct {
    Email Settings
}

type Settings struct {
    Region          string        // empty = AWS default chain (AWS_REGION, profile)
    Endpoint        string        // http(s) URL with host and nothing after but "/" (emulator)
    FromEmail       string        // default sender; one address; empty = every message needs From
    AccessKeyID     string        // static key ID, used only with SecretAccessKey
    SecretAccessKey Secret        // = gas.Secret; prints, marshals and logs as [redacted]
    SessionToken    Secret        // temporary credentials
    OpTimeout       time.Duration `validate:"gt=0"`         // default 10s
    MaxRetries      int           `validate:"min=0,max=10"` // default 2 (3 attempts)
}

func DefaultConfig() *Config
```

Static credentials apply only when both keys are set; with one missing, the
default chain is used and Init logs a warning naming the missing keys. The
credentials need `ses:SendEmail`, and the sender identity must be verified in
SES.

## Memory Backend

```go
func New(opts ...Option) func(gas.TemplateProvider, gas.ConfigProvider, gas.Logger) (*Service, error) // WithConfig
func (s *Service) Messages() []gas.Email // copies, in send order
func (s *Service) Reset()                // empties the outbox
```

Keeps each accepted message, normalized, in an outbox that works in any state
and outlives Close. Past `MaxMessages` (default 10000) or `MaxBytes` (default
256 MiB, at least 1 MiB) a send fails with `emailmem.ErrOutboxFull`. Config
section `email`: `FromEmail`, `MaxMessages`, `MaxBytes`.

## Test Mock

```go
type MockEmail struct {
    SendFn             func(ctx context.Context, msg *gas.Email) error
    SendFromTemplateFn func(ctx context.Context, msg *gas.TemplatedEmail) error
    CheckReadyFn       func(ctx context.Context) error
}
```

With a field nil, the method returns nil. Each call is recorded with a copy
of the message (`Data` is the same value, not a copy). The mock checks no
rule and renders nothing; to test the rules, use the memory sender.

| Method                  | Description                                |
|-------------------------|--------------------------------------------|
| `Calls() []Call`        | Deep copy of the recorded calls, in order  |
| `CallCount(method) int` | Count calls by method name (e.g. `"Send"`) |
| `Reset()`               | Forget all recorded calls                  |

Methods may run from several goroutines; setting an `Fn` field while a
method runs is a data race.

## DI Wiring Patterns

Register the sender under `gas.EmailProvider`: the container resolves a
dependency by its exact registered type. `New` also needs a registered
`gas.TemplateProvider`, `gas.ConfigProvider` (unless `WithConfig` is given),
and `gas.Logger`.

### SES backend

```go
app := gas.NewApp(
    gas.WithSingletonService[gas.TemplateProvider](
        tmplfs.NewStore(emailTemplates, tmplfs.WithExtensions(".html", ".txt")),
    ),
    gas.WithSingletonService[gas.EmailProvider](emailses.New()),
)
```

### With explicit config

```go
cfg := emailses.DefaultConfig()
cfg.Email.Region = "eu-west-1"
cfg.Email.FromEmail = "Shop <noreply@example.com>"
cfg.Email.Endpoint = "http://localhost:8005" // aws-ses-v2-local; emulators only

app := gas.NewApp(
    gas.WithSingletonService[gas.EmailProvider](emailses.New(emailses.WithConfig(cfg))),
)
```

### Consuming via gas.EmailProvider

```go
type Service struct {
    email gas.EmailProvider
}

func (s *Service) Welcome(ctx context.Context, to string) error {
    return s.email.SendFromTemplate(ctx, &gas.TemplatedEmail{
        SubjectTemplate: "emails/welcome-subject.txt",
        HTMLTemplate:    "emails/welcome.html",
        Data:            map[string]any{"Email": to},
        Email:           gas.Email{To: []string{to}},
    })
}
```

### Using the test mock

```go
mock := &emailtest.MockEmail{
    SendFn: func(ctx context.Context, msg *gas.Email) error {
        return nil
    },
}
// inject mock as gas.EmailProvider; assert with mock.CallCount("Send")
```
