# github.com/gasmod/gas/queue/memory

```go
import "github.com/gasmod/gas/queue/memory"
```

Package memory is a queue store held in the process's memory, as a
gas.JobQueueProvider. It keeps the contract of the queue package, and
so the rules and limits of Amazon SQS, without a server.

It suits tests and local development. It is local to one process, and
lost when the process stops. `Service` implements gas.Service and
gas.JobQueueProvider.

## Usage

In an app, the container builds the store from `New` and calls its
Init. By hand, with a configuration given in code:

```go
q, err := memory.New(memory.WithConfig(memory.DefaultConfig()))(nil, logger)
if err != nil {
	return err
}
if err := q.Init(ctx); err != nil {
	return err
}
defer q.Close(ctx)

err = q.CreateQueue(ctx, "orders", memory.QueueConfig{})
err = q.Enqueue(ctx, "orders", []byte("hello"))
jobs, err := q.Dequeue(ctx, "orders", 10, 0)
```

After this jobs holds the one message, hidden from other Dequeues for
30 seconds unless it is acked or nacked.

## Configuration

The settings live in the `queue` section, in `Settings`:

- `queue.visibilitytimeout`: how long a message Dequeue returns stays
hidden, 1s to 12h, rounded up to a whole second, or 0 for the
queue's own. Default: `DefaultVisibilityTimeout`, 0.
- `queue.maxmessages`: the most messages one queue holds, and the
most dedupe IDs a FIFO queue remembers, 1 or more. Default:
`DefaultMaxMessages`, 100000.
- `queue.maxbytes`: the most bytes the messages of one queue hold,
counting each payload and each attribute's name and value,
`MinMaxBytes`, 1 MiB, or more, so that a queue takes a message of
the largest size. Default: `DefaultMaxBytes`, 256 MiB.

`Service.Init` resolves the configuration each time it runs, from one
of two sources:

- Without `WithConfig`, it binds `DefaultConfig` from the
gas.ConfigProvider, which checks the `validate` tags. A nil
ConfigProvider is the error queue.ErrNoConfigProvider.
- With WithConfig, it takes a copy of the given Config, never
changing the caller's struct, and checks the same tags. A nil
Config is the error queue.ErrNilConfig.

Then the one rule the tags cannot express runs: `visibilitytimeout` is
0 or at least a second. A failure wraps queue.ErrInvalidConfig.

`Service.Config` returns a copy of the configuration the last
successful Init resolved, defaults included, or the zero Config before
one. A failed Init and Close leave it as it was. Its
`visibilitytimeout` is as resolved, before Init rounds it up.

## Queues

A queue exists once `Service.CreateQueue` creates it, as an SQS queue
is created outside the app. Every other method on a queue that does not
exist returns queue.ErrQueueNotFound.

A queue is named as the queue package describes. The store keys it by
the whole identifier, so a URL is a name of its own, and its host is
not contacted.

A `QueueConfig` sets:

- the queue's own visibility timeout, 1s to 12h, or 0 for
`DefaultQueueVisibilityTimeout`, 30 seconds, as SQS's default;
- its delay, 0 to 15 minutes;
- on a FIFO queue, content-based deduplication.

Both durations are rounded up to a whole second. A setting out of
bounds, or content-based deduplication on a standard queue, is
queue.ErrInvalidArgument.

Creating a queue that exists is not an error when the configuration is
the same, and is `ErrQueueExists` otherwise.

The queues and their messages belong to the Service, not to one Init.
Close keeps them, and Init after Close finds them as Close left them,
as an SQS queue outlives its clients.

## Limits

A queue holds at most `maxmessages` messages, counting those hidden and
those leased, and at most `maxbytes` bytes of them. An Enqueue past
either is `ErrQueueFull`.

A FIFO queue remembers at most `maxmessages` dedupe IDs. An Enqueue
first forgets those past the dedupe window. A new message that would
make one more is `ErrQueueFull` too, so that the IDs cannot grow
without bound while messages come and go.

A message stays until it is acked: the store has no retention period
and no dead-letter queue. The store keeps its own copy of each payload
and attribute map, and each job holds copies of its own.

## Receiving

Dequeue looks at the queue's messages in the order they were sent, and
leases the first visible ones, up to maxMessages, each with a new
receipt handle. On a FIFO queue it passes over every message of a group
behind one that is not visible.

A Dequeue that finds nothing waits until the first of these:

- a message may have become visible, as Enqueue, Ack, Nack, NackAfter
and Extend tell it;
- the next message it passed is due by the clock;
- its wait, counted on the system clock, ends.

## Receipt handles

