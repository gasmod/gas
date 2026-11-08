package memory_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config/configtest"
	"github.com/gasmod/gas/queue"
	"github.com/gasmod/gas/queue/memory"
)

// Name is ServiceName.
func TestName(t *testing.T) {
	t.Parallel()

	s, _ := build(t)
	assert.Equal(t, "gas/queue/memory", s.Name())
}

// DefaultConfig takes the queue's own visibility timeout and holds up to
// 100000 messages and 256 MiB a queue.
func TestDefaultConfig(t *testing.T) {
	t.Parallel()

	assert.Equal(
		t,
		&memory.Config{Queue: memory.Settings{MaxMessages: 100000, MaxBytes: 256 << 20}},
		memory.DefaultConfig(),
	)
}

// Init binds the `queue` section from the ConfigProvider over
// DefaultConfig.
func TestInit_Binds(t *testing.T) {
	t.Parallel()

	p, err := configtest.NewMockConfigWithValues(map[string]any{
		"queue": map[string]any{"maxmessages": 1, "visibilitytimeout": "1500ms"},
	})
	require.NoError(t, err)

	log := newRecorder()
	s, err := memory.New()(p, log)
	require.NoError(t, err)
	require.NoError(t, s.Init(t.Context()))
	t.Cleanup(func() { require.NoError(t, s.Close(context.Background())) })

	assert.Equal(t, entry{level: "info", msg: "service initialized", fields: map[string]any{
		"service":            memory.ServiceName,
		"max_messages":       1,
		"max_bytes":          memory.DefaultMaxBytes,
		"visibility_timeout": 2 * time.Second,
	}}, log.entries()[0])

	require.NoError(t, s.CreateQueue(t.Context(), "q", memory.QueueConfig{}))
	require.NoError(t, s.Enqueue(t.Context(), "q", []byte("v")))

	err = s.Enqueue(t.Context(), "q", []byte("v"))
	require.ErrorIs(t, err, memory.ErrQueueFull)
}

// A configuration is checked against its tags and the rule that a
// visibility timeout is 0 or at least a second.
func TestInit_ConfigRules(t *testing.T) {
	t.Parallel()

	cfg := memory.DefaultConfig()

	cfg.Queue.VisibilityTimeout = time.Millisecond

	err := initErr(t, memory.WithConfig(cfg))
	require.ErrorIs(t, err, queue.ErrInvalidConfig)
	require.EqualError(
		t,
		err,
		"gas/queue/memory: init: invalid config: Queue.VisibilityTimeout must be 0 or 1s to 12h",
	)

	cfg = memory.DefaultConfig()
	cfg.Queue.VisibilityTimeout = 13 * time.Hour

	err = initErr(t, memory.WithConfig(cfg))
	require.ErrorIs(t, err, queue.ErrInvalidConfig)

	p, err := configtest.NewMockConfigWithValues(
		map[string]any{"queue": map[string]any{"maxmessages": "many"}},
	)
	require.NoError(t, err)

	s, err := memory.New()(p, &gas.NopLogger{})
	require.NoError(t, err)

	err = s.Init(t.Context())
	require.ErrorIs(t, err, queue.ErrInvalidConfig)

	cfg = memory.DefaultConfig()
	cfg.Queue.MaxBytes = memory.MinMaxBytes - 1

	err = initErr(t, memory.WithConfig(cfg))
	require.ErrorIs(t, err, queue.ErrInvalidConfig)
	require.EqualError(t, err, "gas/queue/memory: init: invalid config: validate config: "+
		"Key: 'Config.Queue.MaxBytes' Error:Field validation for 'MaxBytes' failed on the 'min' tag")

	cfg.Queue.MaxBytes = memory.MinMaxBytes
	require.NoError(t, initErr(t, memory.WithConfig(cfg)))
}

