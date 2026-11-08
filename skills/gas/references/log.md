# github.com/gasmod/gas/log

```go
import "github.com/gasmod/gas/log"
```

Package log provides gas.Logger implementations over log/slog, and ships
log records in batches to an HTTP endpoint, encoded by a `Marshaler`
such as the OTLP/JSON `OTLPMarshaler`. It imports no third-party
package: only the standard library and github.com/gasmod/gas. Package
github.com/gasmod/gas/log/zerolog provides a gas.Logger
over zerolog, and package github.com/gasmod/gas/log/logtest a recording
gas.Logger for tests.

## Usage

In an app, the container builds the logger from `NewShippingLogger`,
calls its Init, and its Close at shutdown. By hand:

```go
logger, err := log.NewShippingLogger(
	"https://collector.example:4318/v1/logs",
	log.NewOTLPMarshaler(log.WithServiceName("shop")),
	log.WithHeader("Authorization", "Bearer "+token),
)()
if err != nil {
	return err
}
if err := logger.Init(ctx); err != nil {
	return err
}
defer logger.Close(ctx)

logger.Info("order placed").Str("order", "A-1").Int("items", 3).Send()
```

The event is written as JSON to standard error at once, and posted to
the endpoint as an OTLP logs request with the `order` and `items`
attributes, at the latest by Close.

## Backends

`SlogLogger`, from `NewSlogLogger`, writes to a *slog.Logger,
slog.Default() unless `WithSlogLogger` gives one. Trace is
`LevelTrace`, below slog.LevelDebug, so a handler can filter it apart
from Debug; Debug, Info, Warn and Error are slog's levels. Its Flush does
nothing, since it buffers nothing.

`ShippingLogger`, from `NewShippingLogger`, is a SlogLogger over a
`ShippingHandler`, with the same levels; its Flush posts what is queued.
The ShippingHandler, from `NewShippingHandler`, is the same pipeline as a
plain slog.Handler, for an app that logs with log/slog directly.

The zerolog backend writes each event at zerolog's own level, Trace
included.

The constructors of the loggers return a constructor in the shape the
gas container expects, func() (*T, error). Options only record their
value; the constructor checks them and fails with an error, never later.

## Events

A logger's Trace, Debug, Info, Warn and Error start an event; its field
methods add fields; Send writes it. An event belongs to the goroutine
that started it and is not safe for concurrent use. Field values are
taken on the caller's goroutine, by the time Send returns: a scalar when
its method is called; a value given to Any is held as it is until Send
by slog, and encoded at once by zerolog, so a map or a slice given to
Any must not change before Send returns. Nothing reads a field value
after Send returns: the shipping handler encodes the values it queues
before Handle returns, as described under Shipping.

An event is sent at most once. Every method called after Send, Send
included, does nothing: it neither writes again nor reaches another
event. An event at a level the logger does not enable is a shared no-op
event, so its fields cost nothing.

A source or caller that the backend adds is the line that called Send:
slog's AddSource reports it, as does zerolog's Caller.

A logger from With carries the fields added to its context, before each
event's own; its parent is unchanged. Calling Logger again on the same
context, after adding more fields, gives a second logger with them all;
the first is unchanged. A key given twice is written twice, as the
backend writes duplicates.

Encodings that differ by backend:

- Err with a nil error: slog writes the key with a null value, zerolog
leaves the field out, shipping sends the key with the empty value.
- Any with an error: all write its Error text. zerolog treats it as
Err.
- Duration: slog writes a slog duration, which slog's JSON handler
writes in nanoseconds; zerolog writes zerolog's Dur, in
zerolog.DurationFieldUnit, milliseconds unless changed; shipping
sends its String text, such as `1.5s`.
- Any with other values: slog writes them as the handler does; zerolog
writes their JSON; shipping encodes them as described under
Shipping.

Send of a SlogLogger hands the record to the handler with the caller's
program counter, and discards the handler's error, as slog.Logger does:
gas.LogEvent's Send has no result. A ShippingHandler reports the error
of its local handler, as described under Shipping.

## Shipping

A ShippingHandler writes each record to its local handler, when that
enables the record's level, and then queues a `Record` of it for
shipping when the level is at least the shipping level, `WithLevel`,
`DefaultShippingLevel` unless given. The local handler is
`WithLocalHandler`'s, or a slog.JSONHandler on standard error at the
shipping level; `WithoutLocalHandler` turns it off.

The Record is built in Handle, on the logging goroutine, so nothing the
caller holds is read later. Its attributes are the handler's, from
WithAttrs, then the record's, flattened: a group's attributes take its
key joined to theirs by `.`, a group with an empty key is inlined, an
empty group and an empty attribute are dropped, and LogValuers are
resolved. A value of kind slog.KindAny is encoded: nil stays nil, a
[]byte is copied, an error becomes its Error text, a fmt.Stringer its
String text, a value encoding/json encodes its JSON text, and anything
else, such as a channel, a func or a map, slice or pointer that refers
to itself, `!unencodable ` and the name of its type, such as
`!unencodable map[string]interface {}`; such a value is never
formatted, so a cycle cannot overflow the stack. A method that panics
gives `<nil>` for a nil pointer and otherwise `!PANIC: ` and the panic
value, as slog's handlers write them. The time is kept as it is, zero
included.

