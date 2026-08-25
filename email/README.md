# gas/email

[![Test](https://github.com/gasmod/gas/actions/workflows/test.yml/badge.svg)](https://github.com/gasmod/gas/actions/workflows/test.yml) [![Go Reference](https://pkg.go.dev/badge/github.com/gasmod/gas/email.svg)](https://pkg.go.dev/github.com/gasmod/gas/email) ![Go Version](https://img.shields.io/github/go-mod/go-version/gasmod/gas?filename=email/go.mod) [![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)

Email service for the [Gas](https://github.com/gasmod/gas) ecosystem. Provides a `gas.EmailProvider` implementation
backed by AWS SES, plus a test mock for use in unit tests.

## Install

```bash
go get github.com/gasmod/gas/email
```

## Backends

| Backend | Package                            | Use case                          |
|---------|------------------------------------|-----------------------------------|
| SES     | `github.com/gasmod/gas/email/ses`  | Production (AWS SES / LocalStack) |

The SES backend implements `gas.Service` and `gas.EmailProvider`.

## Usage

### SES backend

```go
package main

import (
	"github.com/gasmod/gas"
	emailses "github.com/gasmod/gas/email/ses"
)

func main() {
	app := gas.NewApp(
		gas.WithSingletonService[*emailses.Service](emailses.New()),
		// ...
	)

	app.Run()
}
```

With custom configuration:

```go
cfg := &emailses.Config{
	Email: emailses.Settings{
		Region:    "eu-west-1",
		FromEmail: "noreply@example.com",
		Endpoint:  "http://localhost:4566", // LocalStack
	},
}

emailses.New(emailses.WithConfig(cfg))
```

With a pre-configured AWS client:

```go
emailses.New(emailses.WithClient(mySESClient))
```

If your DI container resolves a concrete template provider type rather than
the `gas.TemplateProvider` interface, use the generic variant:

```go
emailses.NewWithCustomProvider[*mytemplates.Service]()
```

### Dependency injection

Services receive the email sender through `gas.EmailProvider` via constructor injection:

```go
type Service struct {
	email gas.EmailProvider
}

func New(email gas.EmailProvider) *Service {
	return &Service{email: email}
}

func (s *Service) Init() error {
	ctx := context.Background()
	_ = s.email.Send(ctx, &gas.Email{
		To:      []string{"user@example.com"},
		Subject: "Welcome",
		HTMLBody: "<h1>Hello!</h1>",
	})
	return nil
}
```

### Sending emails

```go
err := s.email.Send(ctx, &gas.Email{
	To:       []string{"alice@example.com"},
	Cc:       []string{"bob@example.com"},
	Bcc:      []string{"log@example.com"},
	Subject:  "Order Confirmation",
	HTMLBody: "<h1>Thank you for your order</h1>",
	TextBody: "Thank you for your order",
	ReplyTo:  "support@example.com",
	From:     "orders@example.com", // overrides cfg.Email.FromEmail
})
```

### Sending from templates

Templates are fetched from the injected `gas.TemplateProvider`, parsed with Go's `html/template` (for HTML)
or `text/template` (for subject and text), and executed with the provided data:

```go
err := s.email.SendFromTemplate(ctx, &gas.TemplatedEmail{
	SubjectTemplate: "welcome-subject",
	HTMLTemplate:    "welcome-html",
	TextTemplate:    "welcome-text",
	Data:            map[string]string{"Name": "Alice"},
	Email: gas.Email{
		To: []string{"alice@example.com"},
	},
})
```

All three template fields are optional. If a template field is empty, the corresponding field on the
embedded `Email` struct is used as-is.

## Config

If `WithConfig` is not provided, the backend automatically binds configuration from the `gas.ConfigProvider` injected
via DI. This lets you drive email settings from environment variables or a config file without any explicit wiring.

### SES config

| Field                   | Default | Description                                                |
|-------------------------|---------|------------------------------------------------------------|
| `Email.Region`          |         | AWS region (required)                                      |
| `Email.FromEmail`       |         | Default sender address (required)                          |
| `Email.Endpoint`        |         | Custom endpoint URL (e.g. LocalStack); empty = default AWS |
| `Email.AccessKeyID`     |         | Static AWS access key; empty = default credential chain    |
| `Email.SecretAccessKey` |         | Static AWS secret key; empty = default credential chain    |

## Readiness

The SES `Service` implements `gas.ReadyReporter`. `CheckReady` returns
`email.ErrClosed` once `Close` has been invoked (so a Kubernetes
readinessProbe depools the pod during shutdown/drain) and `nil` otherwise.
`HealthReporter` is intentionally not implemented — the SES client is
stateless and has no broken-state condition that a process restart would
resolve.

## Sentinel Errors

The root `email` package defines sentinel errors used by all backends:

```go
email.ErrClosed    // returned when an operation is attempted on a closed service
email.IsErrClosed(err) // helper: errors.Is(err, ErrClosed)
```

## Testing

The `emailtest` package provides a mock implementation of `gas.EmailProvider`:

```go
import "github.com/gasmod/gas/email/emailtest"

mock := &emailtest.MockEmail{}
mock.SendFn = func(ctx context.Context, msg *gas.Email) error {
	return nil
}

// pass mock as gas.EmailProvider
// assert calls:
if mock.CallCount("Send") != 1 {
	t.Error("expected one Send call")
}
```
