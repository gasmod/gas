// Package conformance holds the tests every store of the module runs: the
// contract of the queue package, checked with identical expectations
// against a store built by the caller.
//
// # Usage
//
// A store's tests call [Run] with a [Subject] that builds a new,
// uninitialized store with the VisibilityTimeout setting the suite asks
// for, and creates the queues the suite asks for:
//
//	conformance.Run(t, conformance.Subject{
//		Name: memory.ServiceName,
//		New: func(t *testing.T, visibility time.Duration) conformance.Store {
//			cfg := memory.DefaultConfig()
//			cfg.Queue.VisibilityTimeout = visibility
//			s, err := memory.New(memory.WithConfig(cfg))(nil, &gas.NopLogger{})
//			require.NoError(t, err)
//			return s
//		},
//		CreateQueue: createQueue,
//	})
//
// A store whose server the tests share gives each queue a name of its
// own. Subject.Skip names the rules a store skips, each with the reason,
// such as an emulator that does not do as SQS does.
//
// # Suite
//
// Run runs each rule as a parallel subtest, on a new store each: a round
// trip with the attributes Dequeue reports, Nack, NackAfter, Extend, the
// end of a visibility timeout, delays, short and long polls, maxMessages,
// an ended context before and during a call, a queue that does not exist,
// identifiers that are not queues, the payload, size and attribute rules,
// a long attribute name, the bounds of a delay, FIFO order, groups and
// rules, deduplication by ID and by content, a group ID on a standard
// queue, the bounds of Dequeue's arguments and of a visibility timeout,
// receipt handles, a second Ack, the lifecycle sentinels and a restart,
// Init with an ended context, Close ending a waiting Dequeue, concurrent
// calls for -race, and errors that never quote a message. Every error is
// checked with errors.Is, and its full text where it is the same for
// every store; the text starts with the Subject's Name.
package conformance
