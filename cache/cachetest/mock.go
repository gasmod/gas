// Package cachetest provides a mock implementation of gas.CacheProvider
// for use in tests. The mock records all calls and allows configuring
// per-method behavior via function fields.
//
//	mock := &cachetest.MockCache{}
//	mock.GetFn = func(ctx context.Context, key string) ([]byte, error) {
//	    return []byte("value"), nil
//	}
package cachetest

import (
	"context"
	"sync"
	"time"

	"github.com/gasmod/gas"
)

// MockCache is a configurable mock of gas.CacheProvider. Each method
// delegates to its corresponding Fn field if set, otherwise returns the
// zero value. All calls are recorded in the Calls slice for assertions.
type MockCache struct {
	GetFn         func(ctx context.Context, key string) ([]byte, error)
	SetFn         func(ctx context.Context, key string, value []byte, ttl time.Duration) error
	DeleteFn      func(ctx context.Context, key string) error
	ExistsFn      func(ctx context.Context, key string) (bool, error)
	CheckHealthFn func(ctx context.Context) error
	CheckReadyFn  func(ctx context.Context) error
	Calls         []Call

	mu sync.Mutex
}

var _ gas.CacheProvider = (*MockCache)(nil)
var _ gas.HealthReporter = (*MockCache)(nil)
var _ gas.ReadyReporter = (*MockCache)(nil)

// Call records a single method invocation on the mock.
type Call struct {
	Method string
	Args   []any
}

func (m *MockCache) record(method string, args ...any) {
	m.mu.Lock()
	m.Calls = append(m.Calls, Call{Method: method, Args: args})
	m.mu.Unlock()
}

// Get records the call and delegates to GetFn if set.
func (m *MockCache) Get(ctx context.Context, key string) ([]byte, error) {
	m.record("Get", key)
	if m.GetFn != nil {
		return m.GetFn(ctx, key)
	}
	return nil, nil
}

// Set records the call and delegates to SetFn if set.
func (m *MockCache) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	m.record("Set", key, value, ttl)
	if m.SetFn != nil {
		return m.SetFn(ctx, key, value, ttl)
	}
	return nil
}

// Delete records the call and delegates to DeleteFn if set.
func (m *MockCache) Delete(ctx context.Context, key string) error {
	m.record("Delete", key)
	if m.DeleteFn != nil {
		return m.DeleteFn(ctx, key)
	}
	return nil
}

// Exists records the call and delegates to ExistsFn if set.
func (m *MockCache) Exists(ctx context.Context, key string) (bool, error) {
	m.record("Exists", key)
	if m.ExistsFn != nil {
		return m.ExistsFn(ctx, key)
	}
	return false, nil
}

// CheckHealth records the call and delegates to CheckHealthFn if set.
func (m *MockCache) CheckHealth(ctx context.Context) error {
	m.record("CheckHealth")
	if m.CheckHealthFn != nil {
		return m.CheckHealthFn(ctx)
	}
	return nil
}

// CheckReady records the call and delegates to CheckReadyFn if set.
func (m *MockCache) CheckReady(ctx context.Context) error {
	m.record("CheckReady")
	if m.CheckReadyFn != nil {
		return m.CheckReadyFn(ctx)
	}
	return nil
}

// Reset clears all recorded calls.
func (m *MockCache) Reset() {
	m.mu.Lock()
	m.Calls = nil
	m.mu.Unlock()
}

// CallCount returns the number of times the given method was called.
func (m *MockCache) CallCount(method string) int {
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
