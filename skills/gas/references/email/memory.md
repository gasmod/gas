# github.com/gasmod/gas/email/memory

```go
import "github.com/gasmod/gas/email/memory"
```

Package memory is an email sender that sends nothing: it keeps each
message in an outbox in the process's memory, for tests and local
development, as a gas.EmailProvider. `Service` implements gas.Service,
gas.EmailProvider and gas.ReadyReporter, and keeps the contract of the
email package, with the same rules and the same template rendering as
gas/email/ses, so a message the memory sender accepts is one the SES
sender would send.

## Usage

In an app, the container builds the sender from `New` and calls its
Init. By hand, with a configuration given in code:

```go
cfg := memory.DefaultConfig()
cfg.Email.FromEmail = "Shop <shop@example.com>"

m, err := memory.New(memory.WithConfig(cfg))(templates, nil, logger)
if err != nil {
	return err
}
if err := m.Init(ctx); err != nil {
	return err
}
defer m.Close(ctx)

err = m.Send(ctx, &gas.Email{
	To:       []string{"alice@example.com"},
	Subject:  "Your order",
	TextBody: "It ships today.",
})
sent := m.Messages()
```

After this sent holds one message, from `Shop <shop@example.com>` to
`alice@example.com`.

## Configuration

The settings live in the `email` section, in `Settings`:

- `email.fromemail`: the sender of a message whose From is empty, one
email address as the email package reads one, which Init
normalizes. Default: none, so every message needs a From.
- `email.maxmessages`: the most messages the outbox holds, at least
1. Default: `DefaultMaxMessages` (10000).
- `email.maxbytes`: the most bytes the messages of the outbox hold,
each counted as email.MessageSize estimates it, at least
`MinMaxBytes` (1 MiB). Default: `DefaultMaxBytes` (256 MiB).

`Service.Init` resolves the configuration each time it runs. Without
`WithConfig`, it binds `DefaultConfig` from the gas.ConfigProvider,
which checks the `validate` tags; a nil ConfigProvider is the error
email.ErrNoConfigProvider. With WithConfig, it takes a copy of the
given Config, never changing the caller's struct, and checks the same
tags. Then `fromemail`, when set, must be one email address. A failure
wraps email.ErrInvalidConfig and names the setting, never its value.

`Service.Config` returns a copy of the configuration the last successful
Init resolved, defaults included, or the zero Config before one; a
failed Init and Close leave it as it was. Its `fromemail` is the
normalized address.

## Outbox

A send that keeps every rule stores a copy of the normalized message
CheckEmail returns, which shares nothing with the caller's: From and
the other addresses as the email package normalizes them, an empty
list as nil, and the headers. `Service.Messages` returns copies of the
messages in the order they were sent, and `Service.Reset` empties the
outbox; both work in any state of the sender, and the outbox outlives
Close and a restart. A send that finds the outbox holding
`maxmessages` messages, or whose message would take it past
`maxbytes`, fails with `ErrOutboxFull` and stores nothing. A send that
a Close canceled stores nothing, even when its gas.TemplateProvider
ignored the cancellation and rendered. The subject is kept as given,
not encoded as the SES sender sends one that is not ASCII.
The sender logs no message, and no part of one.

## Lifecycle

A successful Init logs `service initialized` at info level with `from`,
whether a default sender is set, `max_messages` and `max_bytes`. Init
on an initialized sender returns email.ErrAlreadyInitialized and
changes nothing; a failed Init leaves the sender as it was.
`Service.Close` makes the sender closed, waits for the sends in flight,
such as a SendFromTemplate that waits on its gas.TemplateProvider, and
logs `service closed` at info level. When its context ends first, it
cancels them, logs `service closed, sends in flight canceled` at warn
level, and returns an error wrapping the context's error. Close on a
sender that is not initialized returns nil and logs nothing.

`New`'s constructor fails with email.ErrNilTemplates for a nil
gas.TemplateProvider, email.ErrNilLogger for a nil logger, and
email.ErrNilConfig for WithConfig(nil); a nil ConfigProvider is
accepted, and Init then requires WithConfig.

`Service.CheckReady` does no I/O: it fails only when the sender is not
initialized or its context has ended.

## Concurrency

A Service is safe for concurrent use. Init and Close run one at a time.
The outbox is kept under a lock, which no send holds while it renders.

## Errors

The sentinel error of this package, to be tested with `errors.Is`:

- `ErrOutboxFull`: the outbox holds `maxmessages` messages, or the
message would take it past `maxbytes`.

The other errors wrap a sentinel of the email package, as the email
package describes. Every error starts with `ServiceName` and the
operation, `send` standing below for both send operations:

```go
gas/email/memory: template provider is nil
gas/email/memory: logger is nil
gas/email/memory: config is nil
gas/email/memory: init: no config provider
gas/email/memory: init: invalid config: Email.FromEmail must be one email address
gas/email/memory: init: service already initialized
gas/email/memory: init: context canceled
gas/email/memory: ready: service not initialized
gas/email/memory: send: message is nil
gas/email/memory: send: service not initialized
gas/email/memory: send: service closed
gas/email/memory: send: context canceled
gas/email/memory: send: no recipients
gas/email/memory: send: outbox full
gas/email/memory: send from template: template failed: HTML "welcome.html": execute failed
gas/email/memory: close: context deadline exceeded
```

## API

Run `go doc -all github.com/gasmod/gas/email/memory` for every declaration, or `go doc github.com/gasmod/gas/email/memory.<Name>` for one.