Records wait in a queue of at most `WithQueueSize` records,
`DefaultQueueSize` unless given, and Handle never blocks on it. A
goroutine of the sender posts the oldest records as a batch of
`WithBatchSize` records, `DefaultBatchSize` unless given, as soon as the
queue holds that many, and every `WithFlushInterval`,
`DefaultFlushInterval` unless given, posts all the records waiting, in
batches. The queue and the batches are bounded by their count of
records only: neither a record's size nor a request body's is bounded.
A batch is one request: the Marshaler encodes it, and the body is
posted to the endpoint with the Content-Type the Marshaler names, then
the headers given to `WithHeader`, which can replace it. A post runs on
the sender goroutine, so a slow endpoint fills the queue. A post's
context ends with the Flush or Close it belongs to; the HTTP client's
timeout, `DefaultHTTPTimeout` for the default client, bounds it too. The
default client follows no redirect: a 3xx response is returned as it
is, so the headers and the body never reach another URL, and the post
fails with its status. A client given to `WithHTTPClient` is used as it
is, redirect policy included. The response body is read, up to 64 KiB,
and discarded, so the connection is reused. A status outside 2xx is a
failure. Delivery is best effort: a failed batch is not retried.

The Marshaler gets a batch it must not keep: the sender reuses its
memory for the next one, and clears it after each post, so no record
stays reachable from it.

A record that is not delivered is dropped and counted: one logged when
the queue is full, or while the sender is not running, every record of
a batch the Marshaler fails or panics on, whose post fails, or whose
response is not 2xx, and the records Close gives up on, as described
below. `ShippingHandler.Dropped` and `ShippingLogger.Dropped` return the
count since the handler was made.

`ShippingHandler.Flush` posts the records queued when it is called, in
batches, and waits for them, within its context. When a post fails,
Flush returns an error wrapping `ErrNotDelivered` with the number of
records not delivered. When its context ends first, Flush returns an
error wrapping ErrNotDelivered, with the number of records still queued
or being posted, and the context's error, and drops nothing: the
records it did not post stay queued for the next batch, and a post its
context cancels puts its batch back at the front of the queue, unless
the queue has no room for it any more, in which case the batch is
dropped. A Flush whose context has already ended posts nothing.
`ShippingLogger.Flush`, the Flush of gas.Logger, does the same within
`DefaultFlushTimeout`, and reports its error. A logger from With
flushes the same sender.

`ShippingHandler.Close` and `ShippingLogger.Close` stop accepting
records, so a record logged after them is written locally and dropped
for shipping, and post what is queued, in batches, within their context.
When a post fails, Close returns an error wrapping ErrNotDelivered with
the number of records not delivered. When the context ends first, Close
returns an error wrapping ErrNotDelivered, with the number of records
still queued or being posted, and the context's error; the post in
flight is canceled, and those records are dropped. Close returns
without waiting for a post that ignores the cancellation, such as one
whose Marshaler blocks. Close of a closed sender returns nil.

NewShippingHandler starts the sender. NewShippingLogger does not: its
Init starts it, and a record logged before the first Init is written
locally and dropped for shipping, as after Close. Init after Close
starts the sender again, so a ShippingLogger is restartable. When the
sender of the earlier Close still runs, because Close's context ended
while a post went on, or because Close has not returned, Init waits for
it to stop, so two senders never post at once and Marshal is never
called concurrently; when Init's context ends first, it returns an error
wrapping `ErrStillClosing` and the context's error, and the sender is
not started. Each ShippingLogger is a gas.Service named by `WithName`,
`DefaultShippingName` unless given; two in one app need distinct names,
or the container closes only one.

Failures are reported to the error handler, `WithErrorHandler`, which
by default writes the error and a newline to standard error, as
os.Stderr was when the handler was made. The error
handler runs on a goroutine of its own, never on the logging goroutine
or the sender's, so it may log through the same logger, and call Flush
or Close, without a deadlock. Reports are coalesced by class, the
sentinel they wrap: at most one report of a class is delivered per flush
interval, the first at once; the reports made meanwhile become one, the
last error with the count, such as `(3 times)`, delivered when the
interval has passed. Reports made before the first is delivered join
it. An error handler that logs through the logger it
reports for therefore makes at most one record per class per interval.
A panic of the error handler is recovered and noted on standard error.
The reports are: a record dropped on a full queue (`ErrQueueFull`); the
local handler's error (`ErrLocalHandler`), which Handle also returns; a
Marshaler's error (`ErrMarshal`) or panic (`ErrMarshalPanic`); a failed
post (`ErrPost`); a status outside 2xx (`ErrStatus`); and the error of
ShippingLogger.Flush (`ErrNotDelivered`); and a record logged before the
first Init of a ShippingLogger (`ErrNotRunning`), so a logger that is
never started is not silent. Records dropped after Close, or because
the context of a Flush or Close ended, are counted but not reported, and
neither is a post that such a context canceled.

## OTLP encoding

