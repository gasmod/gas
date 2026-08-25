// Package migratetest provides a mock implementation of gas.MigrationManager
// for use in tests. The mock records all calls and allows configuring
// per-method behavior via function fields.
//
//	mock := &migratetest.MockMigrationManager{}
//	mock.RunPendingFn = func() error {
//	    return nil
//	}
package migratetest

import (
	"io/fs"
	"sync"

	"github.com/gasmod/gas"
)

// MockMigrationManager is a configurable mock of gas.MigrationManager. Each
// method delegates to its corresponding Fn field if set, otherwise returns the
// zero value. All calls are recorded in the Calls slice for assertions.
type MockMigrationManager struct {
	NameFn          func() string
	InitFn          func() error
	CloseFn         func() error
	RegisterFn      func(service string, m gas.Migration)
	RegisterSliceFn func(service string, migrations []gas.Migration)
	RegisterFSFn    func(service string, fsys fs.FS) error
	RunPendingFn    func() error
	DownFn          func(n int) error
	Calls           []Call

	mu sync.Mutex
}

var _ gas.MigrationManager = (*MockMigrationManager)(nil)

// Call records a single method invocation on the mock.
type Call struct {
	Method string
	Args   []any
}

func (m *MockMigrationManager) record(method string, args ...any) {
	m.mu.Lock()
	m.Calls = append(m.Calls, Call{Method: method, Args: args})
	m.mu.Unlock()
}

// Name records the call and delegates to NameFn if set.
func (m *MockMigrationManager) Name() string {
	m.record("Name")
	if m.NameFn != nil {
		return m.NameFn()
	}
	return "gas/migrate"
}

// Init records the call and delegates to InitFn if set.
func (m *MockMigrationManager) Init() error {
	m.record("Init")
	if m.InitFn != nil {
		return m.InitFn()
	}
	return nil
}

// Close records the call and delegates to CloseFn if set.
func (m *MockMigrationManager) Close() error {
	m.record("Close")
	if m.CloseFn != nil {
		return m.CloseFn()
	}
	return nil
}

// Register records the call and delegates to RegisterFn if set.
func (m *MockMigrationManager) Register(service string, migration gas.Migration) {
	m.record("Register", service, migration)
	if m.RegisterFn != nil {
		m.RegisterFn(service, migration)
	}
}

// RegisterSlice records the call and delegates to RegisterSliceFn if set.
func (m *MockMigrationManager) RegisterSlice(service string, migrations []gas.Migration) {
	m.record("RegisterSlice", service, migrations)
	if m.RegisterSliceFn != nil {
		m.RegisterSliceFn(service, migrations)
	}
}

// RegisterFS records the call and delegates to RegisterFSFn if set.
func (m *MockMigrationManager) RegisterFS(service string, fsys fs.FS) error {
	m.record("RegisterFS", service, fsys)
	if m.RegisterFSFn != nil {
		return m.RegisterFSFn(service, fsys)
	}
	return nil
}

// RunPending records the call and delegates to RunPendingFn if set.
func (m *MockMigrationManager) RunPending() error {
	m.record("RunPending")
	if m.RunPendingFn != nil {
		return m.RunPendingFn()
	}
	return nil
}

// Down records the call and delegates to DownFn if set.
func (m *MockMigrationManager) Down(n int) error {
	m.record("Down", n)
	if m.DownFn != nil {
		return m.DownFn(n)
	}
	return nil
}

// Reset clears all recorded calls.
func (m *MockMigrationManager) Reset() {
	m.mu.Lock()
	m.Calls = nil
	m.mu.Unlock()
}

// CallCount returns the number of times the given method was called.
func (m *MockMigrationManager) CallCount(method string) int {
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
