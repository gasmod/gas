// Package configtest provides a mock implementation of the config provider
// API for use in tests. The mock records all calls and allows configuring
// per-method behavior via function fields.
//
//	mock := &configtest.MockConfig{}
//	mock.GetFn = func(key string) any {
//	    if key == "database.host" {
//	        return "localhost"
//	    }
//	    return nil
//	}
//
// For tests that just need a working config seeded with known values, use
// NewMockConfigWithValues, which delegates to a real *config.Config under the
// hood so Get/Find/Bind retain their normal semantics.
package configtest

import (
	"fmt"
	"sync"

	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/config/providers"
)

var _ providers.Provider = (*staticProvider)(nil)

// MockConfig is a configurable mock that structurally satisfies the
// gas.ConfigProvider interface. Each method delegates to its corresponding
// Fn field if set, otherwise returns the zero value. All calls are recorded
// in the Calls slice for assertions.
type MockConfig struct {
	SetDefaultFn  func(key string, value any)
	SetDefaultsFn func(values any) error
	SetFn         func(key string, value any)
	BindFn        func(dest any, options ...config.BindOption) error
	GetFn         func(key string) any
	FindFn        func(key string) (any, bool)
	ValuesFn      func() map[string]any
	Calls         []Call

	mu sync.Mutex
}

// Call records a single method invocation on the mock.
type Call struct {
	Method string
	Args   []any
}

func (m *MockConfig) record(method string, args ...any) {
	m.mu.Lock()
	m.Calls = append(m.Calls, Call{Method: method, Args: args})
	m.mu.Unlock()
}

// SetDefault records the call and delegates to SetDefaultFn if set.
func (m *MockConfig) SetDefault(key string, value any) {
	m.record("SetDefault", key, value)
	if m.SetDefaultFn != nil {
		m.SetDefaultFn(key, value)
	}
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
func (m *MockConfig) Set(key string, value any) {
	m.record("Set", key, value)
	if m.SetFn != nil {
		m.SetFn(key, value)
	}
}

// Bind records the call and delegates to BindFn if set.
func (m *MockConfig) Bind(dest any, options ...config.BindOption) error {
	m.record("Bind", dest)
	if m.BindFn != nil {
		return m.BindFn(dest, options...)
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

// NewMockConfigWithValues returns a MockConfig whose Fn fields delegate to a
// real *config.Config seeded with the provided values. This preserves real
// key-normalization, Get/Find, and Bind semantics while still recording calls
// and allowing individual Fn fields to be overridden afterwards.
func NewMockConfigWithValues(values map[string]any) (*MockConfig, error) {
	c := config.New(config.WithProvider(&staticProvider{}))
	if err := c.Load(); err != nil {
		return nil, fmt.Errorf("config.Load: %w", err)
	}
	if values != nil {
		if err := c.SetDefaults(values); err != nil {
			return nil, fmt.Errorf("config.SetDefaults: %w", err)
		}
	}

	m := &MockConfig{
		SetDefaultFn:  c.SetDefault,
		SetDefaultsFn: c.SetDefaults,
		SetFn:         c.Set,
		BindFn:        c.Bind,
		GetFn:         c.Get,
		FindFn:        c.Find,
		ValuesFn:      c.Values,
	}
	return m, nil
}

// staticProvider is a no-op provider used by NewMockConfigWithValues to avoid
// the default EnvProvider leaking real environment variables into tests.
type staticProvider struct{}

func (staticProvider) Name() string                  { return "configtest.static" }
func (staticProvider) Load() (map[string]any, error) { return map[string]any{}, nil }
