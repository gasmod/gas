# github.com/gasmod/gas/log/zerolog

```go
import "github.com/gasmod/gas/log/zerolog"
```

Package zerolog provides a gas.Logger over a zerolog.Logger. It is a
package of its own so that only an app that uses it depends on zerolog.
The events follow the contract of package github.com/gasmod/gas/log.

## Usage

```go
zl := zerolog.New(os.Stdout).With().Timestamp().Logger()

logger, err := gaszerolog.New(gaszerolog.WithLogger(&zl))()
if err != nil {
	return err
}

logger.Info("order placed").Str("order", "A-1").Int("items", 3).Send()
```

Here zerolog is github.com/rs/zerolog and gaszerolog this package. The
line written is the JSON object with `level` `info`, `order`, `items`,
the time and `message` `order placed`. In an app, register the
constructor with gas.WithSingletonService[gas.Logger].

## Levels

Trace, Debug, Info, Warn and Error are zerolog's TraceLevel, DebugLevel,
InfoLevel, WarnLevel and ErrorLevel. An event at a level the logger does
not enable is a shared no-op event, so its fields cost nothing.

## Events

Each field is encoded when its method is called, on the caller's
goroutine. An event is sent at most once: Send drops the zerolog event,
which zerolog then reuses for another, so every method called after Send,
Send included, does nothing and cannot reach that other event. With
zerolog's Caller on the logger, the caller written is the line that
called Send.

The encodings are zerolog's: Err with a nil error leaves the field out,
and Err otherwise writes the Error text; Any with an error does the
same as Err, and Any with another value writes zerolog's Interface
encoding, its JSON; Duration writes zerolog's Dur, in
zerolog.DurationFieldUnit, milliseconds unless changed.

A logger from With carries the fields added to its context, before each
event's own; its parent is unchanged. Calling Logger again on the same
context, after adding more fields, gives a second logger with them all;
the first is unchanged.

Flush does nothing: zerolog writes each event on Send. A writer that
buffers, such as a diode writer, is flushed by its owner.

## Concurrency

A Logger is safe for concurrent use, as far as its writer is; events
and the contexts from With are not.

## Errors

`ErrNilLogger`, which is log.ErrNilLogger, to be tested with
`errors.Is`: WithLogger was given nil. The error, as returned:

```go
gas/log: logger is nil
```

## API

Run `go doc -all github.com/gasmod/gas/log/zerolog` for every declaration, or `go doc github.com/gasmod/gas/log/zerolog.<Name>` for one.
