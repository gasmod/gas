---
name: gas-email
description: >
  Reference documentation for the gas/email Go package
  (github.com/gasmod/gas/email) — the email sending service for the Gas
  ecosystem. Use this skill when writing, reviewing, or debugging Go code that
  uses gas/email for sending emails via AWS SES, including templated emails
  with gas.TemplateProvider integration. Covers the ses sub-package, emailtest
  mock, gas.EmailProvider and gas.ReadyReporter implementation, sentinel errors, DI wiring,
  configuration binding, development mode logging, static and default AWS
  credential chains, custom endpoint support for LocalStack, and template
  rendering with html/template and text/template. Make sure to use this skill
  whenever working with email sending in the Gas ecosystem, even if the user
  doesn't explicitly mention gas/email — any code that imports gasmod/gas/email
  or references gas.EmailProvider should trigger this skill.
---

# Gas Email Package Reference

Email sending service for the Gas ecosystem. Provides a `gas.EmailProvider`
implementation backed by AWS SES, plus a reusable test mock.

```
import email "github.com/gasmod/gas/email"
import emailses "github.com/gasmod/gas/email/ses"
import "github.com/gasmod/gas/email/emailtest"
```

## Backends

| Backend | Package          | Service name    | Use case                          |
|---------|------------------|-----------------|-----------------------------------|
| SES     | `gas/email/ses`  | `gas/email/ses` | Production (AWS SES / LocalStack) |

The SES backend implements `gas.Service` and `gas.EmailProvider`.

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
    SubjectTemplate string // template name for subject (text/template)
    TextTemplate    string // template name for text body (text/template)
    HTMLTemplate    string // template name for HTML body (html/template)
    Data            any    // data passed to template.Execute
    Email                  // embedded Email struct
}
```

All three template fields are optional. If a template field is empty, the
corresponding value on the embedded `Email` is used as-is.

## Sentinel Errors

The root `email` package defines sentinel errors:

```go
email.ErrClosed        // returned when an operation is attempted on a closed service
email.IsErrClosed(err) // helper: errors.Is(err, ErrClosed)
```

## SES Backend

### Constructor

```go
func New(opts ...Option) func(gas.TemplateProvider, gas.ConfigProvider, gas.Logger) *Service
```

`New` captures options and returns a DI-injectable constructor. The returned
func receives `gas.TemplateProvider`, `gas.ConfigProvider`, and `gas.Logger`
from the DI container.

```go
func NewWithCustomProvider[T gas.TemplateProvider](opts ...Option) func(T, gas.ConfigProvider, gas.Logger) *Service
```

`NewWithCustomProvider` is a generic variant for cases where the DI container
resolves a concrete type `T` that satisfies `gas.TemplateProvider` rather than
the interface itself. Behaves identically to `New` otherwise.

### Options

| Option                          | Description                                                      |
|---------------------------------|------------------------------------------------------------------|
| `WithConfig(cfg *Config)`       | Set configuration explicitly (skips config binding from DI)      |
| `WithClient(client sesClient)`  | Inject a pre-configured SES client (testing or custom AWS creds) |

### Lifecycle (gas.Service)

| Method  | Signature   | Description                                           |
|---------|-------------|-------------------------------------------------------|
| `Name`  | `() string` | Returns `"gas/email/ses"`                             |
| `Init`  | `() error`  | Validates config, creates AWS SES client              |
| `Close` | `() error`  | Marks service as closed                               |

### Readiness (gas.ReadyReporter)

| Method       | Signature                       | Description                                                            |
|--------------|---------------------------------|------------------------------------------------------------------------|
| `CheckReady` | `(ctx context.Context) error`   | Returns `email.ErrClosed` after `Close`; `nil` otherwise               |

The SES `Service` implements `gas.ReadyReporter` so a Kubernetes
readinessProbe can depool the pod during shutdown/drain. It does not
implement `gas.HealthReporter` — the SES client is stateless HTTP with no
broken-state condition that a process restart would resolve.

### Behavior

- **Send:** Calls SES `SendEmail`. Uses `msg.From` if set, otherwise falls
  back to `cfg.Email.FromEmail`. Supports To, Cc, Bcc, ReplyTo, Subject,
  HTMLBody, and TextBody. All content is sent as UTF-8.
- **SendFromTemplate:** Fetches raw template content from `gas.TemplateProvider`
  via `Get(name)`, parses it with `html/template` (for HTML bodies) or
  `text/template` (for subject and text bodies), executes with `msg.Data`,
  then delegates to `Send`. Each template field (SubjectTemplate,
  HTMLTemplate, TextTemplate) is optional — if empty, the corresponding
  field on the embedded `Email` is used directly.
- **Credential chain:** If both `AccessKeyID` and `SecretAccessKey` are set,
  static credentials are used. Otherwise, the default AWS credential chain
  is used (environment variables, shared config, IAM roles, etc.).
- **No upfront connection:** The AWS SES client is stateless HTTP — Init
  creates the client but does not verify connectivity. Connection errors
  surface at call time.
- **All methods return `email.ErrClosed`** when the service has been closed.

### Client accessor

```go
func (s *Service) Client() *ses.Client
```

Returns the underlying `*ses.Client` for advanced operations beyond the
`EmailProvider` interface. Returns nil if a custom `sesClient` was injected
via `WithClient` that is not an `*ses.Client`.

### Config

```go
type Config struct {
    env.WithGasEnv
    Email Settings
}

