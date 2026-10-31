package memory_test

import (
	"context"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/queue/memory"
)

// The example in the package documentation's Usage section.
func TestDoc_Usage(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	logger := &gas.NopLogger{}

	q, err := memory.New(memory.WithConfig(memory.DefaultConfig()))(nil, logger)
	require.NoError(t, err)
	require.NoError(t, q.Init(ctx))

	defer func() { require.NoError(t, q.Close(ctx)) }()

	err = q.CreateQueue(ctx, "orders", memory.QueueConfig{})
	require.NoError(t, err)

	err = q.Enqueue(ctx, "orders", []byte("hello"))
	require.NoError(t, err)

	jobs, err := q.Dequeue(ctx, "orders", 10, 0)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	assert.Equal(t, "hello", string(jobs[0].Body))

	other, err := q.Dequeue(ctx, "orders", 10, 0)
	require.NoError(t, err)
	assert.Empty(t, other)
}

// The error messages the package documentation lists are the ones the
// store returns, and it lists every message this test makes.
//
//nolint:funlen,contextcheck // one case per documented message, made with the test helpers.
func TestDoc_Errors(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	orders := func(edit func(*memory.Settings), qc memory.QueueConfig, id string) *memory.Service {
		s := initialized(t, edit)
		require.NoError(t, s.CreateQueue(ctx, id, qc))

		return s
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()

	cases := map[string]func() error{
		"gas/queue/memory: logger is nil": func() error {
			_, err := memory.New()(nil, nil)

			return err
		},
		"gas/queue/memory: init: config is nil": func() error {
			return initErr(t, memory.WithConfig(nil))
		},
		"gas/queue/memory: init: no config provider": func() error {
			return initErr(t)
		},
		"gas/queue/memory: init: invalid config: Queue.VisibilityTimeout must be 0 or 1s to 12h": func() error {
			cfg := memory.DefaultConfig()

			cfg.Queue.VisibilityTimeout = time.Nanosecond

			return initErr(t, memory.WithConfig(cfg))
		},
		"gas/queue/memory: init: service already initialized": func() error {
			return initialized(t, nil).Init(ctx)
		},
		"gas/queue/memory: init: context canceled": func() error {
			s, _ := build(t, memory.WithConfig(memory.DefaultConfig()))

			return s.Init(canceled)
		},
		"gas/queue/memory: enqueue: invalid queue: empty": func() error {
			return initialized(t, nil).Enqueue(ctx, "", []byte("v"))
		},
		`gas/queue/memory: enqueue "orders": invalid payload: empty`: func() error {
			return initialized(t, nil).Enqueue(ctx, "orders", nil)
		},
		`gas/queue/memory: enqueue "orders": queue does not exist`: func() error {
			return initialized(t, nil).Enqueue(ctx, "orders", []byte("v"))
		},
		`gas/queue/memory: enqueue "orders": queue full`: func() error {
			s := orders(
				func(set *memory.Settings) { set.MaxMessages = 1 },
				memory.QueueConfig{},
				"orders",
			)
			require.NoError(t, s.Enqueue(ctx, "orders", []byte("v")))

			return s.Enqueue(ctx, "orders", []byte("v"))
		},
		`gas/queue/memory: enqueue "orders.fifo": invalid argument: a dedupe ID is needed without content-based ` +
			`deduplication`: func() error {
			s := orders(nil, memory.QueueConfig{}, "orders.fifo")

			return s.Enqueue(ctx, "orders.fifo", []byte("v"), gas.WithGroupID("g"))
		},
		`gas/queue/memory: enqueue "orders": service not initialized`: func() error {
			s, _ := build(t)

			return s.Enqueue(ctx, "orders", []byte("v"))
		},
		`gas/queue/memory: enqueue "orders": service closed`: func() error {
			s, _ := build(t, memory.WithConfig(memory.DefaultConfig()))
			require.NoError(t, s.Init(ctx))
			require.NoError(t, s.Close(ctx))

			return s.Enqueue(ctx, "orders", []byte("v"))
		},
		`gas/queue/memory: enqueue "orders": context canceled`: func() error {
			return initialized(t, nil).Enqueue(canceled, "orders", []byte("v"))
		},
		`gas/queue/memory: dequeue "orders": service closed`: func() error {
			s, _ := build(t, memory.WithConfig(memory.DefaultConfig()))
			require.NoError(t, s.Init(ctx))
			require.NoError(t, s.CreateQueue(ctx, "orders", memory.QueueConfig{}))

			done := make(chan error, 1)

			go func() {
				_, err := s.Dequeue(context.Background(), "orders", 1, 10*time.Second)
				done <- err
			}()

			time.Sleep(50 * time.Millisecond)
			require.NoError(t, s.Close(ctx))

			return <-done
		},
		`gas/queue/memory: ack "orders": invalid receipt handle: not one this store issued`: func() error {
			return orders(
				nil,
				memory.QueueConfig{},
				"orders",
			).Ack(ctx, "orders", gas.Job{ReceiptHandle: "r"})
		},
		`gas/queue/memory: nack "orders": message not in flight`: func() error {
			s := orders(nil, memory.QueueConfig{}, "orders")
			require.NoError(t, s.Enqueue(ctx, "orders", []byte("v")))

			j := one(t, s, "orders")
			require.NoError(t, s.Ack(ctx, "orders", j))

			return s.Nack(ctx, "orders", j)
		},
		`gas/queue/memory: extend "orders": invalid argument: visibility timeout must end within 12h0m0s ` +
			`of the receive`: func() error {
			c := newClock()
			s := withQueue(t, c, nil, "orders", memory.QueueConfig{})
			require.NoError(t, s.Enqueue(ctx, "orders", []byte("v")))

			j := one(t, s, "orders")

			c.add(time.Second)

			return s.Extend(ctx, "orders", j, 12*time.Hour)
		},
		`gas/queue/memory: create queue "orders": invalid argument: delay must be 0 to 15m0s`: func() error {
			return initialized(
				t,
				nil,
			).CreateQueue(ctx, "orders", memory.QueueConfig{Delay: time.Hour})
		},
		`gas/queue/memory: create queue "orders": invalid argument: visibility timeout must be 0 to 12h0m0s`: func() error {
			return initialized(
				t,
				nil,
			).CreateQueue(ctx, "orders", memory.QueueConfig{VisibilityTimeout: 24 * time.Hour})
		},
		`gas/queue/memory: create queue "orders": invalid argument: a standard queue takes no content-based ` +
			`deduplication`: func() error {
			return initialized(
				t,
				nil,
			).CreateQueue(ctx, "orders", memory.QueueConfig{ContentBasedDeduplication: true})
		},
		`gas/queue/memory: create queue "orders": queue exists with another configuration`: func() error {
			return orders(nil, memory.QueueConfig{}, "orders").
				CreateQueue(ctx, "orders", memory.QueueConfig{Delay: time.Second})
		},
	}

	for _, text := range slices.Sorted(maps.Keys(cases)) {
		require.EqualError(t, cases[text](), text)
	}

	assert.ElementsMatch(
		t,
		slices.Collect(maps.Keys(cases)),
		docLines(t, "../doc.go", "gas/queue/memory: "),
	)

	cfg := memory.DefaultConfig()

	cfg.Queue.MaxMessages = 0

	err := initErr(t, memory.WithConfig(cfg))
	require.EqualError(t, err, "gas/queue/memory: init: invalid config: validate config: "+
		docLines(t, "../doc.go", "Key: ")[0])
}