A receipt handle is the latest of its message while the message is
leased under it and is not received again.

- Ack with the latest handle deletes the message, even when its lease
has ended.
- Ack with an older handle, or one of a message already deleted,
deletes nothing and is not an error. SQS can still delete in that
case.
- Nack, NackAfter and Extend need the latest handle of a leased
message, and otherwise return queue.ErrNotInFlight.
- A handle the store cannot have issued is queue.ErrInvalidReceipt.

## Clock

The store times delays, leases, the dedupe window and the timestamps
Dequeue reports by a `Clock`. That is the system clock unless
`WithClock` gives another, such as one a test moves by hand to see a
lease end without waiting.

The wait of a Dequeue is counted on the system clock whatever the
Clock, since it is the time a caller waits. A waiting Dequeue looks
again when the Clock says the next message is due, measured from the
Clock's now when it starts to wait.

## Lifecycle

A successful Init opens the store and logs `service initialized` at
info level, with `max_messages`, `max_bytes` and `visibility_timeout`.
Init on an initialized store returns queue.ErrAlreadyInitialized and
changes nothing.

`Service.Close` runs these steps, in order:

1. It makes the store closed.
2. It ends every waiting Dequeue at once, which returns
queue.ErrClosed.
3. It waits for them to return.
4. It logs `service closed` at info level.

A Dequeue never holds a message it has not returned, so none is left
hidden. Close ignores its context. Close on a store that is not
initialized returns nil and logs nothing. Init after Close opens the
store again, with a configuration resolved afresh.

Out of the initialized state:

- Before the first successful Init, every method but Init, Close,
Name and Config returns an error wrapping queue.ErrNotInitialized.
- After Close, until Init succeeds, they return queue.ErrClosed.

`New`'s constructor fails with queue.ErrNilLogger for a nil logger. A
nil ConfigProvider is accepted, and Init then requires WithConfig.

## Concurrency

A Service is safe for concurrent use. One mutex guards the queues and
the state. Each call holds it while it works, which is short and does
no I/O, and a waiting Dequeue releases it while it waits.

A call that runs beside Close either completes before it or returns
queue.ErrClosed.

## Errors

The sentinel errors of this package, to be tested with `errors.Is`:

- `ErrQueueFull`: the queue holds `maxmessages` messages, the message
would take it past `maxbytes`, or a FIFO queue remembers
`maxmessages` dedupe IDs within the dedupe window.
- `ErrQueueExists`: CreateQueue found the queue with another
configuration.

The other errors wrap a sentinel of the queue package, as the queue
package describes. Every error starts with `ServiceName` and the
operation. A configuration that fails its tags gets the validator's
error after `gas/queue/memory: init: invalid config: validate config: `,
such as:

```go
Key: 'Config.Queue.MaxMessages' Error:Field validation for 'MaxMessages' failed on the 'min' tag
```

The other errors, as returned, with `enqueue` standing for each
operation that can fail the same way:

```go
gas/queue/memory: logger is nil
gas/queue/memory: init: config is nil
gas/queue/memory: init: no config provider
gas/queue/memory: init: invalid config: Queue.VisibilityTimeout must be 0 or 1s to 12h
gas/queue/memory: init: service already initialized
gas/queue/memory: init: context canceled
gas/queue/memory: enqueue: invalid queue: empty
gas/queue/memory: enqueue "orders": invalid payload: empty
gas/queue/memory: enqueue "orders": queue does not exist
gas/queue/memory: enqueue "orders": queue full
gas/queue/memory: enqueue "orders.fifo": invalid argument: a dedupe ID is needed without content-based deduplication
gas/queue/memory: enqueue "orders": service not initialized
gas/queue/memory: enqueue "orders": service closed
gas/queue/memory: enqueue "orders": context canceled
gas/queue/memory: dequeue "orders": service closed
gas/queue/memory: ack "orders": invalid receipt handle: not one this store issued
gas/queue/memory: nack "orders": message not in flight
gas/queue/memory: extend "orders": invalid argument: visibility timeout must end within 12h0m0s of the receive
gas/queue/memory: create queue "orders": invalid argument: delay must be 0 to 15m0s
gas/queue/memory: create queue "orders": invalid argument: visibility timeout must be 0 to 12h0m0s
gas/queue/memory: create queue "orders": invalid argument: a standard queue takes no content-based deduplication
gas/queue/memory: create queue "orders": queue exists with another configuration
```

## API

Run `go doc -all github.com/gasmod/gas/queue/memory` for every declaration, or `go doc github.com/gasmod/gas/queue/memory.<Name>` for one.