type Settings struct {
    Region         string // AWS region (required)
    Endpoint       string // optional custom endpoint (e.g. LocalStack)
    FromEmail      string // default sender address (required)
    AccessKeyID    string // static AWS key; empty = default credential chain
    SecretAccessKey string // static AWS secret; empty = default credential chain
}

func DefaultConfig() *Config   // zero-value defaults (no default region)
func (c *Config) Validate() error // rejects empty Region, empty FromEmail
```

## Test Mock

The `emailtest` package provides `MockEmail`, a configurable mock of
`gas.EmailProvider` for use in unit tests.

```go
import "github.com/gasmod/gas/email/emailtest"
```

### MockEmail

```go
type MockEmail struct {
    SendFn             func(ctx context.Context, msg *gas.Email) error
    SendFromTemplateFn func(ctx context.Context, msg *gas.TemplatedEmail) error
    CheckReadyFn       func(ctx context.Context) error
    Calls              []Call
}
```

Each method delegates to its `Fn` field if set, otherwise returns zero value.
All calls are recorded in `Calls` for assertions. Thread-safe.

| Method                  | Description                                |
|-------------------------|--------------------------------------------|
| `Reset()`               | Clear all recorded calls                   |
| `CallCount(method) int` | Count calls by method name (e.g. `"Send"`) |

## DI Wiring Patterns

### SES backend

```go
app := gas.NewApp(
    gas.WithSingletonService[*emailses.Service](emailses.New()),
)
```

### With explicit config

```go
app := gas.NewApp(
    gas.WithSingletonService[*emailses.Service](
        emailses.New(emailses.WithConfig(&emailses.Config{
            Email: emailses.Settings{
                Region:    "eu-west-1",
                FromEmail: "noreply@example.com",
                Endpoint:  "http://localhost:4566",
            },
        })),
    ),
)
```

### Consuming via gas.EmailProvider

Services receive email through the provider interface, never importing
gas/email backends directly:

```go
type Service struct {
    email gas.EmailProvider
}

func New(email gas.EmailProvider) *Service {
    return &Service{email: email}
}

func (s *Service) Init() error {
    // use s.email.Send, s.email.SendFromTemplate
    return nil
}
```

### Using the test mock

```go
mock := &emailtest.MockEmail{}
mock.SendFn = func(ctx context.Context, msg *gas.Email) error {
    return nil
}

// inject mock as gas.EmailProvider in tests
```
