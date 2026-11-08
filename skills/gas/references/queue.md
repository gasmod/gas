# github.com/gasmod/gas/queue

```go
import "github.com/gasmod/gas/queue"
```

Package queue defines what every queue store of the module promises:
the queues it accepts, the messages it sends, how a message is leased
and redelivered, how it honors a context, its lifecycle, and the errors
it returns.

The rules are Amazon SQS's, so that code written against one store runs
on another. The stores implement gas.JobQueueProvider and gas.Service,
and each lives in its own package:

- gas/queue/sqs: Amazon SQS, or an emulator that speaks its API.
- gas/queue/memory: queues in the process's memory, for tests and
local development.
- gas/queue/queuetest: a mock for tests.

## Usage

Each store's New returns a constructor in the shape the gas container
expects, so a store is registered as gas.JobQueueProvider:

```go
gas.WithSingletonService[gas.JobQueueProvider](sqs.New())
```

A producer sends, and a worker receives, handles and acknowledges:

```go
err := q.Enqueue(ctx, "orders", []byte(`{"id":42}`),
	gas.WithJobAttributes(map[string]string{"kind": "created"}))

jobs, err := q.Dequeue(ctx, "orders", 10, 20*time.Second)
for _, job := range jobs {
	if handle(job) == nil {
		err = q.Ack(ctx, "orders", job)
	} else {
		err = q.Nack(ctx, "orders", job)
	}
}
```

After this the queue `orders` no longer holds the messages that were
handled, and holds the others, visible again.

## Contract

Every store, and any gas.JobQueueProvider that wants to be one, keeps
the rules of the sections from Queue names to Order of checks.

The module's conformance tests run every store through these rules,
with identical expectations, but for the differences of an emulator
from SQS that a test names.

## Queue names

A queue is named by a queue name or a queue URL, as `ParseQueue` reads
it:

- A name is 1 to 80 ASCII letters, digits, hyphens and underscores. A
FIFO queue's name ends with `.fifo`.
- A URL is `http` or `https`, has a host and no user information,
query or fragment, and its path ends with a queue name.

Anything else is `ErrInvalidQueue`, from every method, and the error
never quotes it. A queue that does not exist is `ErrQueueNotFound`.

Errors name a queue by its name, never by its URL, which holds the AWS
account ID.

## Delivery

Delivery is at least once. A message is leased, not removed, when
Dequeue returns it. It stays hidden for a visibility timeout and then
becomes visible again, unless Ack deletes it first.

A send whose reply is lost, and that the store or the caller retries,
can store the message twice. Consumers must be idempotent.

## Sending and receiving

Enqueue checks the message, as `CheckMessage` does, before it sends
anything, and returns the first rule it breaks. A message that fails is
never stored.

Dequeue returns up to maxMessages jobs, waiting up to wait for the
first:

- maxMessages is 1 to `MaxMessages`.
- wait is 0 to `MaxWait`. A wait of 0 is a short poll, which returns
at once. The wait is counted in whole seconds, rounded up.
- Values out of bounds are `ErrInvalidArgument`, never clamped.
- Dequeue returns no jobs, and no error, when none came. A store may
return fewer jobs than there are messages.

Ack deletes the job's message. Nack makes it visible again at once, to
be received again. Each counts as done only once it returns nil. A job
without a receipt handle is `ErrInvalidReceipt`.

## Contexts

Every method returns the context's error, wrapped, when its context has
ended before it starts, and honors the context during the call.

Close is the exception: it always closes. A store whose Close context
ends while calls are in flight may cut them short, as its package
describes.

## States

A store is not initialized until its first successful Init, and closed
after Close:

- Before its first successful Init, every method but Init and Close
returns `ErrNotInitialized`.
- Init on an initialized store returns `ErrAlreadyInitialized` and
changes nothing.
- After Close, until Init succeeds again, every method but Init and
Close returns `ErrClosed`.
- Close on a store that is not initialized returns nil.
- Close then Init restarts the store.

Close refuses new calls and returns once every call in flight has
returned. A Dequeue in flight when Close begins:

