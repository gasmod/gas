package templatetest

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"slices"
	"sync"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/template"
)

// Call is one call of the mock's method.
type Call struct {
	// Method is the name of the method called.
	Method string
	// Args are the method's arguments after the context, in order.
	Args []any
}

// MockTemplate is a gas.TemplateProvider that records its calls, as
// described in the package documentation.
type MockTemplate struct {
	// GetFn is what Get returns. When it is nil, Get returns an error
	// wrapping template.ErrTemplateNotFound.
	GetFn func(ctx context.Context, name string) ([]byte, error)
	// ListFn is what List returns. When it is nil, List returns an empty
	// list.
	ListFn func(ctx context.Context) ([]string, error)
	// RegisterFn is what Register returns. When it is nil, Register
	// returns nil.
	RegisterFn func(ctx context.Context, name string, content []byte) error
	// RegisterFSFn is what RegisterFS returns. When it is nil, RegisterFS
	// returns nil.
	RegisterFSFn func(ctx context.Context, fsys fs.FS) error

	// calls holds the calls in the order they were made.
	calls []Call
	// mu guards calls, since the mock may be called from several
	// goroutines.
	mu sync.Mutex
}

var _ gas.TemplateProvider = (*MockTemplate)(nil)

// Get records the call and returns GetFn's result.
func (m *MockTemplate) Get(ctx context.Context, name string) ([]byte, error) {
	m.record("Get", name)

	if m.GetFn != nil {
		return m.GetFn(ctx, name)
	}

	return nil, fmt.Errorf("%w: %q", template.ErrTemplateNotFound, name)
}

// List records the call and returns ListFn's result.
func (m *MockTemplate) List(ctx context.Context) ([]string, error) {
	m.record("List")

	if m.ListFn != nil {
		return m.ListFn(ctx)
	}

	return []string{}, nil
}

// Register records the call and returns RegisterFn's result.
func (m *MockTemplate) Register(ctx context.Context, name string, content []byte) error {
	m.record("Register", name, bytes.Clone(content))

	if m.RegisterFn != nil {
		return m.RegisterFn(ctx, name, content)
	}

	return nil
}

// RegisterFS records the call and returns RegisterFSFn's result.
func (m *MockTemplate) RegisterFS(ctx context.Context, fsys fs.FS) error {
	m.record("RegisterFS", fsys)

	if m.RegisterFSFn != nil {
		return m.RegisterFSFn(ctx, fsys)
	}

	return nil
}

// Calls returns a copy of the calls, in the order they were made.
func (m *MockTemplate) Calls() []Call {
	m.mu.Lock()
	defer m.mu.Unlock()

	return slices.Clone(m.calls)
}

// CallCount returns how many calls were made to method.
func (m *MockTemplate) CallCount(method string) int {
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
func (m *MockTemplate) Reset() {
	m.mu.Lock()

	m.calls = nil
	m.mu.Unlock()
}

// record appends a call.
func (m *MockTemplate) record(method string, args ...any) {
	m.mu.Lock()

	m.calls = append(m.calls, Call{Method: method, Args: args})
	m.mu.Unlock()
}
