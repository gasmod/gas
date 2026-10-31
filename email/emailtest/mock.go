package emailtest

import (
	"context"
	"maps"
	"slices"
	"sync"

	"github.com/gasmod/gas"
)

// Call is one call of the mock's method.
type Call struct {
	// Method is the name of the method called.
	Method string
	// Args are the method's arguments after the context, in order.
	Args []any
}

// MockEmail is a gas.EmailProvider and gas.ReadyReporter that records its
// calls, as described in the package documentation.
type MockEmail struct {
	// SendFn is what Send returns. When it is nil, Send returns nil.
	SendFn func(ctx context.Context, msg *gas.Email) error
	// SendFromTemplateFn is what SendFromTemplate returns. When it is nil,
	// SendFromTemplate returns nil.
	SendFromTemplateFn func(ctx context.Context, msg *gas.TemplatedEmail) error
	// CheckReadyFn is what CheckReady returns. When it is nil, CheckReady
	// returns nil.
	CheckReadyFn func(ctx context.Context) error

	// calls holds the calls in the order they were made.
	calls []Call
	// mu guards calls, since the mock may be called from several
	// goroutines.
	mu sync.Mutex
}

var (
	_ gas.EmailProvider = (*MockEmail)(nil)
	_ gas.ReadyReporter = (*MockEmail)(nil)
)

// Send records the call and returns SendFn's result.
func (m *MockEmail) Send(ctx context.Context, msg *gas.Email) error {
	m.record("Send", cloneEmail(msg))

	if m.SendFn != nil {
		return m.SendFn(ctx, msg)
	}

	return nil
}

// SendFromTemplate records the call and returns SendFromTemplateFn's
// result.
func (m *MockEmail) SendFromTemplate(ctx context.Context, msg *gas.TemplatedEmail) error {
	m.record("SendFromTemplate", cloneTemplated(msg))

	if m.SendFromTemplateFn != nil {
		return m.SendFromTemplateFn(ctx, msg)
	}

	return nil
}

// CheckReady records the call and returns CheckReadyFn's result.
func (m *MockEmail) CheckReady(ctx context.Context) error {
	m.record("CheckReady")

	if m.CheckReadyFn != nil {
		return m.CheckReadyFn(ctx)
	}

	return nil
}

// Calls returns a deep copy of the calls, in the order they were made:
// changing a message it returns changes no recorded call.
func (m *MockEmail) Calls() []Call {
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

// CallCount returns how many calls were made to method.
func (m *MockEmail) CallCount(method string) int {
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
func (m *MockEmail) Reset() {
	m.mu.Lock()

	m.calls = nil
	m.mu.Unlock()
}

// record appends a call.
func (m *MockEmail) record(method string, args ...any) {
	m.mu.Lock()

	m.calls = append(m.calls, Call{Method: method, Args: args})
	m.mu.Unlock()
}

// cloneArg returns a copy of a, a recorded argument, that shares nothing
// a caller can change with it.
func cloneArg(a any) any {
	switch v := a.(type) {
	case *gas.Email:
		return cloneEmail(v)
	case *gas.TemplatedEmail:
		return cloneTemplated(v)
	default:
		return a
	}
}

// cloneEmail returns a copy of msg that shares no slice or map with it,
// or nil for nil.
func cloneEmail(msg *gas.Email) *gas.Email {
	if msg == nil {
		return nil
	}

	out := *msg

	out.To = slices.Clone(msg.To)
	out.Cc = slices.Clone(msg.Cc)
	out.Bcc = slices.Clone(msg.Bcc)
	out.Headers = maps.Clone(msg.Headers)

	return &out
}

// cloneTemplated returns a copy of msg whose Email shares no slice or
// map with it, or nil for nil. Data is the same value.
func cloneTemplated(msg *gas.TemplatedEmail) *gas.TemplatedEmail {
	if msg == nil {
		return nil
	}

	out := *msg

	out.Email = *cloneEmail(&msg.Email)

	return &out
}
