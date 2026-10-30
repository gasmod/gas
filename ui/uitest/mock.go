package uitest

import (
	"html/template"
	"net/http"
	"sync"

	"github.com/gasmod/gas/ui"
)

// MockUI is a configurable mock of ui.Provider. Each method
// delegates to its corresponding Fn field if set, otherwise returns the
// zero value without writing to the ResponseWriter. All calls are recorded
// in the Calls slice for assertions.
type MockUI struct {
	RenderFn         func(w http.ResponseWriter, name string, data any, opts ...ui.RenderOption) error
	RenderFragmentFn func(w http.ResponseWriter, name string, data any, opts ...ui.RenderFragmentOption) error
	RegisterFuncsFn  func(funcs template.FuncMap)
	Calls            []Call

	mu sync.Mutex
}

var _ ui.Provider = (*MockUI)(nil)

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
func (m *MockUI) Render(
	w http.ResponseWriter,
	name string,
	data any,
	opts ...ui.RenderOption,
) error {
	m.record("Render", name, data, opts)
	if m.RenderFn != nil {
		return m.RenderFn(w, name, data, opts...)
	}
	return nil
}

// RenderFragment records the call and delegates to RenderFragmentFn if set.
func (m *MockUI) RenderFragment(
	w http.ResponseWriter,
	name string,
	data any,
	opts ...ui.RenderFragmentOption,
) error {
	m.record("RenderFragment", name, data, opts)
	if m.RenderFragmentFn != nil {
		return m.RenderFragmentFn(w, name, data, opts...)
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