`OTLPMarshaler` encodes a batch as one OTLP/HTTP JSON logs request
(Content-Type `OTLPContentType`): one resource, with the attributes
given to `WithServiceName`, `WithServiceVersion` and
`WithResourceAttribute`, each replacing an earlier value of its key; one
scope, named by `WithScopeName` or else the service name; and one log
record per Record, following the OTLP JSON Protobuf encoding:

- `timeUnixNano`: the time in Unix nanoseconds, as a string, left out
for a zero time, one before 1970, or one after 2262.
- `severityNumber`: the floor of the level's band: TRACE (1) below
slog.LevelDebug, DEBUG (5), INFO (9), WARN (13), ERROR (17).
- `severityText`: `TRACE` for LevelTrace, else slog's name of the
level, such as `INFO` or `DEBUG-2`.
- `body`: the message, as `stringValue`.
- `attributes`: the Record's attributes, flattened and encoded as a
ShippingHandler does when the Record was built elsewhere. A string
is `stringValue`; an integer `intValue`, a string, except a uint64
above the int64 range, which is the decimal `stringValue`; a float
`doubleValue`, a number, or the string `NaN`, `Infinity` or
`-Infinity`; a bool `boolValue`; a []byte `bytesValue`, in standard
base64; a duration its String text and a time its RFC 3339 text
with nanoseconds, as `stringValue`; nil the empty value `{}`.

A record that fails to encode, because a method of one of its values
panics, is sent as a record with its time and level and the body
`OTLPReplacedBody`, and counted by `OTLPMarshaler.Replaced`, so one
record cannot fail its batch.

## Concurrency

The loggers, the ShippingHandler and the OTLPMarshaler are safe for
concurrent use; events and the contexts from With are not. A
ShippingHandler queues under a read lock; Close takes the write lock to
stop the sender, so no record enters a closing queue. One goroutine per
running sender posts the batches, and Marshal is called on it, one batch
at a time; Init waits for the goroutine of the last Close to end, so
there is never more than one. The error handler runs on a goroutine
that exists only while a report waits. A Close whose context ended
returns without waiting for the sender goroutine, which ends once its
canceled posts return.

## Errors

The sentinel errors, to be tested with `errors.Is`:

- `ErrNilLogger`: WithSlogLogger was given nil.
- `ErrNilMarshaler`: a shipping constructor was given a nil Marshaler.
- `ErrInvalidEndpoint`: the endpoint is not an absolute http or https
URL.
- `ErrInvalidOption`: a ShippingOption was given a value it does not
accept. The constructor checks WithBatchSize, WithQueueSize,
WithFlushInterval, WithHTTPClient, WithLevel, WithLocalHandler,
WithErrorHandler, WithName and WithHeader, in this order, after the
Marshaler and the endpoint. A WithHeader name must be a token and
its value must hold no control character but a horizontal tab, as
RFC 9110 says; the error names neither.
- `ErrQueueFull`, `ErrNotRunning`, `ErrLocalHandler`, `ErrMarshal`,
`ErrMarshalPanic`, `ErrPost`, `ErrStatus`: reported to the error
handler, as described under Shipping. ErrLocalHandler, ErrMarshal
and ErrPost wrap the cause.
- `ErrNotDelivered`: Flush or Close returned with records not
delivered. It wraps the context's error when the context ended.
- `ErrStillClosing`: Init's context ended while the sender of an
earlier Close still ran. It wraps the context's error.

Every error starts with `gas/log: `. No error names the endpoint's user
info, path, query or fragment, or a header name or value: an error
names the endpoint by its scheme and host only, such as
`https://collector.example:4318`, and a parse error of the endpoint is
replaced, not wrapped. The panic value of a Marshaler is left out of its report,
since it can hold the records. The errors, as returned or reported:

```go
gas/log: logger is nil
gas/log: marshaler is nil
gas/log: invalid endpoint: must be an absolute http or https URL
gas/log: invalid option: WithBatchSize must be positive
gas/log: invalid option: WithQueueSize must be positive
gas/log: invalid option: WithFlushInterval must be positive
gas/log: invalid option: WithHTTPClient requires a client
gas/log: invalid option: WithLevel requires a level
gas/log: invalid option: WithLocalHandler requires a handler
gas/log: invalid option: WithErrorHandler requires a function
gas/log: invalid option: WithName requires a name
gas/log: invalid option: WithHeader requires a token as the name
gas/log: invalid option: WithHeader requires a value without control characters
gas/log: queue full: record dropped
gas/log: queue full: record dropped (3 times)
gas/log: not running: record dropped
gas/log: local handler failed: disk full
gas/log: marshal failed: unsupported value
gas/log: marshaler panicked
gas/log: post failed: Post "https://collector.example:4318": connection refused
gas/log: unexpected status 503 from https://collector.example:4318
gas/log: not delivered: 1 record
gas/log: not delivered: 2 records: context deadline exceeded
gas/log: still closing: context deadline exceeded
```

## API

Run `go doc -all github.com/gasmod/gas/log` for every declaration, or `go doc github.com/gasmod/gas/log.<Name>` for one.