// A queue holds at most MaxBytes bytes, counting each payload and each
// attribute's name and value, leased messages included; an Ack makes
// room again. A message of the largest size always fits an empty queue.
func TestQueueFull_Bytes(t *testing.T) {
	t.Parallel()

	s := initialized(t, func(set *memory.Settings) { set.MaxBytes = memory.MinMaxBytes })
	ctx := t.Context()

	require.NoError(t, s.CreateQueue(ctx, "q", memory.QueueConfig{}))

	attrs := map[string]string{"kind": "created"}
	half := []byte(strings.Repeat("a", memory.MinMaxBytes/2-len("kind")-len("created")))

	require.NoError(t, s.Enqueue(ctx, "q", half, gas.WithJobAttributes(attrs)))
	require.NoError(t, s.Enqueue(ctx, "q", half, gas.WithJobAttributes(attrs)))

	err := s.Enqueue(ctx, "q", []byte("v"))
	require.ErrorIs(t, err, memory.ErrQueueFull)
	require.EqualError(t, err, `gas/queue/memory: enqueue "q": queue full`)

	j := one(t, s, "q")
	require.ErrorIs(t, s.Enqueue(ctx, "q", []byte("v")), memory.ErrQueueFull)
	require.NoError(t, s.Ack(ctx, "q", j))
	require.NoError(t, s.Enqueue(ctx, "q", []byte("v")))

	jobs, err := s.Dequeue(ctx, "q", queue.MaxMessages, 0)
	require.NoError(t, err)

	for _, j := range jobs {
		require.NoError(t, s.Ack(ctx, "q", j))
	}

	largest := []byte(strings.Repeat("a", queue.MaxPayloadSize))
	require.NoError(t, s.Enqueue(ctx, "q", largest))
}

// A FIFO queue remembers at most MaxMessages dedupe IDs, so that IDs
// cannot pile up while messages come and go: past that, a new message is
// ErrQueueFull until the window forgets the oldest. A duplicate is still
// accepted.
func TestQueueFull_DedupeIDs(t *testing.T) {
	t.Parallel()

	c := newClock()
	s := withQueue(t, c, func(set *memory.Settings) { set.MaxMessages = 2 }, "q.fifo",
		memory.QueueConfig{})
	ctx := t.Context()
	send := func(d string) error {
		return s.Enqueue(ctx, "q.fifo", []byte("v"), gas.WithGroupID("g"), gas.WithDedupeID(d))
	}

	require.NoError(t, send("a"))
	require.NoError(t, s.Ack(ctx, "q.fifo", one(t, s, "q.fifo")))

	c.add(time.Minute)
	require.NoError(t, send("b"))
	require.NoError(t, s.Ack(ctx, "q.fifo", one(t, s, "q.fifo")))

	err := send("c")
	require.ErrorIs(t, err, memory.ErrQueueFull)
	require.EqualError(t, err, `gas/queue/memory: enqueue "q.fifo": queue full`)
	require.NoError(t, send("a"))
	none(t, s, "q.fifo")

	c.add(queue.DedupeWindow - time.Minute)
	require.NoError(t, send("c"))
	require.ErrorIs(t, send("d"), memory.ErrQueueFull)
}

// CreateQueue checks the queue's configuration, naming the rule broken.
func TestCreateQueue_Rules(t *testing.T) {
	t.Parallel()

	s := initialized(t, nil)
	ctx := t.Context()

	cases := []struct {
		id   string
		text string
		qc   memory.QueueConfig
	}{
		{
			"q",
			"invalid argument: visibility timeout must be 0 to 12h0m0s",
			memory.QueueConfig{VisibilityTimeout: -1},
		},
		{
			"q",
			"invalid argument: visibility timeout must be 0 to 12h0m0s",
			memory.QueueConfig{VisibilityTimeout: 13 * time.Hour},
		},
		{
			"q",
			"invalid argument: delay must be 0 to 15m0s",
			memory.QueueConfig{Delay: 16 * time.Minute},
		},
		{"q", "invalid argument: delay must be 0 to 15m0s", memory.QueueConfig{Delay: -1}},
		{
			"q", "invalid argument: a standard queue takes no content-based deduplication",
			memory.QueueConfig{ContentBasedDeduplication: true},
		},
	}

	for _, c := range cases {
		err := s.CreateQueue(ctx, c.id, c.qc)
		require.ErrorIs(t, err, queue.ErrInvalidArgument, c.text)
		require.EqualError(t, err, `gas/queue/memory: create queue "q": `+c.text)
	}
}

