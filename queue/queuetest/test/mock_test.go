package queuetest_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/queue/queuetest"
)

// errFake is an error a field returns.
var errFake = errors.New("fake")

// With every field nil, the mock is an empty queue that accepts
// everything: Dequeue returns no jobs, the rest return nil.
func TestMockQueue_Defaults(t *testing.T) {
	t.Parallel()

	m := &queuetest.MockQueue{}
	ctx := t.Context()

	jobs, err := m.Dequeue(ctx, "q", 10, time.Second)
	require.NoError(t, err)
	assert.Empty(t, jobs)

	require.NoError(t, m.Enqueue(ctx, "q", []byte("v")))
	require.NoError(t, m.Ack(ctx, "q", gas.Job{}))
	require.NoError(t, m.Nack(ctx, "q", gas.Job{}))
}

// Each method returns what its field returns, given the arguments.
func TestMockQueue_Fields(t *testing.T) {
	t.Parallel()

	var got []any

	want := []gas.Job{{ID: "1", ReceiptHandle: "r1", Body: []byte("hello")}}
	m := &queuetest.MockQueue{
		EnqueueFn: func(_ context.Context, q string, payload []byte, opts ...gas.EnqueueOption) error {
			got = append(got, q, string(payload), len(opts))

			return errFake
		},
		DequeueFn: func(_ context.Context, q string, maxMessages int, wait time.Duration) ([]gas.Job, error) {
			got = append(got, q, maxMessages, wait)

			return want, nil
		},
		AckFn: func(_ context.Context, q string, job gas.Job) error {
			got = append(got, q, job.ID)

			return errFake
		},
		NackFn: func(_ context.Context, q string, job gas.Job) error {
			got = append(got, q, job.ID)

			return errFake
		},
	}
	ctx := t.Context()

	require.ErrorIs(t, m.Enqueue(ctx, "e", []byte("x"), gas.WithDelay(time.Second)), errFake)

	jobs, err := m.Dequeue(ctx, "d", 10, 20*time.Second)
	require.NoError(t, err)
	assert.Equal(t, want, jobs)
	require.ErrorIs(t, m.Ack(ctx, "a", gas.Job{ID: "1"}), errFake)
	require.ErrorIs(t, m.Nack(ctx, "n", gas.Job{ID: "2"}), errFake)
	assert.Equal(t, []any{"e", "x", 1, "d", 10, 20 * time.Second, "a", "1", "n", "2"}, got)
}

// Calls records each call with its arguments after the context, the
// options resolved, and the payload, attributes and jobs as copies, and
// returns a copy; CallCount counts one method's; Reset forgets them.
//
//nolint:funlen // every method's call, checked whole.
func TestMockQueue_Calls(t *testing.T) {
	t.Parallel()

	m := &queuetest.MockQueue{}
	ctx := t.Context()
	payload := []byte("abc")
	attrs := map[string]string{"k": "v"}
	job := gas.Job{
		ID:            "1",
		ReceiptHandle: "r",
		Attributes:    map[string]string{"a": "b"},
		Body:          []byte("body"),
	}

	_ = m.Enqueue(
		ctx,
		"q.fifo",
		payload,
		gas.WithDelay(
			time.Second,
		),
		gas.WithGroupID("g"),
		gas.WithDedupeID("d"),
		gas.WithJobAttributes(attrs),
	)
	_ = m.Enqueue(ctx, "q", nil)
	_, _ = m.Dequeue(ctx, "q", 5, time.Minute)
	_ = m.Ack(ctx, "q", job)
	_ = m.Nack(ctx, "q", job)

	payload[0] = 'X'
	attrs["k"] = "changed"
	job.Body[0] = 'X'
	job.Attributes["a"] = "changed"

	recorded := gas.Job{
		ID:            "1",
		ReceiptHandle: "r",
		Attributes:    map[string]string{"a": "b"},
		Body:          []byte("body"),
	}
	calls := m.Calls()
	assert.Equal(t, []queuetest.Call{
		{Method: "Enqueue", Args: []any{
			"q.fifo",
			[]byte("abc"),
			queuetest.Options{
				Attributes: map[string]string{
					"k": "v",
				},
				GroupID:  "g",
				DedupeID: "d",
				Delay:    time.Second,
			},
		}},
		{Method: "Enqueue", Args: []any{"q", []byte(nil), queuetest.Options{}}},
		{Method: "Dequeue", Args: []any{"q", 5, time.Minute}},
		{Method: "Ack", Args: []any{"q", recorded}},
		{Method: "Nack", Args: []any{"q", recorded}},
	}, calls)

	calls[0].Method = "changed"

	assert.Equal(t, "Enqueue", m.Calls()[0].Method)
	assert.Equal(t, 2, m.CallCount("Enqueue"))
	assert.Equal(t, 0, m.CallCount("Missing"))

	m.Reset()
	assert.Empty(t, m.Calls())
	assert.Equal(t, 0, m.CallCount("Enqueue"))
}

// Calls returns a deep copy: changing a payload, the attributes of the
// options or a job it returns, or the arguments slice, changes no
// recorded call.
func TestMockQueue_CallsDeepCopy(t *testing.T) {
	t.Parallel()

	m := &queuetest.MockQueue{}
	ctx := t.Context()
	job := gas.Job{
		ReceiptHandle: "r",
		Attributes:    map[string]string{"a": "b"},
		Body:          []byte("body"),
	}

	_ = m.Enqueue(ctx, "q", []byte("abc"), gas.WithJobAttributes(map[string]string{"k": "v"}))
	_ = m.Ack(ctx, "q", job)

	calls := m.Calls()

	calls[0].Args[1].([]byte)[0] = 'X'
	calls[0].Args[2].(queuetest.Options).Attributes["k"] = "changed"
	calls[1].Args[1].(gas.Job).Body[0] = 'X'
	calls[1].Args[1].(gas.Job).Attributes["a"] = "changed"
	calls[1].Args[0] = "changed"

	again := m.Calls()
	assert.Equal(t, []byte("abc"), again[0].Args[1])
	assert.Equal(t, map[string]string{"k": "v"}, again[0].Args[2].(queuetest.Options).Attributes)
	assert.Equal(t, job, again[1].Args[1])
	assert.Equal(t, "q", again[1].Args[0])
}

// The mock may be called from several goroutines at once, which -race
// checks.
func TestMockQueue_Concurrent(t *testing.T) {
	t.Parallel()

	m := &queuetest.MockQueue{}

	var wg sync.WaitGroup

	for range 8 {
		wg.Go(func() {
			for range 50 {
				_ = m.Enqueue(t.Context(), "q", []byte("v"))
				_, _ = m.Dequeue(t.Context(), "q", 1, 0)
				_ = m.CallCount("Enqueue")
				_ = m.Calls()
			}
		})
	}

	wg.Wait()
	assert.Equal(t, 400, m.CallCount("Enqueue"))
}

// The example in the package documentation's Usage section.
func TestDoc_Usage(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	m := &queuetest.MockQueue{
		DequeueFn: func(context.Context, string, int, time.Duration) ([]gas.Job, error) {
			return []gas.Job{{ID: "1", ReceiptHandle: "r1", Body: []byte("hello")}}, nil
		},
	}
	jobs, err := m.Dequeue(ctx, "orders", 10, 20*time.Second)

	require.NoError(t, err)
	require.Len(t, jobs, 1)
	assert.Equal(t, 1, m.CallCount("Dequeue"))
}
