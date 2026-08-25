// Package uitest provides a mock implementation of gas.UIProvider
// for use in tests. The mock records all calls and allows configuring
// per-method behavior via function fields.
//
//	mock := &uitest.MockUI{}
//	mock.RenderFn = func(w http.ResponseWriter, name string, data any) error {
//	    w.Write([]byte("<h1>Hello</h1>"))
//	    return nil
//	}
package uitest

import (
	"html/template"
	"net/http"
	"sync"

	"github.com/gasmod/gas"
)

// MockUI is a configurable mock of gas.UIProvider. Each method
// delegates to its corresponding Fn field if set, otherwise returns the
// zero value without writing to the ResponseWriter. All calls are recorded
// in the Calls slice for assertions.
type MockUI struct {
	RenderFn           func(w http.ResponseWriter, name string, data any) error
	RenderFragmentFn   func(w http.ResponseWriter, name string, data any) error
	RenderWithStatusFn func(w http.ResponseWriter, status int, name string, data any) error
	RegisterFuncsFn    func(funcs template.FuncMap)
	Calls              []Call

	mu sync.Mutex
}

var _ gas.UIProvider = (*MockUI)(nil)

// Call records a single method invocation on the mock.
type Call struct {
	Method string
	Args   []any
}

func (m *MockUI) record(method string, args ...any) {
	m.mu.Lock()
	m.Calls = append(m.Calls, Call{Method: method, Args: args})
	m.mu.Unlock()
}

// Render records the call and delegates to RenderFn if set.
func (m *MockUI) Render(w http.ResponseWriter, name string, data any) error {
	m.record("Render", name, data)
	if m.RenderFn != nil {
		return m.RenderFn(w, name, data)
	}
	return nil
}

// RenderFragment records the call and delegates to RenderFragmentFn if set.
func (m *MockUI) RenderFragment(w http.ResponseWriter, name string, data any) error {
	m.record("RenderFragment", name, data)
	if m.RenderFragmentFn != nil {
		return m.RenderFragmentFn(w, name, data)
	}
	return nil
}

// RenderWithStatus records the call and delegates to RenderWithStatusFn if set.
func (m *MockUI) RenderWithStatus(w http.ResponseWriter, status int, name string, data any) error {
	m.record("RenderWithStatus", status, name, data)
	if m.RenderWithStatusFn != nil {
		return m.RenderWithStatusFn(w, status, name, data)
	}
	return nil
}

// RegisterFuncs records the call and delegates to RegisterFuncsFn if set.
func (m *MockUI) RegisterFuncs(funcs template.FuncMap) {
	m.record("RegisterFuncs", funcs)
	if m.RegisterFuncsFn != nil {
		m.RegisterFuncsFn(funcs)
	}
}

// Reset clears all recorded calls.
func (m *MockUI) Reset() {
	m.mu.Lock()
	m.Calls = nil
	m.mu.Unlock()
}

// CallCount returns the number of times the given method was called.
func (m *MockUI) CallCount(method string) int {
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
