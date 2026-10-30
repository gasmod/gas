package migratetest

import (
	"context"
	"io/fs"
	"slices"
	"sync"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/migrate"
)

// Call is one call of the mock's method.
type Call struct {
	// Method is the name of the method called.
	Method string
	// Args are the method's arguments, in order.
	Args []any
}

// MockMigrationManager is a gas.MigrationManager that records its calls,
// as described in the package documentation.
type MockMigrationManager struct {
	// NameFn is what Name returns. When it is nil, Name returns
	// migrate.ServiceName.
	NameFn func() string
	// InitFn is what Init returns. When it is nil, Init returns nil.
	InitFn func(ctx context.Context) error
	// CloseFn is what Close returns. When it is nil, Close returns nil.
	CloseFn func(ctx context.Context) error
	// RegisterFn is called by Register, when it is set.
	RegisterFn func(service gas.Service, m gas.Migration)
	// RegisterSliceFn is called by RegisterSlice, when it is set.
	RegisterSliceFn func(service gas.Service, migrations []gas.Migration)
	// RegisterFSFn is what RegisterFS returns. When it is nil, RegisterFS
	// returns nil.
	RegisterFSFn func(service gas.Service, fsys fs.FS) error
	// RunPendingFn is what RunPending returns. When it is nil, RunPending
	// returns nil.
	RunPendingFn func() error
	// DownFn is what Down returns. When it is nil, Down returns nil.
	DownFn func(n int) error

	// calls holds the calls in the order they were made.
	calls []Call
	// mu guards calls, since the mock may be called from several
	// goroutines.
	mu sync.Mutex
}

var _ gas.MigrationManager = (*MockMigrationManager)(nil)

// Name records the call and returns NameFn's result.
func (m *MockMigrationManager) Name() string {
	m.record("Name")

	if m.NameFn != nil {
		return m.NameFn()
	}

	return migrate.ServiceName
}

// Init records the call and returns InitFn's result.
func (m *MockMigrationManager) Init(ctx context.Context) error {
	m.record("Init", ctx)

	if m.InitFn != nil {
		return m.InitFn(ctx)
	}

	return nil
}

// Close records the call and returns CloseFn's result.
func (m *MockMigrationManager) Close(ctx context.Context) error {
	m.record("Close", ctx)

	if m.CloseFn != nil {
		return m.CloseFn(ctx)
	}

	return nil
}

// Register records the call and calls RegisterFn.
func (m *MockMigrationManager) Register(service gas.Service, migration gas.Migration) {
	m.record("Register", service, migration)

	if m.RegisterFn != nil {
		m.RegisterFn(service, migration)
	}
}

// RegisterSlice records the call and calls RegisterSliceFn.
func (m *MockMigrationManager) RegisterSlice(service gas.Service, migrations []gas.Migration) {
	m.record("RegisterSlice", service, migrations)

	if m.RegisterSliceFn != nil {
		m.RegisterSliceFn(service, migrations)
	}
}

// RegisterFS records the call and returns RegisterFSFn's result.
func (m *MockMigrationManager) RegisterFS(service gas.Service, fsys fs.FS) error {
	m.record("RegisterFS", service, fsys)

	if m.RegisterFSFn != nil {
		return m.RegisterFSFn(service, fsys)
	}

	return nil
}

// RunPending records the call and returns RunPendingFn's result.
func (m *MockMigrationManager) RunPending() error {
	m.record("RunPending")

	if m.RunPendingFn != nil {
		return m.RunPendingFn()
	}

	return nil
}

// Down records the call and returns DownFn's result.
func (m *MockMigrationManager) Down(n int) error {
	m.record("Down", n)

	if m.DownFn != nil {
		return m.DownFn(n)
	}

	return nil
}

// Calls returns a copy of the calls, in the order they were made.
func (m *MockMigrationManager) Calls() []Call {
	m.mu.Lock()
	defer m.mu.Unlock()

	return slices.Clone(m.calls)
}

// CallCount returns how many calls were made to method.
func (m *MockMigrationManager) CallCount(method string) int {
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
func (m *MockMigrationManager) Reset() {
	m.mu.Lock()

	m.calls = nil
	m.mu.Unlock()
}

// record appends a call.
func (m *MockMigrationManager) record(method string, args ...any) {
	m.mu.Lock()

	m.calls = append(m.calls, Call{Method: method, Args: args})
	m.mu.Unlock()
}
