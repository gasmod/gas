# github.com/gasmod/gas/log/logtest

```go
import "github.com/gasmod/gas/log/logtest"
```

Package logtest provides `RecordingLogger`, a gas.Logger for tests that
keeps every event it sends, with its level, message and fields.

## Usage

```go
logger := logtest.New()

svc.Run(logger)

for _, e := range logger.Entries() {
	if e.Level == "error" {
		t.Errorf("unexpected error: %s %v", e.Msg, e.Fields)
	}
}
```

Entries returns the events sent so far, in order. The zero value of a
RecordingLogger is ready to use, as New's is.

## Entries

An `Entry` holds:

- the level, one of `trace`, `debug`, `info`, `warn` and `error`;
- the message;
- the fields by key, the values as they were given. A field given twice
keeps the last value.

Entries returns a copy, with copies of the field maps, so changing it
changes no later result.

An event is recorded by Send, and at most once: every method called
after Send, Send included, does nothing.

A logger from With records into the same entries as the logger it came
from, and puts the fields of its context on every event before the
event's own, so an event's field of the same key wins. Calling Logger
again on the same context, after adding more fields, gives a second
logger with them all; the first is unchanged. Entries of any of these
loggers returns them all.

Flush does nothing.

## Concurrency

A RecordingLogger is safe for concurrent use; events and the contexts
from With are not.

## API

Run `go doc -all github.com/gasmod/gas/log/logtest` for every declaration, or `go doc github.com/gasmod/gas/log/logtest.<Name>` for one.
