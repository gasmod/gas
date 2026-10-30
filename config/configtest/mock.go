package configtest

import (
	"context"
	"fmt"
	"sync"

	"github.com/gasmod/gas/config"
)

var _ config.ConfigProvider = (*MockConfig)(nil)

// MockConfig is a config.ConfigProvider that records its calls, as
// described in the package documentation. Each method calls the field
// named after it, when it is set.
type MockConfig struct {
	SetDefaultFn          func(key string, value any) error
	SetDefaultsFn         func(values any) error
	SetFn                 func(key string, value any) error
	BindFn                func(dest any) error
	GetFn                 func(key string) any
	FindFn                func(key string) (any, bool)
	ValuesFn              func() map[string]any
	LoadProviderFn        func(p config.Provider) error
	LoadProviderContextFn func(ctx context.Context, p config.Provider) error
	Calls                 []Call

	mu sync.Mutex
}

// Call is one call of a MockConfig method.
type Call struct {
	Method string
	Args   []any
}

// NewMockConfigWithValues returns a MockConfig whose fields are the methods
// of a config.Config holding values, as described in the package
// documentation.
func NewMockConfigWithValues(values map[string]any) (*MockConfig, error) {
	c := config.New()
	if values != nil {
		err := c.SetDefaults(values)
		if err != nil {
			return nil, fmt.Errorf("config.SetDefaults: %w", err)
		}
	}

	m := &MockConfig{
		SetDefaultFn:          c.SetDefault,
		SetDefaultsFn:         c.SetDefaults,
		SetFn:                 c.Set,
		BindFn:                c.Bind,
		GetFn:                 c.Get,
		FindFn:                c.Find,
		ValuesFn:              c.Values,
		LoadProviderFn:        c.LoadProvider,
		LoadProviderContextFn: c.LoadProviderContext,
	}

	return m, nil
}

// SetDefault records the call and delegates to SetDefaultFn if set.
func (m *MockConfig) SetDefault(key string, value any) error {
	m.record("SetDefault", key, value)

	if m.SetDefaultFn != nil {
		return m.SetDefaultFn(key, value)
	}

	return nil
}

// SetDefaults records the call and delegates to SetDefaultsFn if set.
func (m *MockConfig) SetDefaults(values any) error {
	m.record("SetDefaults", values)

	if m.SetDefaultsFn != nil {
		return m.SetDefaultsFn(values)
	}

	return nil
}

// Set records the call and delegates to SetFn if set.
func (m *MockConfig) Set(key string, value any) error {
	m.record("Set", key, value)

	if m.SetFn != nil {
		return m.SetFn(key, value)
	}

	return nil
}

// Bind records the call and delegates to BindFn if set.
func (m *MockConfig) Bind(dest any) error {
	m.record("Bind", dest)

	if m.BindFn != nil {
		return m.BindFn(dest)
	}

	return nil
}

// Get records the call and delegates to GetFn if set.
func (m *MockConfig) Get(key string) any {
	m.record("Get", key)

	if m.GetFn != nil {
		return m.GetFn(key)
	}

	return nil
}

// Find records the call and delegates to FindFn if set.
func (m *MockConfig) Find(key string) (value any, exist bool) {
	m.record("Find", key)

	if m.FindFn != nil {
		return m.FindFn(key)
	}

	return nil, false
}

// Values records the call and delegates to ValuesFn if set.
func (m *MockConfig) Values() map[string]any {
	m.record("Values")

	if m.ValuesFn != nil {
		return m.ValuesFn()
	}

	return nil
}

// LoadProvider records the call and delegates to LoadProviderFn if set.
func (m *MockConfig) LoadProvider(p config.Provider) error {
	m.record("LoadProvider", p)

	if m.LoadProviderFn != nil {
		return m.LoadProviderFn(p)
	}

	return nil
}

// LoadProviderContext records the call, without ctx, and delegates to
// LoadProviderContextFn if set.
func (m *MockConfig) LoadProviderContext(ctx context.Context, p config.Provider) error {
	m.record("LoadProviderContext", p)

	if m.LoadProviderContextFn != nil {
		return m.LoadProviderContextFn(ctx, p)
	}

	return nil
}

// Reset clears all recorded calls.
func (m *MockConfig) Reset() {
	m.mu.Lock()

	m.Calls = nil
	m.mu.Unlock()
}

// CallCount returns the number of times the given method was called.
func (m *MockConfig) CallCount(method string) int {
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

func (m *MockConfig) record(method string, args ...any) {
	m.mu.Lock()

	m.Calls = append(m.Calls, Call{Method: method, Args: args})
	m.mu.Unlock()
}