// CreateQueue takes an identifier as the other methods do, needs an
// initialized store, and accepts creating a queue again with the same
// configuration, but not with another. A URL is a name of its own.
func TestCreateQueue_Again(t *testing.T) {
	t.Parallel()

	s := initialized(t, nil)
	ctx := t.Context()

	err := s.CreateQueue(ctx, "bad name", memory.QueueConfig{})
	require.ErrorIs(t, err, queue.ErrInvalidQueue)
	require.EqualError(
		t,
		err,
		"gas/queue/memory: create queue: invalid queue: not a queue name or URL",
	)

	require.NoError(t, s.CreateQueue(ctx, "q", memory.QueueConfig{}))
	require.NoError(
		t,
		s.CreateQueue(ctx, "q", memory.QueueConfig{VisibilityTimeout: 30 * time.Second}),
	)

	err = s.CreateQueue(ctx, "q", memory.QueueConfig{Delay: time.Second})
	require.ErrorIs(t, err, memory.ErrQueueExists)
	require.EqualError(
		t,
		err,
		`gas/queue/memory: create queue "q": queue exists with another configuration`,
	)

	require.NoError(
		t,
		s.CreateQueue(ctx, "https://sqs.example/123456789012/q", memory.QueueConfig{}),
	)
	require.NoError(t, s.Enqueue(ctx, "https://sqs.example/123456789012/q", []byte("url")))
	none(t, s, "q")
	assert.Equal(t, "url", string(one(t, s, "https://sqs.example/123456789012/q").Body))

	closed, _ := build(t)

	err = closed.CreateQueue(ctx, "q", memory.QueueConfig{})
	require.ErrorIs(t, err, queue.ErrNotInitialized)
}

// A queue holds at most MaxMessages messages, leased ones included; a
// duplicate on a FIFO queue is accepted without counting.
func TestQueueFull(t *testing.T) {
	t.Parallel()

	s := initialized(t, func(set *memory.Settings) { set.MaxMessages = 2 })
	ctx := t.Context()

	require.NoError(t, s.CreateQueue(ctx, "q.fifo", memory.QueueConfig{}))

	send := func(d string) error {
		return s.Enqueue(ctx, "q.fifo", []byte("v"), gas.WithGroupID("g"), gas.WithDedupeID(d))
	}

	require.NoError(t, send("a"))
	one(t, s, "q.fifo")
	require.NoError(t, send("b"))
	require.NoError(t, send("a"))

	err := send("c")
	require.ErrorIs(t, err, memory.ErrQueueFull)
	require.EqualError(t, err, `gas/queue/memory: enqueue "q.fifo": queue full`)
}

// A lease ends when the clock passes the visibility timeout: the queue's
// own, or the VisibilityTimeout setting when it is set.
func TestClock_Lease(t *testing.T) {
	t.Parallel()

	c := newClock()
	s := withQueue(t, c, nil, "q", memory.QueueConfig{VisibilityTimeout: 10 * time.Second})
	ctx := t.Context()

	require.NoError(t, s.Enqueue(ctx, "q", []byte("v")))

	j := one(t, s, "q")
	assert.Equal(
		t,
		strconv.FormatInt(c.Now().UnixMilli(), 10),
		j.Attributes[queue.AttributeFirstReceiveTimestamp],
	)
	assert.Equal(
		t,
		strconv.FormatInt(c.Now().UnixMilli(), 10),
		j.Attributes[queue.AttributeSentTimestamp],
	)

	c.add(9 * time.Second)
	none(t, s, "q")

	c.add(time.Second)
	assert.Equal(t, "2", one(t, s, "q").Attributes[queue.AttributeReceiveCount])

	short := withQueue(
		t,
		c,
		func(set *memory.Settings) { set.VisibilityTimeout = 1500 * time.Millisecond },
		"q",
		memory.QueueConfig{VisibilityTimeout: time.Hour},
	)
	require.NoError(t, short.Enqueue(ctx, "q", []byte("v")))
	one(t, short, "q")
	c.add(1999 * time.Millisecond)
	none(t, short, "q")
	c.add(time.Millisecond)
	one(t, short, "q")
}

// A message's own delay, rounded up to a whole second, hides it, and
// without one the queue's delay does.
func TestClock_Delay(t *testing.T) {
	t.Parallel()

	c := newClock()
	s := withQueue(t, c, nil, "q", memory.QueueConfig{Delay: 1500 * time.Millisecond})
	ctx := t.Context()

	require.NoError(t, s.Enqueue(ctx, "q", []byte("queue delay")))
	require.NoError(t, s.Enqueue(ctx, "q", []byte("own"), gas.WithDelay(time.Millisecond)))

	none(t, s, "q")
	c.add(time.Second)
	assert.Equal(t, "own", string(one(t, s, "q").Body))
	c.add(time.Second)
	assert.Equal(t, "queue delay", string(one(t, s, "q").Body))
}

