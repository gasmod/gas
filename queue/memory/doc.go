// Package memory is a queue store held in the process's memory, as a
// gas.JobQueueProvider. It keeps the contract of the queue package, and
// so the rules and limits of Amazon SQS, without a server: it suits
// tests and local development, and is local to one process and lost when
// the process stops. [Service] implements gas.Service and
// gas.JobQueueProvider.
//
// # Usage
//
// In an app, the container builds the store from [New] and calls its
// Init. By hand, with a configuration given in code:
//
//	q, err := memory.New(memory.WithConfig(memory.DefaultConfig()))(nil, logger)
//	if err != nil {
//		return err
//	}
//	if err := q.Init(ctx); err != nil {
//		return err
//	}
//	defer q.Close(ctx)
//
//	err = q.CreateQueue(ctx, "orders", memory.QueueConfig{})
//	err = q.Enqueue(ctx, "orders", []byte("hello"))
//	jobs, err := q.Dequeue(ctx, "orders", 10, 0)
//
// After this jobs holds the one message, hidden from other Dequeues for
// 30 seconds unless it is acked or nacked.
//
// # Configuration
//
// The settings live in the `queue` section, in [Settings]:
//
//   - `queue.visibilitytimeout`: how long a message Dequeue returns stays
//     hidden, 1s to 12h, rounded up to a whole second, or 0 for the
//     queue's own. Default: [DefaultVisibilityTimeout], 0.
//   - `queue.maxmessages`: the most messages one queue holds, and the
//     most dedupe IDs a FIFO queue remembers, 1 or more. Default:
//     [DefaultMaxMessages], 100000.
//   - `queue.maxbytes`: the most bytes the messages of one queue hold,
//     counting each payload and each attribute's name and value,
//     [MinMaxBytes], 1 MiB, or more, so that a queue takes a message of
//     the largest size. Default: [DefaultMaxBytes], 256 MiB.
//
// [Service.Init] resolves the configuration each time it runs. Without
// [WithConfig], it binds [DefaultConfig] from the gas.ConfigProvider,
// which checks the `validate` tags; a nil ConfigProvider is the error
// queue.ErrNoConfigProvider. With WithConfig, it takes a copy of the given
// Config, never changing the caller's struct, and checks the same tags; a
// nil Config is the error queue.ErrNilConfig. Then the one rule the tags
// cannot express runs: `visibilitytimeout` is 0 or at least a second. A
// failure wraps queue.ErrInvalidConfig.
//
// # Queues
//
// A queue exists once [Service.CreateQueue] creates it, as an SQS queue
// is created outside the app; every other method on a queue that does
// not exist returns queue.ErrQueueNotFound. A queue is named as the queue
// package describes; the store keys it by the whole identifier, so a URL
// is a name of its own and its host is not contacted. A [QueueConfig]
// sets the queue's own visibility timeout, 1s to 12h, or 0 for
// [DefaultQueueVisibilityTimeout], 30 seconds, as SQS's default; its
// delay, 0 to 15 minutes; and, on a FIFO queue, content-based
// deduplication. Both durations are rounded up to a whole second. A
// setting out of bounds, or content-based deduplication on a standard
// queue, is queue.ErrInvalidArgument. Creating a queue that exists is not
// an error when the configuration is the same, and else
// [ErrQueueExists].
//
// The queues and their messages belong to the Service, not to one Init:
// Close keeps them, and Init after Close finds them as Close left them,
// as an SQS queue outlives its clients.
//
// # Messages
//
// A queue holds at most `maxmessages` messages, counting those hidden
// and those leased, and at most `maxbytes` bytes of them; an Enqueue past
// either is [ErrQueueFull]. A FIFO queue remembers at most `maxmessages`
// dedupe IDs: an Enqueue first forgets those past the dedupe window, and
// a new message that would make one more is [ErrQueueFull] too, so that
// the IDs cannot grow without bound while messages come and go. A message
// stays until it is acked: the store has no retention period and no
// dead-letter queue. The store keeps its own copy of each payload and
// attribute map, and each job holds copies of its own.
//
// Dequeue looks at the queue's messages in the order they were sent and
// leases the first visible ones, up to maxMessages, each with a new
// receipt handle. On a FIFO queue it passes over every message of a
// group behind one that is not visible. A Dequeue that finds nothing
// waits until a message may have become visible, as Enqueue, Ack, Nack,
// NackAfter and Extend tell it, or until the next message it passed is
// due by the clock, or until its wait, counted on the system clock, ends.
//
// A receipt handle is the latest of its message while the message is
// leased under it and is not received again. Ack with the latest handle
// deletes the message, even when its lease has ended. Ack with an older
// handle, or one of a message already deleted, deletes nothing and is not
// an error; SQS can still delete in that case. Nack, NackAfter and Extend
// need the latest handle of a leased message, and otherwise return
// queue.ErrNotInFlight. A handle the store cannot have issued is
// queue.ErrInvalidReceipt.
//
// # Clock
//
// The store times delays, leases, the dedupe window and the timestamps
// Dequeue reports by a [Clock], the system clock unless [WithClock] gives
// another, such as one a test moves by hand to see a lease end without
// waiting. The wait of a Dequeue is counted on the system clock whatever
// the Clock, since it is the time a caller waits; a waiting Dequeue looks
// again when the Clock says the next message is due, measured from the
// Clock's now when it starts to wait.
//
// # Lifecycle
//
// A successful Init opens the store and logs `service initialized` at
// info level, with `max_messages`, `max_bytes` and `visibility_timeout`. Init on an
// initialized store returns queue.ErrAlreadyInitialized and changes
// nothing. [Service.Close] makes the store closed, ends every waiting
// Dequeue at once, which returns queue.ErrClosed, waits for them to
// return, and logs `service closed` at info level. A Dequeue never holds
// a message it has not returned, so none is left hidden. Close on a store
// that is not initialized returns nil and logs nothing. Close ignores its
// context. Init after Close opens the store again, with a configuration
// resolved afresh.
//
// Before the first successful Init, every method but Init, Close and Name
// returns an error wrapping queue.ErrNotInitialized; after Close, until
// Init succeeds, queue.ErrClosed. [New]'s constructor fails with
// queue.ErrNilLogger for a nil logger; a nil ConfigProvider is accepted,
// and Init then requires WithConfig.
//
// # Concurrency
//
// A Service is safe for concurrent use. One mutex guards the queues and
// the state: each call holds it while it works, which is short and does
// no I/O, and a waiting Dequeue releases it while it waits. A call that
// runs beside Close either completes before it or returns
// queue.ErrClosed.
//
// # Errors
//
// The sentinel errors of this package, to be tested with [errors.Is]:
//
//   - [ErrQueueFull]: the queue holds `maxmessages` messages, the message
//     would take it past `maxbytes`, or a FIFO queue remembers
//     `maxmessages` dedupe IDs within the dedupe window.
//   - [ErrQueueExists]: CreateQueue found the queue with another
//     configuration.
//
// The other errors wrap a sentinel of the queue package, as the queue
// package describes. Every error starts with [ServiceName] and the
// operation. A configuration that fails its tags gets the validator's
// error after `gas/queue/memory: init: invalid config: validate config: `,
// such as:
//
//	Key: 'Config.Queue.MaxMessages' Error:Field validation for 'MaxMessages' failed on the 'min' tag
//
// The other errors, as returned, with `enqueue` standing for each
// operation that can fail the same way:
//
//	gas/queue/memory: logger is nil
//	gas/queue/memory: init: config is nil
//	gas/queue/memory: init: no config provider
//	gas/queue/memory: init: invalid config: Queue.VisibilityTimeout must be 0 or 1s to 12h
//	gas/queue/memory: init: service already initialized
//	gas/queue/memory: init: context canceled
//	gas/queue/memory: enqueue: invalid queue: empty
//	gas/queue/memory: enqueue "orders": invalid payload: empty
//	gas/queue/memory: enqueue "orders": queue does not exist
//	gas/queue/memory: enqueue "orders": queue full
//	gas/queue/memory: enqueue "orders.fifo": invalid argument: a dedupe ID is needed without content-based deduplication
//	gas/queue/memory: enqueue "orders": service not initialized
//	gas/queue/memory: enqueue "orders": service closed
//	gas/queue/memory: enqueue "orders": context canceled
//	gas/queue/memory: dequeue "orders": service closed
//	gas/queue/memory: ack "orders": invalid receipt handle: not one this store issued
//	gas/queue/memory: nack "orders": message not in flight
//	gas/queue/memory: extend "orders": invalid argument: visibility timeout must end within 12h0m0s of the receive
//	gas/queue/memory: create queue "orders": invalid argument: delay must be 0 to 15m0s
//	gas/queue/memory: create queue "orders": invalid argument: visibility timeout must be 0 to 12h0m0s
//	gas/queue/memory: create queue "orders": invalid argument: a standard queue takes no content-based deduplication
//	gas/queue/memory: create queue "orders": queue exists with another configuration
package memory
