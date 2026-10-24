package modulesconfig_test

import (
	"testing"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config/configtest"
)

// Docs: modules/config.mdx#testing
// "`configtest.MockConfig` satisfies `gas.ConfigProvider`. Each method records the call in `Calls` and delegates to its `Fn` field (`GetFn`, `BindFn`, ...) when set; with no `Fn`, it returns the zero value, so `Bind` leaves the struct untouched and returns `nil`. `CallCount(method)` and `Reset()` help with assertions."
func TestMockConfig(t *testing.T) {
	t.Parallel()
	var _ gas.ConfigProvider = (*configtest.MockConfig)(nil)

	m := &configtest.MockConfig{GetFn: func(key string) any { return "v:" + key }}
	if got := m.Get("a"); got != "v:a" {
		t.Fatalf("Get with GetFn = %v, want v:a", got)
	}
	if len(m.Calls) != 1 || m.Calls[0].Method != "Get" || m.Calls[0].Args[0] != "a" {
		t.Fatalf("Calls = %+v, want one Get(a)", m.Calls)
	}

	zero := &configtest.MockConfig{}
	dest := struct{ Port int }{Port: 8080}
	if err := zero.Bind(&dest); err != nil || dest.Port != 8080 {
		t.Fatalf("Bind with no BindFn = %v, Port %d; want nil and the struct untouched", err, dest.Port)
	}
	if v, ok := zero.Find("x"); v != nil || ok {
		t.Fatalf("Find with no FindFn = %v, %v; want zero values", v, ok)
	}
	if zero.Get("x") != nil || zero.Values() != nil || zero.SetDefaults(map[string]any{}) != nil {
		t.Fatal("methods without an Fn returned non-zero values")
	}
	zero.Get("y")
	if zero.CallCount("Get") != 2 || zero.CallCount("Bind") != 1 {
		t.Fatalf("CallCount Get=%d Bind=%d, want 2 and 1", zero.CallCount("Get"), zero.CallCount("Bind"))
	}
	zero.Reset()
	if len(zero.Calls) != 0 || zero.CallCount("Get") != 0 {
		t.Fatalf("after Reset: %d calls", len(zero.Calls))
	}
}

// Docs: modules/config.mdx#testing
// "`configtest.NewMockConfigWithValues` builds a mock whose `Fn` fields delegate to a real `*config.Config` seeded with the values you pass. It does not read the process environment, so the machine running the test cannot leak into it."
func TestMockConfigWithValues(t *testing.T) {
	t.Setenv("DOCSCLAIM_MODCFG_LEAK", "from-machine")
	t.Setenv("DATABASE__PORT", "9999")
	m, err := configtest.NewMockConfigWithValues(map[string]any{"Database": map[string]any{"Host": "h"}})
	if err != nil {
		t.Fatal(err)
	}
	if m.Get("database.host") != "h" {
		t.Errorf("Get(database.host) = %v, want h", m.Get("database.host"))
	}
	if v, ok := m.Find("DATABASE.HOST"); !ok || v != "h" {
		t.Errorf("Find = %v, %v; want real case-insensitive lookup", v, ok)
	}
	if m.Get("docsclaim_modcfg_leak") != nil || m.Get("database.port") != nil {
		t.Errorf("process environment leaked: %v / %v", m.Get("docsclaim_modcfg_leak"), m.Get("database.port"))
	}
	var out struct {
		Database struct {
			Host string
			Port int `validate:"required"`
		}
	}
	if err := m.Bind(&out); err == nil {
		t.Error("Bind skipped validation, want a real Bind")
	}
	if out.Database.Host != "h" || m.CallCount("Bind") != 1 {
		t.Errorf("Bind = %+v, calls %d; want real binding, recorded", out, m.CallCount("Bind"))
	}
}
