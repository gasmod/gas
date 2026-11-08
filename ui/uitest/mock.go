package uitest

import (
	"context"
	"html/template"
	"maps"
	"net/http"
	"sync"

	"github.com/gasmod/gas/ui"
)

// Call is one call of the mock's method.
type Call struct {
	// Method is the name of the method called.
	Method string
	// Args are the method's arguments after the context and the response
	// writer, in order.
	Args []any
}

// MockUI is a ui.Provider that records its calls, as described in the
// package documentation.
type MockUI struct {
	// RenderFn is what Render returns. When it is nil, Render writes
	// nothing and returns nil.
	RenderFn func(ctx context.Context, w http.ResponseWriter, name string, data any, opts ...ui.RenderOption) error
	// RenderFragmentFn is what RenderFragment returns. When it is nil,
	// RenderFragment writes nothing and returns nil.
	RenderFragmentFn func(
		ctx context.Context,
		w http.ResponseWriter,
		name string,
		data any,
		opts ...ui.RenderOption,
	) error
	// RegisterFuncsFn is what RegisterFuncs returns. When it is nil,
	// RegisterFuncs returns nil.
	RegisterFuncsFn func(funcs template.FuncMap) error

	// calls holds the calls in the order they were made.
	calls []Call
	// mu guards calls, since the mock may be called from several
	// goroutines.
	mu sync.Mutex
}

var _ ui.Provider = (*MockUI)(nil)

// Render records the call and returns RenderFn's result.
func (m *MockUI) Render(
	ctx context.Context,
	w http.ResponseWriter,
	name string,
	data any,
	opts ...ui.RenderOption,
) error {
	m.record("Render", name, data, ui.ApplyRenderOptions(opts...))

	if m.RenderFn != nil {
		return m.RenderFn(ctx, w, name, data, opts...)
	}

	return nil
}

// RenderFragment records the call and returns RenderFragmentFn's result.
func (m *MockUI) RenderFragment(
	ctx context.Context,
	w http.ResponseWriter,
	name string,
	data any,
	opts ...ui.RenderOption,
) error {
	m.record("RenderFragment", name, data, ui.ApplyRenderOptions(opts...))

	if m.RenderFragmentFn != nil {
		return m.RenderFragmentFn(ctx, w, name, data, opts...)
	}

	return nil
}

// RegisterFuncs records the call and returns RegisterFuncsFn's result.
func (m *MockUI) RegisterFuncs(funcs template.FuncMap) error {
	m.record("RegisterFuncs", maps.Clone(funcs))

	if m.RegisterFuncsFn != nil {
		return m.RegisterFuncsFn(funcs)
	}

	return nil
}

// Calls returns a copy of the calls, in the order they were made:
// changing a function map it returns changes no recorded call.
func (m *MockUI) Calls() []Call {
	m.mu.Lock()
	defer m.mu.Unlock()

	calls := make([]Call, 0, len(m.calls))

	for _, c := range m.calls {
		args := make([]any, 0, len(c.Args))

		for _, a := range c.Args {
			if funcs, ok := a.(template.FuncMap); ok {
				a = maps.Clone(funcs)
			}

			args = append(args, a)
		}

		calls = append(calls, Call{Method: c.Method, Args: args})
	}

	return calls
}

// CallCount returns how many calls were made to method.
func (m *MockUI) CallCount(method string) int {
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
func (m *MockUI) Reset() {
	m.mu.Lock()

	m.calls = nil
	m.mu.Unlock()
}

// record appends a call.
func (m *MockUI) record(method string, args ...any) {
	m.mu.Lock()

	m.calls = append(m.calls, Call{Method: method, Args: args})
	m.mu.Unlock()
}
