// Package emailtest provides a mock implementation of gas.EmailProvider
// for use in tests. The mock records all calls and allows configuring
// per-method behavior via function fields.
//
//	mock := &emailtest.MockEmail{}
//	mock.SendFn = func(ctx context.Context, msg *gas.Email) error {
//	    return nil
//	}
package emailtest

import (
	"context"
	"sync"

	"github.com/gasmod/gas"
)

// MockEmail is a configurable mock of gas.EmailProvider. Each method
// delegates to its corresponding Fn field if set, otherwise returns the
// zero value. All calls are recorded in the Calls slice for assertions.
type MockEmail struct {
	SendFn             func(ctx context.Context, msg *gas.Email) error
	SendFromTemplateFn func(ctx context.Context, msg *gas.TemplatedEmail) error
	CheckReadyFn       func(ctx context.Context) error
	Calls              []Call

	mu sync.Mutex
}

var _ gas.EmailProvider = (*MockEmail)(nil)
var _ gas.ReadyReporter = (*MockEmail)(nil)

// Call records a single method invocation on the mock.
type Call struct {
	Method string
	Args   []any
}

func (m *MockEmail) record(method string, args ...any) {
	m.mu.Lock()
	m.Calls = append(m.Calls, Call{Method: method, Args: args})
	m.mu.Unlock()
}

// Send records the call and delegates to SendFn if set.
func (m *MockEmail) Send(ctx context.Context, msg *gas.Email) error {
	m.record("Send", msg)
	if m.SendFn != nil {
		return m.SendFn(ctx, msg)
	}
	return nil
}

// SendFromTemplate records the call and delegates to SendFromTemplateFn if set.
func (m *MockEmail) SendFromTemplate(ctx context.Context, msg *gas.TemplatedEmail) error {
	m.record("SendFromTemplate", msg)
	if m.SendFromTemplateFn != nil {
		return m.SendFromTemplateFn(ctx, msg)
	}
	return nil
}

// CheckReady records the call and delegates to CheckReadyFn if set.
func (m *MockEmail) CheckReady(ctx context.Context) error {
	m.record("CheckReady")
	if m.CheckReadyFn != nil {
		return m.CheckReadyFn(ctx)
	}
	return nil
}

// Reset clears all recorded calls.
func (m *MockEmail) Reset() {
	m.mu.Lock()
	m.Calls = nil
	m.mu.Unlock()
}

// CallCount returns the number of times the given method was called.
func (m *MockEmail) CallCount(method string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, c := range m.Calls {
		if c.Method == method {
			n++
		}
	}
	return n
}
