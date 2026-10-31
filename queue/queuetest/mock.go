package queuetest

import (
	"bytes"
	"context"
	"maps"
	"sync"
	"time"

	"github.com/gasmod/gas"
)

// Call is one call of the mock's method.
type Call struct {
	// Method is the name of the method called.
	Method string
	// Args are the method's arguments after the context, in order.
	Args []any
}

// Options are the options of an Enqueue, resolved, as a Call records
// them.
type Options struct {
	// Attributes are from gas.WithJobAttributes, a copy, nil for none.
	Attributes map[string]string
	// GroupID is from gas.WithGroupID.
	GroupID string
	// DedupeID is from gas.WithDedupeID.
	DedupeID string
	// Delay is from gas.WithDelay.
	Delay time.Duration
}

// MockQueue is a gas.JobQueueProvider that records its calls, as
// described in the package documentation.
type MockQueue struct {
	// EnqueueFn is what Enqueue returns. When it is nil, Enqueue returns
	// nil.
	EnqueueFn func(ctx context.Context, queue string, payload []byte, opts ...gas.EnqueueOption) error
	// DequeueFn is what Dequeue returns. When it is nil, Dequeue returns
	// no jobs and nil.
	DequeueFn func(ctx context.Context, queue string, maxMessages int, wait time.Duration) ([]gas.Job, error)
	// AckFn is what Ack returns. When it is nil, Ack returns nil.
	AckFn func(ctx context.Context, queue string, job gas.Job) error
	// NackFn is what Nack returns. When it is nil, Nack returns nil.
	NackFn func(ctx context.Context, queue string, job gas.Job) error

	// calls holds the calls in the order they were made.
	calls []Call
	// mu guards calls, since the mock may be called from several
	// goroutines.
	mu sync.Mutex
}

var _ gas.JobQueueProvider = (*MockQueue)(nil)

// Enqueue records the call and returns EnqueueFn's result.
func (m *MockQueue) Enqueue(
	ctx context.Context,
	queue string,
	payload []byte,
	opts ...gas.EnqueueOption,
) error {
	delay, groupID, dedupeID, attrs := gas.ApplyEnqueueOptions(opts)

	m.record("Enqueue", queue, bytes.Clone(payload), Options{
		Attributes: maps.Clone(attrs),
		GroupID:    groupID,
		DedupeID:   dedupeID,
		Delay:      delay,
	})

	if m.EnqueueFn != nil {
		return m.EnqueueFn(ctx, queue, payload, opts...)
	}

	return nil
}

// Dequeue records the call and returns DequeueFn's result.
func (m *MockQueue) Dequeue(
	ctx context.Context,
	queue string,
	maxMessages int,
	wait time.Duration,
) ([]gas.Job, error) {
	m.record("Dequeue", queue, maxMessages, wait)

	if m.DequeueFn != nil {
		return m.DequeueFn(ctx, queue, maxMessages, wait)
	}

	return nil, nil
}

// Ack records the call and returns AckFn's result.
func (m *MockQueue) Ack(ctx context.Context, queue string, job gas.Job) error {
	m.record("Ack", queue, cloneJob(job))

	if m.AckFn != nil {
		return m.AckFn(ctx, queue, job)
	}

	return nil
}

// Nack records the call and returns NackFn's result.
func (m *MockQueue) Nack(ctx context.Context, queue string, job gas.Job) error {
	m.record("Nack", queue, cloneJob(job))

	if m.NackFn != nil {
		return m.NackFn(ctx, queue, job)
	}

	return nil
}

// Calls returns a deep copy of the calls, in the order they were made:
// changing a payload, an attribute map or a job it returns changes no
// recorded call.
func (m *MockQueue) Calls() []Call {
	m.mu.Lock()
	defer m.mu.Unlock()

	calls := make([]Call, 0, len(m.calls))

	for _, c := range m.calls {
		args := make([]any, 0, len(c.Args))

		for _, a := range c.Args {
			args = append(args, cloneArg(a))
		}

		calls = append(calls, Call{Method: c.Method, Args: args})
	}

	return calls
}

// cloneArg returns a copy of a, a recorded argument, that shares nothing
// a caller can change with it.
func cloneArg(a any) any {
	switch v := a.(type) {
	case []byte:
		return bytes.Clone(v)
	case Options:
		v.Attributes = maps.Clone(v.Attributes)

		return v
	case gas.Job:
		return cloneJob(v)
	default:
		return a
	}
}

// CallCount returns how many calls were made to method.
func (m *MockQueue) CallCount(method string) int {
	m.mu.Lock()
	defer m.mu.Unlock()

	n := 0

	for _, c := range m.calls {
		if c.Method == method {
			n++
		}
	}

	return n
}

// Reset forgets every call.
func (m *MockQueue) Reset() {
	m.mu.Lock()

	m.calls = nil
	m.mu.Unlock()
}

// record appends a call.
func (m *MockQueue) record(method string, args ...any) {
	m.mu.Lock()

	m.calls = append(m.calls, Call{Method: method, Args: args})
	m.mu.Unlock()
}

// cloneJob returns a copy of job whose body and attributes the caller
// cannot change.
func cloneJob(job gas.Job) gas.Job {
	job.Attributes = maps.Clone(job.Attributes)
	job.Body = bytes.Clone(job.Body)

	return job
}