// A FIFO queue forgets a dedupe ID once the dedupe window has passed.
func TestClock_DedupeWindow(t *testing.T) {
	t.Parallel()

	c := newClock()
	s := withQueue(t, c, nil, "q.fifo", memory.QueueConfig{})
	ctx := t.Context()
	send := func() error {
		return s.Enqueue(ctx, "q.fifo", []byte("v"), gas.WithGroupID("g"), gas.WithDedupeID("d"))
	}

	require.NoError(t, send())
	require.NoError(t, s.Ack(ctx, "q.fifo", one(t, s, "q.fifo")))

	c.add(queue.DedupeWindow - time.Nanosecond)
	require.NoError(t, send())
	none(t, s, "q.fifo")

	c.add(time.Nanosecond)
	require.NoError(t, send())

	j := one(t, s, "q.fifo")
	assert.Equal(t, "d", j.Attributes[queue.AttributeDedupeID])
	assert.Equal(t, "2", j.Attributes[queue.AttributeSequenceNumber])
}

// Nack, NackAfter and Extend need a message leased under the job's
// receipt handle; Ack with an older handle deletes nothing, with the
// latest it deletes, even after the lease ended; a handle the store did
// not issue is queue.ErrInvalidReceipt.
func TestReceiptHandles(t *testing.T) {
	t.Parallel()

	c := newClock()
	s := withQueue(t, c, nil, "q", memory.QueueConfig{VisibilityTimeout: 10 * time.Second})
	ctx := t.Context()

	require.NoError(t, s.Enqueue(ctx, "q", []byte("v")))

	first := one(t, s, "q")

	c.add(10 * time.Second)

	err := s.Nack(ctx, "q", first)
	require.ErrorIs(t, err, queue.ErrNotInFlight)
	require.EqualError(t, err, `gas/queue/memory: nack "q": message not in flight`)

	second := one(t, s, "q")
	require.ErrorIs(t, s.Extend(ctx, "q", first, time.Second), queue.ErrNotInFlight)
	require.ErrorIs(t, s.NackAfter(ctx, "q", first, time.Second), queue.ErrNotInFlight)
	require.NoError(t, s.Ack(ctx, "q", first))
	none(t, s, "q")

	c.add(time.Minute)
	require.NoError(t, s.Ack(ctx, "q", second))
	none(t, s, "q")
	require.NoError(t, s.Ack(ctx, "q", second))
	require.ErrorIs(t, s.Nack(ctx, "q", second), queue.ErrNotInFlight)

	for _, h := range []string{"bogus", "1:x", "x:1", "1"} {
		err = s.Ack(ctx, "q", gas.Job{ReceiptHandle: h})
		require.ErrorIs(t, err, queue.ErrInvalidReceipt, h)
		require.EqualError(
			t,
			err,
			`gas/queue/memory: ack "q": invalid receipt handle: not one this store issued`,
		)

		err = s.Nack(ctx, "q", gas.Job{ReceiptHandle: h})
		require.ErrorIs(t, err, queue.ErrInvalidReceipt, h)
	}
}

// A lease ends no later than 12 hours after the receive: Extend past
// that is queue.ErrInvalidArgument.
func TestExtend_Limit(t *testing.T) {
	t.Parallel()

	c := newClock()
	s := withQueue(t, c, nil, "q", memory.QueueConfig{})
	ctx := t.Context()

	require.NoError(t, s.Enqueue(ctx, "q", []byte("v")))

	j := one(t, s, "q")
	require.NoError(t, s.Extend(ctx, "q", j, queue.MaxVisibilityTimeout))

	c.add(time.Hour)

	err := s.Extend(ctx, "q", j, 11*time.Hour+time.Second)
	require.ErrorIs(t, err, queue.ErrInvalidArgument)
	require.EqualError(
		t,
		err,
		`gas/queue/memory: extend "q": invalid argument: visibility timeout must end within 12h0m0s of the receive`,
	)
	require.NoError(t, s.Extend(ctx, "q", j, 11*time.Hour))
}

// A waiting Dequeue looks again when the clock says the next message is
// due, and returns it.
func TestDequeue_WakesForDueMessage(t *testing.T) {
	t.Parallel()

	s := initialized(t, nil)
	ctx := t.Context()

	require.NoError(t, s.CreateQueue(ctx, "q", memory.QueueConfig{}))
	require.NoError(t, s.Enqueue(ctx, "q", []byte("v"), gas.WithDelay(time.Second)))

	start := time.Now()

	jobs, err := s.Dequeue(ctx, "q", 1, 5*time.Second)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	assert.Less(t, time.Since(start), 3*time.Second)
}

