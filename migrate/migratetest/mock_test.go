package migratetest

import (
	"context"
	"errors"
	"io/fs"
	"reflect"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/gasmod/gas"
)

// stubService is a minimal gas.Service to pass to the Register methods.
type stubService struct{ name string }

func (s *stubService) Name() string                { return s.name }
func (s *stubService) Init(context.Context) error  { return nil }
func (s *stubService) Close(context.Context) error { return nil }

// mockCase drives one MockMigrationManager method: call invokes it, setErr
// installs an Fn that returns err and records that it ran, and args are the
// arguments the mock must record. returnsErr is false for Register and
// RegisterSlice, which have no result.
type mockCase struct {
	call       func(m *MockMigrationManager) error
	setErr     func(m *MockMigrationManager, err error, ran *bool)
	method     string
	args       []any
	returnsErr bool
}

func mockCases() []mockCase {
	ctx := context.Background()
	svc := &stubService{name: "svc"}
	mig := gas.Migration{Version: "001", Up: "CREATE TABLE t (id INT)"}
	migs := []gas.Migration{mig}
	fsys := fstest.MapFS{}

	return []mockCase{
		{
			method:     "Init",
			returnsErr: true,
			call:       func(m *MockMigrationManager) error { return m.Init(ctx) },
			setErr: func(m *MockMigrationManager, err error, ran *bool) {
				m.InitFn = func() error {
					*ran = true
					return err
				}
			},
		},
		{
			method:     "Close",
			returnsErr: true,
			call:       func(m *MockMigrationManager) error { return m.Close(ctx) },
			setErr: func(m *MockMigrationManager, err error, ran *bool) {
				m.CloseFn = func() error {
					*ran = true
					return err
				}
			},
		},
		{
			method: "Register",
			args:   []any{svc, mig},
			call: func(m *MockMigrationManager) error {
				m.Register(svc, mig)
				return nil
			},
			setErr: func(m *MockMigrationManager, _ error, ran *bool) {
				m.RegisterFn = func(gas.Service, gas.Migration) { *ran = true }
			},
		},
		{
			method: "RegisterSlice",
			args:   []any{svc, migs},
			call: func(m *MockMigrationManager) error {
				m.RegisterSlice(svc, migs)
				return nil
			},
			setErr: func(m *MockMigrationManager, _ error, ran *bool) {
				m.RegisterSliceFn = func(gas.Service, []gas.Migration) { *ran = true }
			},
		},
		{
			method:     "RegisterFS",
			args:       []any{svc, fsys},
			returnsErr: true,
			call:       func(m *MockMigrationManager) error { return m.RegisterFS(svc, fsys) },
			setErr: func(m *MockMigrationManager, err error, ran *bool) {
				m.RegisterFSFn = func(gas.Service, fs.FS) error {
					*ran = true
					return err
				}
			},
		},
		{
			method:     "RunPending",
			returnsErr: true,
			call:       func(m *MockMigrationManager) error { return m.RunPending() },
			setErr: func(m *MockMigrationManager, err error, ran *bool) {
				m.RunPendingFn = func() error {
					*ran = true
					return err
				}
			},
		},
		{
			method:     "Down",
			args:       []any{3},
			returnsErr: true,
			call:       func(m *MockMigrationManager) error { return m.Down(3) },
			setErr: func(m *MockMigrationManager, err error, ran *bool) {
				m.DownFn = func(int) error {
					*ran = true
					return err
				}
			},
		},
	}
}

func TestMockMigrationManager_Methods(t *testing.T) {
	t.Parallel()

	for _, tc := range mockCases() {
		t.Run(tc.method, func(t *testing.T) {
			t.Parallel()

			m := &MockMigrationManager{}
			if err := tc.call(m); err != nil {
				t.Errorf("default %s = %v, want nil", tc.method, err)
			}
			if len(m.Calls) != 1 || m.Calls[0].Method != tc.method {
				t.Fatalf("Calls = %+v, want one %s call", m.Calls, tc.method)
			}
			if !reflect.DeepEqual(m.Calls[0].Args, tc.args) {
				t.Errorf("recorded args = %#v, want %#v", m.Calls[0].Args, tc.args)
			}

			wantErr := errors.New("injected")
			var ran bool
			tc.setErr(m, wantErr, &ran)
			err := tc.call(m)
			if tc.returnsErr && !errors.Is(err, wantErr) {
				t.Errorf("%s with Fn = %v, want %v", tc.method, err, wantErr)
			}
			if !ran {
				t.Errorf("%s did not call its Fn", tc.method)
			}
			if got := m.CallCount(tc.method); got != 2 {
				t.Errorf("CallCount(%s) = %d, want 2", tc.method, got)
			}
		})
	}
}

func TestMockMigrationManager_Name(t *testing.T) {
	t.Parallel()

	m := &MockMigrationManager{}
	if got := m.Name(); got != "gas/migrate" {
		t.Errorf("default Name() = %q, want %q", got, "gas/migrate")
	}

	m.NameFn = func() string { return "custom" }
	if got := m.Name(); got != "custom" {
		t.Errorf("Name() with NameFn = %q, want %q", got, "custom")
	}
	if got := m.CallCount("Name"); got != 2 {
		t.Errorf("CallCount(Name) = %d, want 2", got)
	}
}

func TestMockMigrationManager_CallCountAndReset(t *testing.T) {
	t.Parallel()

	m := &MockMigrationManager{}
	_ = m.RunPending()
	_ = m.RunPending()
	_ = m.Down(1)

	if got := m.CallCount("RunPending"); got != 2 {
		t.Errorf("CallCount(RunPending) = %d, want 2", got)
	}
	if got := m.CallCount("Down"); got != 1 {
		t.Errorf("CallCount(Down) = %d, want 1", got)
	}
	if got := m.CallCount("Init"); got != 0 {
		t.Errorf("CallCount(Init) = %d, want 0", got)
	}

	m.Reset()
	if len(m.Calls) != 0 || m.CallCount("RunPending") != 0 {
		t.Errorf("after Reset, Calls = %+v, want none", m.Calls)
	}
}

// TestMockMigrationManager_ConcurrentCalls checks, under -race, that the mock
// records concurrent calls without a data race and loses none.
func TestMockMigrationManager_ConcurrentCalls(t *testing.T) {
	t.Parallel()

	const workers, perWorker = 8, 50
	m := &MockMigrationManager{}

	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for range perWorker {
				_ = m.RunPending()
				_ = m.CallCount("RunPending")
			}
		})
	}
	wg.Wait()

	if got := m.CallCount("RunPending"); got != workers*perWorker {
		t.Errorf("CallCount(RunPending) = %d, want %d", got, workers*perWorker)
	}
}
