# github.com/gasmod/gas/email/emailtest

```go
import "github.com/gasmod/gas/email/emailtest"
```

Package emailtest holds a mock of gas.EmailProvider, `MockEmail`, for
tests of code that sends email.

## Usage

The mock records each call, and returns what the function in the field
named after the method returns:

```go
m := &emailtest.MockEmail{
	SendFn: func(context.Context, *gas.Email) error { return errBounce },
}
err := m.Send(ctx, &gas.Email{To: []string{"alice@example.com"}})
// err is errBounce, and m.CallCount("Send") is 1
```

The mock keeps none of the email package's rules: it checks no
address, renders no template, and sends nothing. To test against those
rules without a server, use gas/email/memory, whose outbox holds what
SES would be sent.

## Defaults

With a field nil, its method succeeds: Send, SendFromTemplate and
CheckReady return nil.

## Calls

Each method appends a `Call` with its name and its arguments after the
context, before it calls its field. The arguments:

- Send: a copy of the *gas.Email.
- SendFromTemplate: a copy of the *gas.TemplatedEmail.
- Either, for a nil message: nil.
- CheckReady: nothing.

A copy shares no slice or map with the caller's message, so a caller
that reuses the message does not change a recorded call. The Data of a
TemplatedEmail is the same value, not a copy.

To read the record:

- `MockEmail.Calls` returns a deep copy of the calls in the order they
were made, so that changing what it returns does not change them
either.
- `MockEmail.CallCount` counts those of one method.
- `MockEmail.Reset` forgets them all.

## Concurrency

The methods may be called from several goroutines at once, as the calls
are kept under a lock, which Calls, CallCount and Reset take too.
Setting a field while a method runs is a data race.

## API

Run `go doc -all github.com/gasmod/gas/email/emailtest` for every declaration, or `go doc github.com/gasmod/gas/email/emailtest.<Name>` for one.