// Close ends a waiting Dequeue at once, which returns queue.ErrClosed,
// and the queues and their messages outlive it.
func TestClose_EndsWaitAtOnce(t *testing.T) {
	t.Parallel()

	s, log := build(t, memory.WithConfig(memory.DefaultConfig()))
	ctx := t.Context()

	require.NoError(t, s.Init(ctx))
	require.NoError(t, s.CreateQueue(ctx, "q", memory.QueueConfig{}))

	done := make(chan error, 1)

	go func() {
		_, err := s.Dequeue(context.Background(), "q", 1, 20*time.Second)
		done <- err
	}()

	time.Sleep(100 * time.Millisecond)

	start := time.Now()

	require.NoError(t, s.Close(ctx))
	require.ErrorIs(t, <-done, queue.ErrClosed)
	assert.Less(t, time.Since(start), time.Second)

	require.NoError(t, s.Init(ctx))
	require.NoError(t, s.Enqueue(ctx, "q", []byte("v")))
	require.NoError(t, s.Close(ctx))
	require.NoError(t, s.Init(ctx))
	assert.Equal(t, "v", string(one(t, s, "q").Body))
	require.NoError(t, s.Close(ctx))

	assert.Equal(t, entry{level: "info", msg: "service closed", fields: map[string]any{
		"service": memory.ServiceName,
	}}, log.entries()[1])
}

// A Dequeue that a send wakes as Close runs returns queue.ErrClosed and
// leases nothing.
func TestClose_RacesWake(t *testing.T) {
	t.Parallel()

	for range 20 {
		s := initialized(t, nil)
		ctx := t.Context()

		require.NoError(t, s.CreateQueue(ctx, "q", memory.QueueConfig{}))

		done := make(chan error, 1)

		go func() {
			_, err := s.Dequeue(context.Background(), "q", 1, 5*time.Second)
			done <- err
		}()

		time.Sleep(10 * time.Millisecond)

		var wg sync.WaitGroup

		wg.Go(func() { _ = s.Enqueue(ctx, "q", []byte("v")) })
		wg.Go(func() { assert.NoError(t, s.Close(ctx)) })
		wg.Wait()

		err := <-done
		if err != nil {
			require.ErrorIs(t, err, queue.ErrClosed)
		}

		require.NoError(t, s.Init(ctx))

		jobs, err := s.Dequeue(ctx, "q", 1, 0)
		require.NoError(t, err)

		if len(jobs) == 0 {
			continue
		}

		assert.Equal(t, "1", jobs[0].Attributes[queue.AttributeReceiveCount],
			"a Dequeue that Close ended leased the message")
	}
}

// Calls may run while the store restarts, which -race checks.
func TestRestart_Concurrent(t *testing.T) {
	t.Parallel()

	s := initialized(t, nil)
	require.NoError(t, s.CreateQueue(t.Context(), "q", memory.QueueConfig{}))

	var wg sync.WaitGroup

	stop := make(chan struct{})

	for range 4 {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}

				err := s.Enqueue(context.Background(), "q", []byte("v"))
				if err != nil && !errors.Is(err, queue.ErrClosed) {
					t.Errorf("enqueue: %v", err)
				}

				_, err = s.Dequeue(context.Background(), "q", 10, 0)
				if err != nil && !errors.Is(err, queue.ErrClosed) {
					t.Errorf("dequeue: %v", err)
				}
			}
		})
	}

	for range 20 {
		require.NoError(t, s.Close(t.Context()))
		require.NoError(t, s.Init(t.Context()))
	}

	close(stop)
	wg.Wait()
}

// A nil Clock given to WithClock keeps the system clock.
func TestWithClock_Nil(t *testing.T) {
	t.Parallel()

	s := initialized(t, nil, memory.WithClock(nil))
	require.NoError(t, s.CreateQueue(t.Context(), "q", memory.QueueConfig{}))

	before := time.Now().UnixMilli()

	require.NoError(t, s.Enqueue(t.Context(), "q", []byte("v")))

	ms, err := strconv.ParseInt(one(t, s, "q").Attributes[queue.AttributeSentTimestamp], 10, 64)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, ms, before)
}
