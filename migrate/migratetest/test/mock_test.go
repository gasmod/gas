package migratetest_test

import (
	"context"
	"errors"
	"io/fs"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/migrate"
	"github.com/gasmod/gas/migrate/migratetest"
)

// errBoom is what the tests' functions return.
var errBoom = errors.New("boom")

// svc is a gas.Service to register with.
type svc struct{}

// Name returns `svc`.
func (svc) Name() string { return "svc" }

// Init does nothing.
func (svc) Init(context.Context) error { return nil }

// Close does nothing.
func (svc) Close(context.Context) error { return nil }

// The example in the package documentation's Usage section.
func TestDoc_Usage(t *testing.T) {
	t.Parallel()

	m := &migratetest.MockMigrationManager{
		RunPendingFn: func() error { return errBoom },
	}
	err := m.RunPending()

	require.ErrorIs(t, err, errBoom)
	assert.Equal(t, 1, m.CallCount("RunPending"))
}

// RunPendingContext and DownContext, the methods gas.MigrationManager
// requires, record their context and return what RunPendingFn and DownFn
// return.
func TestMock_ContextMethods(t *testing.T) {
	t.Parallel()

	var gotN int

	m := &migratetest.MockMigrationManager{
		RunPendingFn: func() error { return errBoom },
		DownFn: func(n int) error {
			gotN = n

			return errBoom
		},
	}

	var mgr gas.MigrationManager = m

	ctx := t.Context()

	require.ErrorIs(t, mgr.RunPendingContext(ctx), errBoom)
	require.ErrorIs(t, mgr.DownContext(ctx, 3), errBoom)
	assert.Equal(t, 3, gotN)
	assert.Equal(t, []migratetest.Call{
		{Method: "RunPendingContext", Args: []any{ctx}},
		{Method: "DownContext", Args: []any{ctx, 3}},
	}, m.Calls())

	empty := &migratetest.MockMigrationManager{}
	require.NoError(t, empty.RunPendingContext(ctx))
	require.NoError(t, empty.DownContext(ctx, 1))
}

// With every field nil, each method succeeds and does nothing, and Name
// is migrate.ServiceName.
func TestMock_Defaults(t *testing.T) {
	t.Parallel()

	m := &migratetest.MockMigrationManager{}
	ctx := t.Context()

	assert.Equal(t, migrate.ServiceName, m.Name())
	require.NoError(t, m.Init(ctx))
	require.NoError(t, m.Close(ctx))
	m.Register(svc{}, gas.Migration{})
	m.RegisterSlice(svc{}, nil)
	require.NoError(t, m.RegisterFS(svc{}, fstest.MapFS{}))
	require.NoError(t, m.RunPending())
	require.NoError(t, m.Down(1))
}

// Each method records its call, with its arguments, the context of Init
// and Close included, and then calls its field and returns its result.
func TestMock_RecordsAndDelegates(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	mig := gas.Migration{Version: "001"}
	migs := []gas.Migration{mig}
	fsys := fstest.MapFS{}

	var registered, sliced int

	m := &migratetest.MockMigrationManager{
		NameFn: func() string { return "custom" },
		InitFn: func(got context.Context) error {
			assert.Equal(t, ctx, got)

			return errBoom
		},
		CloseFn:         func(context.Context) error { return errBoom },
		RegisterFn:      func(gas.Service, gas.Migration) { registered++ },
		RegisterSliceFn: func(gas.Service, []gas.Migration) { sliced++ },
		RegisterFSFn:    func(gas.Service, fs.FS) error { return errBoom },
		RunPendingFn:    func() error { return errBoom },
		DownFn: func(n int) error {
			assert.Equal(t, 2, n)

			return errBoom
		},
	}

	assert.Equal(t, "custom", m.Name())
	require.ErrorIs(t, m.Init(ctx), errBoom)
	require.ErrorIs(t, m.Close(ctx), errBoom)
	m.Register(svc{}, mig)
	m.RegisterSlice(svc{}, migs)
	require.ErrorIs(t, m.RegisterFS(svc{}, fsys), errBoom)
	require.ErrorIs(t, m.RunPending(), errBoom)
	require.ErrorIs(t, m.Down(2), errBoom)

	assert.Equal(t, 1, registered)
	assert.Equal(t, 1, sliced)
	assert.Equal(t, []migratetest.Call{
		{Method: "Name"},
		{Method: "Init", Args: []any{ctx}},
		{Method: "Close", Args: []any{ctx}},
		{Method: "Register", Args: []any{svc{}, mig}},
		{Method: "RegisterSlice", Args: []any{svc{}, migs}},
		{Method: "RegisterFS", Args: []any{svc{}, fsys}},
		{Method: "RunPending"},
		{Method: "Down", Args: []any{2}},
	}, m.Calls())
}

// Calls returns a copy, CallCount counts one method, and Reset forgets
// every call; all are safe beside calls from other goroutines.
func TestMock_Calls(t *testing.T) {
	t.Parallel()

	m := &migratetest.MockMigrationManager{}

	var wg sync.WaitGroup

	for range 8 {
		wg.Go(func() {
			_ = m.RunPending()
			_ = m.Calls()
			_ = m.CallCount("RunPending")
		})
	}

	wg.Wait()

	calls := m.Calls()
	require.Len(t, calls, 8)

	calls[0].Method = "changed"

	assert.Equal(t, 8, m.CallCount("RunPending"))

	m.Reset()
	assert.Empty(t, m.Calls())
	assert.Zero(t, m.CallCount("RunPending"))
}
