// Package templatetest provides a mock implementation of gas.TemplateProvider
// for use in tests. The mock records all calls and allows configuring
// per-method behavior via function fields.
//
//	mock := &templatetest.MockTemplate{}
//	mock.GetFn = func(ctx context.Context, name string) ([]byte, error) {
//	    return []byte("<h1>Hello</h1>"), nil
//	}
package templatetest

import (
	"context"
	"io/fs"
	"sync"

	"github.com/gasmod/gas"
)

// MockTemplate is a configurable mock of gas.TemplateProvider. Each method
// delegates to its corresponding Fn field if set, otherwise returns the
// zero value. All calls are recorded in the Calls slice for assertions.
type MockTemplate struct {
	GetFn        func(ctx context.Context, name string) ([]byte, error)
	ListFn       func(ctx context.Context) ([]string, error)
	RegisterFn   func(ctx context.Context, name string, content []byte) error
	RegisterFSFn func(ctx context.Context, fsys fs.FS) error
	Calls        []Call

	mu sync.Mutex
}

var _ gas.TemplateProvider = (*MockTemplate)(nil)

// Call records a single method invocation on the mock.
type Call struct {
	Method string
	Args   []any
}

func (m *MockTemplate) record(method string, args ...any) {
	m.mu.Lock()
	m.Calls = append(m.Calls, Call{Method: method, Args: args})
	m.mu.Unlock()
}

// Get records the call and delegates to GetFn if set.
func (m *MockTemplate) Get(ctx context.Context, name string) ([]byte, error) {
	m.record("Get", name)
	if m.GetFn != nil {
		return m.GetFn(ctx, name)
	}
	return nil, nil
}

// List records the call and delegates to ListFn if set.
func (m *MockTemplate) List(ctx context.Context) ([]string, error) {
	m.record("List")
	if m.ListFn != nil {
		return m.ListFn(ctx)
	}
	return nil, nil
}

// Register records the call and delegates to RegisterFn if set.
func (m *MockTemplate) Register(ctx context.Context, name string, content []byte) error {
	m.record("Register", name, content)
	if m.RegisterFn != nil {
		return m.RegisterFn(ctx, name, content)
	}
	return nil
}

// RegisterFS records the call and delegates to RegisterFSFn if set.
func (m *MockTemplate) RegisterFS(ctx context.Context, fsys fs.FS) error {
	m.record("RegisterFS", fsys)
	if m.RegisterFSFn != nil {
		return m.RegisterFSFn(ctx, fsys)
	}
	return nil
}

// Reset clears all recorded calls.
func (m *MockTemplate) Reset() {
	m.mu.Lock()
	m.Calls = nil
	m.mu.Unlock()
}

// CallCount returns the number of times the given method was called.
func (m *MockTemplate) CallCount(method string) int {
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
