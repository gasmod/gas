package modulesmigrations_test

import (
	"context"
	"errors"
	"io/fs"
	"reflect"
	"testing"
	"testing/fstest"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/migrate/migratetest"
)

// Docs: modules/migrations.mdx#testing
// "`migratetest.MockMigrationManager` implements `gas.MigrationManager` for
// unit tests."
var _ gas.MigrationManager = (*migratetest.MockMigrationManager)(nil)

// Docs: modules/migrations.mdx#testing
// "Each method records the call in `Calls` and delegates to the matching `Fn`
// field (`RegisterFn`, `RegisterSliceFn`, `RegisterFSFn`, `RunPendingFn`,
// `DownFn`, and so on) when it is set; otherwise it returns nil, and `Name`
// returns `gas/migrate`."
func TestMockRecordsAndDelegates(t *testing.T) {
	t.Parallel()
	svc := &owner{name: "svc"}
	mig := logMig("001")
	fsys := fstest.MapFS{}

	m := &migratetest.MockMigrationManager{}
	if got := m.Name(); got != "gas/migrate" {
		t.Errorf("Name() = %q, want gas/migrate", got)
	}
	m.Register(svc, mig)
	m.RegisterSlice(svc, []gas.Migration{mig})
	for name, err := range map[string]error{
		"Init":       m.Init(t.Context()),
		"Close":      m.Close(t.Context()),
		"RegisterFS": m.RegisterFS(svc, fsys),
		"RunPending": m.RunPending(),
		"Down":       m.Down(2),
	} {
		if err != nil {
			t.Errorf("default %s = %v, want nil", name, err)
		}
	}
	var methods []string
	for _, c := range m.Calls {
		methods = append(methods, c.Method)
	}
	want := []string{"Name", "Register", "RegisterSlice"}
	if !reflect.DeepEqual(methods[:3], want) || len(methods) != 8 {
		t.Errorf("recorded calls = %v, want %v then the five error-returning methods", methods, want)
	}
	if args := m.Calls[1].Args; !reflect.DeepEqual(args, []any{svc, mig}) {
		t.Errorf("Register recorded args %#v", args)
	}

	errFn := errors.New("from Fn")
	ran := map[string]bool{}
	d := &migratetest.MockMigrationManager{
		NameFn:          func() string { ran["Name"] = true; return "custom" },
		RegisterFn:      func(gas.Service, gas.Migration) { ran["Register"] = true },
		RegisterSliceFn: func(gas.Service, []gas.Migration) { ran["RegisterSlice"] = true },
		RegisterFSFn:    func(gas.Service, fs.FS) error { ran["RegisterFS"] = true; return errFn },
		RunPendingFn:    func() error { ran["RunPending"] = true; return errFn },
		DownFn:          func(n int) error { ran["Down"] = n == 3; return errFn },
		InitFn:          func() error { ran["Init"] = true; return errFn },
		CloseFn:         func() error { ran["Close"] = true; return errFn },
	}
	if d.Name() != "custom" {
		t.Error("Name did not delegate to NameFn")
	}
	d.Register(svc, mig)
	d.RegisterSlice(svc, nil)
	for name, err := range map[string]error{
		"RegisterFS": d.RegisterFS(svc, fsys),
		"RunPending": d.RunPending(),
		"Down":       d.Down(3),
		"Init":       d.Init(context.Background()),
		"Close":      d.Close(context.Background()),
	} {
		if err != errFn { //nolint:errorlint // the Fn's error must come back as is
			t.Errorf("%s = %v, want the Fn's error", name, err)
		}
	}
	for _, name := range []string{"Name", "Register", "RegisterSlice", "RegisterFS", "RunPending", "Down", "Init", "Close"} {
		if !ran[name] {
			t.Errorf("%sFn did not run (or got the wrong argument)", name)
		}
	}
	if len(d.Calls) != 8 {
		t.Errorf("calls recorded with Fns set = %d, want 8", len(d.Calls))
	}
}

// Docs: modules/migrations.mdx#testing
// "`CallCount(method)` counts the calls to one method and `Reset` clears
// `Calls`. The mock has no `ClearDirty` or `CheckReady`."
func TestMockCallCountResetAndMissingMethods(t *testing.T) {
	t.Parallel()
	m := &migratetest.MockMigrationManager{}
	_ = m.RunPending()
	_ = m.RunPending()
	_ = m.Down(1)
	if got := m.CallCount("RunPending"); got != 2 {
		t.Errorf("CallCount(RunPending) = %d, want 2", got)
	}
	if got := m.CallCount("Down"); got != 1 {
		t.Errorf("CallCount(Down) = %d, want 1", got)
	}
	m.Reset()
	if len(m.Calls) != 0 || m.CallCount("RunPending") != 0 {
		t.Errorf("after Reset: Calls = %v, want empty", m.Calls)
	}
	typ := reflect.TypeFor[*migratetest.MockMigrationManager]()
	for _, name := range []string{"ClearDirty", "CheckReady"} {
		if _, ok := typ.MethodByName(name); ok {
			t.Errorf("the mock has %s", name)
		}
	}
}
