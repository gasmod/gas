package modulescache_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/cache/cachetest"
)

// Docs: modules/cache.mdx#testing
// "`cachetest.MockCache` implements `gas.CacheProvider`, `gas.HealthReporter`
// and `gas.ReadyReporter`."
var (
	_ gas.CacheProvider  = (*cachetest.MockCache)(nil)
	_ gas.HealthReporter = (*cachetest.MockCache)(nil)
	_ gas.ReadyReporter  = (*cachetest.MockCache)(nil)
)

// Docs: modules/cache.mdx#testing
// "Each method records the call in `Calls` and delegates
// to the matching field (`GetFn`, `SetFn`, `DeleteFn`, `ExistsFn`,
// `CheckHealthFn`, `CheckReadyFn`) when it is set."
func TestMockRecordsAndDelegates(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	sentinel := errors.New("from fn")
	m := &cachetest.MockCache{
		GetFn:         func(context.Context, string) ([]byte, error) { return []byte("got"), sentinel },
		SetFn:         func(context.Context, string, []byte, time.Duration) error { return sentinel },
		DeleteFn:      func(context.Context, string) error { return sentinel },
		ExistsFn:      func(context.Context, string) (bool, error) { return true, sentinel },
		CheckHealthFn: func(context.Context) error { return sentinel },
		CheckReadyFn:  func(context.Context) error { return sentinel },
	}
	if v, err := m.Get(ctx, "k"); string(v) != "got" || !errors.Is(err, sentinel) {
		t.Errorf("Get = %q, %v", v, err)
	}
	if err := m.Set(ctx, "k", []byte("v"), time.Minute); !errors.Is(err, sentinel) {
		t.Errorf("Set = %v", err)
	}
	if err := m.Delete(ctx, "k"); !errors.Is(err, sentinel) {
		t.Errorf("Delete = %v", err)
	}
	if ok, err := m.Exists(ctx, "k"); !ok || !errors.Is(err, sentinel) {
		t.Errorf("Exists = %v, %v", ok, err)
	}
	if err := m.CheckHealth(ctx); !errors.Is(err, sentinel) {
		t.Errorf("CheckHealth = %v", err)
	}
	if err := m.CheckReady(ctx); !errors.Is(err, sentinel) {
		t.Errorf("CheckReady = %v", err)
	}
	want := []string{"Get", "Set", "Delete", "Exists", "CheckHealth", "CheckReady"}
	if len(m.Calls) != len(want) {
		t.Fatalf("Calls = %+v, want %v", m.Calls, want)
	}
	for i, c := range m.Calls {
		if c.Method != want[i] {
			t.Errorf("Calls[%d].Method = %q, want %q", i, c.Method, want[i])
		}
	}
}

// Docs: modules/cache.mdx#testing
// "Unset, `Get` returns
// `nil, nil`, which cache-aside code reads as a hit with an empty value;
// `Exists` returns `false, nil`, and the rest return nil. `CallCount(method)`
// counts the calls to one method and `Reset` clears `Calls`."
func TestMockDefaultsAndCounters(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	m := &cachetest.MockCache{}
	if v, err := m.Get(ctx, "k"); v != nil || err != nil {
		t.Errorf("unset Get = %v, %v; want nil, nil", v, err)
	}
	if ok, err := m.Exists(ctx, "k"); ok || err != nil {
		t.Errorf("unset Exists = %v, %v; want false, nil", ok, err)
	}
	for name, err := range map[string]error{
		"Set":         m.Set(ctx, "k", nil, 0),
		"Delete":      m.Delete(ctx, "k"),
		"CheckHealth": m.CheckHealth(ctx),
		"CheckReady":  m.CheckReady(ctx),
	} {
		if err != nil {
			t.Errorf("unset %s = %v, want nil", name, err)
		}
	}
	_, _ = m.Get(ctx, "again")
	if n := m.CallCount("Get"); n != 2 {
		t.Errorf("CallCount(Get) = %d, want 2", n)
	}
	if n := m.CallCount("Set"); n != 1 {
		t.Errorf("CallCount(Set) = %d, want 1", n)
	}
	m.Reset()
	if len(m.Calls) != 0 || m.CallCount("Get") != 0 {
		t.Errorf("after Reset: Calls = %+v", m.Calls)
	}
}