- returns no jobs, no later than its wait ends;
- returns ErrClosed, unless its receive failed first;
- leaves no message hidden: what it received is made visible again
before it returns.

## Order of checks

A method checks, in this order, and returns the first failure:

1. Its arguments, in the order of its parameters. The message of
Enqueue is the exception: its rules are checked in the order
`CheckMessage` checks them.
2. The store's state.
3. The context.

## Payloads

A payload is text: 1 byte or more of valid UTF-8, holding only the
characters SQS allows, #x9, #xA, #xD, #x20 to #xD7FF, #xE000 to #xFFFD
and #x10000 to #x10FFFF. Anything else is `ErrInvalidPayload`. Encode
binary data, such as with base64, before it is sent.

A message is at most `MaxPayloadSize`, 1 MiB, counting the payload and,
for each attribute, its name, its value, and the 6 bytes of its type,
`String`.

## Message attributes

gas.WithJobAttributes sets the message's attributes, at most
`MaxAttributes`, 10. A name:

- is 1 to `MaxAttributeNameLength`, 256, ASCII letters, digits, `_`,
`-` and `.`;
- neither starts nor ends with `.`, nor holds `..`;
- does not start with `AWS.` or `Amazon.`, in any case;
- is not the name of an SQS system attribute, as
`IsReservedAttribute` reports, so that no producer can pass one off
as a system attribute.

The names match exactly, as SQS's are case-sensitive. A name that
differs only in case, such as `sentTimestamp`, is a user attribute of
its own, and a job holds it beside the system attribute.

A value is non-empty valid UTF-8 of the characters a payload may hold.

A broken rule is `ErrInvalidAttribute`. The error quotes neither the
name nor the value, but for a system attribute's name.

## Delay

gas.WithDelay hides a message for 0 to `MaxDelay`, 15 minutes, counted
in whole seconds, rounded up, so a delay is never shorter than asked.
Without one, or with 0, the queue's own delay applies, which is 0
unless the queue sets one. A delay out of bounds is
`ErrInvalidArgument`.

## System attributes

gas.Job.Attributes holds the message's attributes, then these system
attributes, which take the place of an attribute of the same name that
another producer sent:

- `ApproximateReceiveCount`: how many times the message was received,
this time included, such as `1`.
- `ApproximateFirstReceiveTimestamp`: when the message was first
received, in milliseconds since the Unix epoch.
- `SentTimestamp`: when the message was sent, in milliseconds since
the Unix epoch.
- `MessageGroupId`: the group ID, when the message has one.
- `MessageDeduplicationId`: the dedupe ID of a FIFO message.
- `SequenceNumber`: the sequence number of a FIFO message.

`SystemAttributes` lists them. No other system attribute is reported.

## FIFO

A queue whose name ends with `.fifo` is a FIFO queue. On it:

- gas.WithGroupID is required. A store returns a group's messages in
the order they were sent and, while a message of a group is leased,
no later message of the group.
- gas.WithDedupeID gives a dedupe ID. A message whose dedupe ID the
queue saw within `DedupeWindow`, 5 minutes, is accepted and not
stored.
- Without a dedupe ID, a queue with content-based deduplication takes
the SHA-256 of the payload, and a queue without it refuses the
message.
- A message takes no delay of its own.

A group ID and a dedupe ID are 1 to `MaxIDLength`, 128, ASCII letters,
digits and punctuation.

On a standard queue, a group ID names the tenant of a message, as SQS's
fair queues take it, and orders nothing. A dedupe ID is refused.

Every broken rule is `ErrInvalidArgument`.

## Visibility

Dequeue leases each message for the store's VisibilityTimeout setting,
or, when that is 0, for the queue's own visibility timeout. Nack ends
the lease at once.

The stores also have NackAfter and Extend, which set the lease to end a
duration from now, 0 to `MaxVisibilityTimeout`, 12 hours, rounded up to
a whole second:

- NackAfter retries later.
- Extend keeps a long job's message hidden.

A lease never ends more than 12 hours after the receive. A duration out
of bounds is `ErrInvalidArgument`.

