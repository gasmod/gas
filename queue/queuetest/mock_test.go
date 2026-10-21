package queuetest

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/gasmod/gas"
)

// mockCase drives one MockQueue method: call invokes it, setErr installs an
// Fn that returns err and records that it ran, and args are the arguments the
// mock must record.
type mockCase struct {
	call   func(m *MockQueue) error
	setErr func(m *MockQueue, err error, ran *bool)
	method string
	args   []any
}

func mockCases() []mockCase {
	ctx := context.Background()
	payload := []byte("payload")
	job := gas.Job{ID: "id-1", ReceiptHandle: "rh-1"}

	return []mockCase{
		{
			method: "Enqueue",
			args:   []any{"q", payload, []gas.EnqueueOption(nil)},
			call:   func(m *MockQueue) error { return m.Enqueue(ctx, "q", payload) },
			setErr: func(m *MockQueue, err error, ran *bool) {
				m.EnqueueFn = func(context.Context, string, []byte, ...gas.EnqueueOption) error {
					*ran = true
					return err
				}
			},
		},
		{
			method: "Dequeue",
			args:   []any{"q", 5, time.Second},
			call: func(m *MockQueue) error {
				_, err := m.Dequeue(ctx, "q", 5, time.Second)
				return err
			},
			setErr: func(m *MockQueue, err error, ran *bool) {
				m.DequeueFn = func(context.Context, string, int, time.Duration) ([]gas.Job, error) {
					*ran = true
					return nil, err
				}
			},
		},
		{
			method: "Ack",
			args:   []any{"q", job},
			call:   func(m *MockQueue) error { return m.Ack(ctx, "q", job) },
			setErr: func(m *MockQueue, err error, ran *bool) {
				m.AckFn = func(context.Context, string, gas.Job) error {
					*ran = true
					return err
				}
			},
		},
		{
			method: "Nack",
			args:   []any{"q", job},
			call:   func(m *MockQueue) error { return m.Nack(ctx, "q", job) },
			setErr: func(m *MockQueue, err error, ran *bool) {
				m.NackFn = func(context.Context, string, gas.Job) error {
					*ran = true
					return err
				}
			},
		},
	}
}

func TestMockQueue_Methods(t *testing.T) {
	t.Parallel()

	for _, tc := range mockCases() {
		t.Run(tc.method, func(t *testing.T) {
			t.Parallel()

			m := &MockQueue{}
			if err := tc.call(m); err != nil {
				t.Errorf("default %s = %v, want nil", tc.method, err)
			}
			if len(m.Calls) != 1 || m.Calls[0].Method != tc.method {
				t.Fatalf("Calls = %+v, want one %s call", m.Calls, tc.method)
			}
			if !reflect.DeepEqual(m.Calls[0].Args, tc.args) {
				t.Errorf("recorded args = %#v, want %#v", m.Calls[0].Args, tc.args)
			}

			wantErr := errors.New("injected")
			var ran bool
			tc.setErr(m, wantErr, &ran)
			if err := tc.call(m); !errors.Is(err, wantErr) {
				t.Errorf("%s with Fn = %v, want %v", tc.method, err, wantErr)
			}
			if !ran {
				t.Errorf("%s did not call its Fn", tc.method)
			}
			if got := m.CallCount(tc.method); got != 2 {
				t.Errorf("CallCount(%s) = %d, want 2", tc.method, got)
			}
		})
	}
}

func TestMockQueue_DequeueReturnsFnJobs(t *testing.T) {
	t.Parallel()

	want := []gas.Job{{ID: "a"}, {ID: "b"}}
	m := &MockQueue{
		DequeueFn: func(context.Context, string, int, time.Duration) ([]gas.Job, error) {
			return want, nil
		},
	}
	got, err := m.Dequeue(context.Background(), "q", 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Dequeue = %+v, want %+v", got, want)
	}
}

func TestMockQueue_CallCountAndReset(t *testing.T) {
	t.Parallel()

	m := &MockQueue{}
	ctx := context.Background()
	_ = m.Enqueue(ctx, "q", nil)
	_ = m.Enqueue(ctx, "q", nil)
	_ = m.Ack(ctx, "q", gas.Job{})

	if got := m.CallCount("Enqueue"); got != 2 {
		t.Errorf("CallCount(Enqueue) = %d, want 2", got)
	}
	if got := m.CallCount("Ack"); got != 1 {
		t.Errorf("CallCount(Ack) = %d, want 1", got)
	}
	if got := m.CallCount("Nack"); got != 0 {
		t.Errorf("CallCount(Nack) = %d, want 0", got)
	}

	m.Reset()
	if len(m.Calls) != 0 || m.CallCount("Enqueue") != 0 {
		t.Errorf("after Reset, Calls = %+v, want none", m.Calls)
	}
}

// TestMockQueue_ConcurrentCalls checks, under -race, that the mock records
// concurrent calls without a data race and loses none.
func TestMockQueue_ConcurrentCalls(t *testing.T) {
	t.Parallel()

	const workers, perWorker = 8, 50
	m := &MockQueue{}
	ctx := context.Background()

	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for range perWorker {
				_ = m.Enqueue(ctx, "q", nil)
				_ = m.CallCount("Enqueue")
			}
		})
	}
	wg.Wait()

	if got := m.CallCount("Enqueue"); got != workers*perWorker {
		t.Errorf("CallCount(Enqueue) = %d, want %d", got, workers*perWorker)
	}
}