## Receipt handles

Nack, NackAfter and Extend need the message to be leased under the
job's receipt handle. Once its lease has ended, or it was received
again, the memory store returns `ErrNotInFlight`, and SQS replies with
an error, as the sqs package describes.

Ack with the receipt handle of the latest receive deletes the message,
even after its lease ended. Ack with an older handle is not an error:

- On SQS, it may or may not delete the message.
- On the memory store, it deletes nothing.

## Lifecycle

Stop the workers that call Dequeue, and let them finish their Ack and
Nack, before Close. A call after Close returns ErrClosed, and an Ack
refused then leaves the message to come back when its lease ends.

The gas Worker closes services in the reverse order of their Init, so a
service that depends on the queue, and runs its workers, is closed
before it. A worker that is not such a service is stopped by the app
before Shutdown.

## Security

Payloads, attribute values and receipt handles are application data,
and a queue URL holds the AWS account ID.

No error the stores make holds a payload, an attribute name or value, a
group or dedupe ID, a receipt handle, a queue URL, an endpoint, or a
credential. A store's errors name the operation and the queue name
instead.

## Concurrency

Every store is safe for concurrent use, as each store's package
describes. The mock is too, except for setting its fields while it is
called.

## Errors

Every error a store returns starts with, in order:

1. its service name, such as `gas/queue/memory`;
2. the operation, such as `enqueue`;
3. the queue name, quoted, when the identifier is valid;
4. what failed.

It wraps one of these sentinels, to be tested with `errors.Is`:

- `ErrInvalidQueue`: the identifier is neither a queue name nor a
queue URL.
- `ErrInvalidPayload`, `ErrInvalidAttribute`: as under Payloads and
Message attributes.
- `ErrInvalidArgument`: a delay, an ID, a FIFO rule, maxMessages, a
wait or a visibility timeout, as above.
- `ErrInvalidReceipt`: the job has no receipt handle, or one the
store did not issue.
- `ErrQueueNotFound`: the queue does not exist.
- `ErrNotInFlight`: the message is not leased under the job's
receipt handle, as under Receipt handles.
- `ErrNotInitialized`, `ErrAlreadyInitialized`, `ErrClosed`: as under
States.
- `ErrNilLogger`: a store's constructor got a nil logger.
- `ErrNilConfig`, `ErrNoConfigProvider`, `ErrInvalidConfig`: Init
could not resolve a valid configuration.

The errors of this package's checks, as returned, before a store adds
its service name, the operation and the queue name:

```go
invalid queue: empty
invalid queue: not a queue name or URL
invalid payload: empty
invalid payload: not valid UTF-8
invalid payload: a character SQS does not allow, at byte 2
invalid payload: 1048577 bytes with the attributes, more than 1048576
invalid attribute: 11 attributes, at most 10
invalid attribute: a name is empty or longer than 256 bytes
invalid attribute: a name holds a character other than letters, digits, '_', '-' and '.'
invalid attribute: a name starts or ends with '.' or holds '..'
invalid attribute: a name starts with the reserved prefix AWS. or Amazon., in any case
invalid attribute: "SentTimestamp" is a system attribute
invalid attribute: a value is empty
invalid attribute: a value is not valid UTF-8
invalid attribute: a value holds a character SQS does not allow
invalid argument: delay must be 0 to 15m0s
invalid argument: a FIFO queue takes no per-message delay
invalid argument: a FIFO queue needs a group ID
invalid argument: group ID must be 1 to 128 ASCII letters, digits and punctuation
invalid argument: a standard queue takes no dedupe ID
invalid argument: dedupe ID must be 1 to 128 ASCII letters, digits and punctuation
invalid argument: maxMessages must be 1 to 10
invalid argument: wait must be 0 to 20s
invalid argument: visibility timeout must be 0 to 12h0m0s
invalid receipt handle: empty
```

## API

Run `go doc -all github.com/gasmod/gas/queue` for every declaration, or `go doc github.com/gasmod/gas/queue.<Name>` for one.
