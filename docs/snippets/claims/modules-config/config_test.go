package modulesconfig_test

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth/jwt"
	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/config/configtest"
	"github.com/gasmod/gas/database"
	"github.com/gasmod/gas/email/ses"
	"github.com/gasmod/gas/template/memory"
)

// Docs: modules/config.mdx#what-it-is-for
// "`*config.Config` satisfies `gas.ConfigProvider`, the interface core and every module read their settings through."
func TestConfigSatisfiesConfigProvider(t *testing.T) {
	t.Parallel()
	var _ gas.ConfigProvider = (*config.Config)(nil)
}

// Docs: modules/config.mdx#what-it-is-for
// "Core binds its own server settings from it"
// Docs: modules/config.mdx#configuration
// "Core binds its server settings (`server.host`, `server.port`, the server timeouts) from the same config"
func TestCoreBindsServerSettings(t *testing.T) {
	t.Parallel()
	cfg := load(t, values("m", map[string]any{"server": map[string]any{
		"host": "127.0.0.1", "port": "9393", "readtimeout": "7s", "writetimeout": "11s", "idletimeout": "3m",
	}}))
	app := gas.NewApp(gas.WithServiceInstance[gas.ConfigProvider](cfg))
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	srv := app.Server()
	if srv.Addr != "127.0.0.1:9393" || srv.ReadTimeout.String() != "7s" ||
		srv.WriteTimeout.String() != "11s" || srv.IdleTimeout.String() != "3m0s" {
		t.Fatalf("server Addr=%q Read=%v Write=%v Idle=%v, want the bound settings",
			srv.Addr, srv.ReadTimeout, srv.WriteTimeout, srv.IdleTimeout)
	}
}

// bindRecorder is a MockConfig whose Bind records the destination type and
// fails, so Init stops right after binding.
func bindRecorder() (*configtest.MockConfig, *[]reflect.Type) {
	var dests []reflect.Type
	m := &configtest.MockConfig{BindFn: func(dest any, _ ...config.BindOption) error {
		dests = append(dests, reflect.TypeOf(dest))
		return errBoom
	}}
	return m, &dests
}

// hasSection reports whether the struct behind dest has a field whose
// lowercased name is section.
func hasSection(dest reflect.Type, section string) bool {
	for dest.Kind() == reflect.Pointer {
		dest = dest.Elem()
	}
	for f := range dest.Fields() {
		if strings.ToLower(f.Name) == section {
			return true
		}
	}
	return false
}

// Docs: modules/config.mdx#what-it-is-for
// "each module binds its section (`database.*`, `email.*`, `jwt.*`, ...) in its `Init`."
func TestModulesBindTheirSectionInInit(t *testing.T) {
	t.Parallel()
	type initer interface{ Init(context.Context) error }
	cases := []struct {
		section string
		build   func(*configtest.MockConfig) (initer, error)
	}{
		{"database", func(m *configtest.MockConfig) (initer, error) {
			return database.New()(m, &gas.NopLogger{})
		}},
		{"email", func(m *configtest.MockConfig) (initer, error) {
			return ses.New()(memory.NewStore(), m, &gas.NopLogger{})
		}},
		{"jwt", func(m *configtest.MockConfig) (initer, error) {
			return jwt.New()(m, &gas.NopLogger{})
		}},
	}
	for _, c := range cases {
		m, dests := bindRecorder()
		svc, err := c.build(m)
		if err != nil {
			t.Fatalf("%s: construct: %v", c.section, err)
		}
		if len(*dests) != 0 {
			t.Fatalf("%s: bound in the constructor, want only in Init", c.section)
		}
		if err := svc.Init(t.Context()); !errors.Is(err, errBoom) {
			t.Fatalf("%s: Init = %v, want the Bind error", c.section, err)
		}
		if len(*dests) != 1 || !hasSection((*dests)[0], c.section) {
			t.Fatalf("%s: Init bound %v, want one struct with a %s section", c.section, *dests, c.section)
		}
	}
}

// Docs: modules/config.mdx#what-it-is-for
// "Config is not a service. It has no `Init` or `Close`"
func TestConfigIsNotAService(t *testing.T) {
	t.Parallel()
	typ := reflect.TypeFor[*config.Config]()
	if typ.Implements(reflect.TypeFor[gas.Service]()) {
		t.Fatal("*config.Config implements gas.Service")
	}
	for _, name := range []string{"Init", "Close"} {
		if _, ok := typ.MethodByName(name); ok {
			t.Errorf("*config.Config has a %s method", name)
		}
	}
}

// Docs: modules/config.mdx#wiring
// "`config.New` takes providers and extensions and returns the config without reading anything. `Load` reads every source."
func TestNewReadsNothing(t *testing.T) {
	t.Parallel()
	j := &journal{}
	a := &countingProvider{name: "a", values: map[string]any{"a": "1"}}
	b := &countingProvider{name: "b", values: map[string]any{"b": "2"}}
	cfg := config.New(config.WithProvider(a), config.WithProvider(b),
		config.WithExtension(journalExtension{name: "x", j: j}))
	if a.loads.Load() != 0 || b.loads.Load() != 0 || len(j.list()) != 0 || len(cfg.Values()) != 0 {
		t.Fatalf("New read something: loads %d/%d, hooks %v, values %v", a.loads.Load(), b.loads.Load(), j.list(), cfg.Values())
	}
	if err := cfg.Load(); err != nil {
		t.Fatal(err)
	}
	if a.loads.Load() != 1 || b.loads.Load() != 1 || cfg.Get("a") != "1" || cfg.Get("b") != "2" {
		t.Fatalf("Load: loads %d/%d, a=%v b=%v; want every source read", a.loads.Load(), b.loads.Load(), cfg.Get("a"), cfg.Get("b"))
	}
}

// Docs: modules/config.mdx#wiring
// "When `New` is given no provider at all, it adds `providers.NewEnvProvider()` by itself; as soon as you pass one provider explicitly, add the environment provider yourself if you still want environment variables."
func TestEnvProviderOnlyWithoutProviders(t *testing.T) {
	t.Setenv("DOCSCLAIM_MODCFG_AUTO", "present")
	if got := load(t).Get("docsclaim_modcfg_auto"); got != "present" {
		t.Fatalf("no provider: docsclaim_modcfg_auto = %v, want the env value", got)
	}
	if got := load(t, values("m", map[string]any{})).Get("docsclaim_modcfg_auto"); got != nil {
		t.Fatalf("explicit provider: docsclaim_modcfg_auto = %v, want nil", got)
	}
	if got := load(t, config.WithExtension(journalExtension{name: "x", j: &journal{}})).Get("docsclaim_modcfg_auto"); got != "present" {
		t.Fatalf("an extension alone: docsclaim_modcfg_auto = %v, want the env value", got)
	}
}

// Docs: modules/config.mdx#providers-and-precedence
// "1. Every extension's `PreLoad` hook, in registration order. 2. Every provider's `Load`, in registration order." and "4. Every extension's `PostLoad` hook, in registration order."
func TestLoadOrder(t *testing.T) {
	t.Parallel()
	j := &journal{}
	cfg := config.New(
		config.WithExtension(journalExtension{name: "e1", j: j}),
		config.WithProvider(journalProvider{name: "p1", j: j}),
		config.WithExtension(journalExtension{name: "e2", j: j}),
		config.WithProvider(journalProvider{name: "p2", j: j}),
	)
	if err := cfg.Load(); err != nil {
		t.Fatal(err)
	}
	want := []string{"pre e1", "pre e2", "load p1", "load p2", "post e1", "post e2"}
	if got := j.list(); !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

// Docs: modules/config.mdx#providers-and-precedence
// "All of them are read before anything is merged, so if one fails, `Load` returns an error wrapping `config.ErrProviderLoadFailed` with the provider's name and merges none of them."
func TestFailingProviderMergesNothing(t *testing.T) {
	t.Parallel()
	first := &countingProvider{name: "first", values: map[string]any{"first": "yes"}}
	cfg := config.New(config.WithProvider(first), config.WithProvider(&failingProvider{name: "docsclaim-broken"}))
	err := cfg.Load()
	if !errors.Is(err, config.ErrProviderLoadFailed) || !errors.Is(err, errBoom) {
		t.Fatalf("Load = %v, want ErrProviderLoadFailed wrapping the provider's error", err)
	}
	if !strings.Contains(err.Error(), "docsclaim-broken") {
		t.Fatalf("Load = %q, want the provider's name", err)
	}
	if first.loads.Load() != 1 {
		t.Fatalf("first provider loaded %d times, want 1", first.loads.Load())
	}
	if got := cfg.Get("first"); got != nil {
		t.Fatalf("first = %v after a failed Load, want nothing merged", got)
	}
}

// Docs: modules/config.mdx#providers-and-precedence
// "3. The provider results are merged in registration order. A later provider overrides the keys it defines and leaves the rest alone; nested maps are merged key by key, not replaced."
func TestMergeOrder(t *testing.T) {
	t.Parallel()
	cfg := load(t,
		values("a", map[string]any{"k": "a", "onlya": "a", "db": map[string]any{"host": "h", "port": "1"}}),
		values("b", map[string]any{"k": "b", "db": map[string]any{"port": "2"}}),
	)
	for key, want := range map[string]string{"k": "b", "onlya": "a", "db.host": "h", "db.port": "2"} {
		if got := cfg.Get(key); got != want {
			t.Errorf("%s = %v, want %q", key, got, want)
		}
	}
}

// Docs: modules/config.mdx#providers-and-precedence
// "Keys are lowercased and trimmed at every level as they are merged, so `Database.DSN` from a JSON file and `DATABASE__DSN` from the environment land on the same key, `database.dsn`."
func TestKeysLowercasedAndTrimmed(t *testing.T) {
	t.Parallel()
	cfg := load(t, values("m", map[string]any{" Outer ": map[string]any{" Inner ": map[string]any{" LEAF ": "v"}}}))
	if got := cfg.Values(); !reflect.DeepEqual(got, map[string]any{"outer": map[string]any{"inner": map[string]any{"leaf": "v"}}}) {
		t.Fatalf("Values = %v, want every level lowercased and trimmed", got)
	}
}

// Docs: modules/config.mdx#providers-and-precedence
// "Keys are lowercased and trimmed at every level as they are merged, so `Database.DSN` from a JSON file and `DATABASE__DSN` from the environment land on the same key, `database.dsn`."
func TestJSONAndEnvLandOnSameKey(t *testing.T) {
	t.Setenv("DATABASE__DSN", "from-env")
	cfg := load(t, values("json", map[string]any{"Database": map[string]any{"DSN": "from-json", "Other": "kept"}}),
		config.WithProvider(envProvider()))
	if cfg.Get("database.dsn") != "from-env" || cfg.Get("database.other") != "kept" {
		t.Fatalf("database = %v, want the env value over the JSON one at database.dsn", cfg.Get("database"))
	}
}

// Docs: modules/config.mdx#providers-and-precedence
// "`Load` merges into the values the config already holds, so a value set with `Set` before `Load` survives unless a provider defines the same key."
func TestSetBeforeLoadSurvives(t *testing.T) {
	t.Parallel()
	cfg := config.New(values("m", map[string]any{"clash": "provider"}))
	cfg.Set("kept", "set")
	cfg.Set("clash", "set")
	if err := cfg.Load(); err != nil {
		t.Fatal(err)
	}
	if cfg.Get("kept") != "set" || cfg.Get("clash") != "provider" {
		t.Fatalf("kept=%v clash=%v, want set and provider", cfg.Get("kept"), cfg.Get("clash"))
	}
}

// Docs: modules/config.mdx#providers-and-precedence
// "`LoadProvider(p)` adds one more provider after the fact: it loads `p`, merges its values over the current ones, and registers it. A provider whose load fails is not registered."
func TestLoadProvider(t *testing.T) {
	t.Parallel()
	cfg := load(t, values("base", map[string]any{"k": "base", "keep": "base"}))
	late := &countingProvider{name: "late", values: map[string]any{"k": "late"}}
	if err := cfg.LoadProvider(late); err != nil {
		t.Fatal(err)
	}
	if late.loads.Load() != 1 || cfg.Get("k") != "late" || cfg.Get("keep") != "base" {
		t.Fatalf("after LoadProvider: loads=%d k=%v keep=%v", late.loads.Load(), cfg.Get("k"), cfg.Get("keep"))
	}
	broken := &failingProvider{name: "broken"}
	if err := cfg.LoadProvider(broken); !errors.Is(err, config.ErrProviderLoadFailed) {
		t.Fatalf("LoadProvider(broken) = %v, want ErrProviderLoadFailed", err)
	}
	// A later Load reads every registered provider: late is among them and
	// broken is not.
	if err := cfg.Load(); err != nil {
		t.Fatalf("Load after a failed LoadProvider = %v, want the failed provider not registered", err)
	}
	if late.loads.Load() != 2 || broken.loads.Load() != 1 {
		t.Fatalf("Load read late %d times and broken %d times, want 2 and 1", late.loads.Load(), broken.loads.Load())
	}
}

// Docs: modules/config.mdx#reading-and-setting-values
// "`Get("database.host")` walks the dot-separated path, case-insensitively, and returns `nil` when the key is missing; `Find` also reports whether it exists."
func TestGetAndFind(t *testing.T) {
	t.Parallel()
	cfg := load(t, values("m", map[string]any{"database": map[string]any{"host": "h", "empty": nil}}))
	if got := cfg.Get("DataBase.HOST"); got != "h" {
		t.Errorf("Get(DataBase.HOST) = %v, want h", got)
	}
	if got := cfg.Get("database.missing"); got != nil {
		t.Errorf("Get(missing) = %v, want nil", got)
	}
	if v, ok := cfg.Find("Database.Host"); !ok || v != "h" {
		t.Errorf("Find(Database.Host) = %v, %v", v, ok)
	}
	if v, ok := cfg.Find("database.empty"); !ok || v != nil {
		t.Errorf("Find(database.empty) = %v, %v; want nil, true", v, ok)
	}
	if _, ok := cfg.Find("database.missing"); ok {
		t.Error("Find(missing) reported the key exists")
	}
}

// Docs: modules/config.mdx#reading-and-setting-values
// "`Get`, `Find` and `Values` return deep copies, so changing a returned map does not change the config."
func TestReadsReturnDeepCopies(t *testing.T) {
	t.Parallel()
	cfg := load(t, values("m", map[string]any{"a": map[string]any{"b": map[string]any{"c": "orig"}}}))
	cfg.Get("a").(map[string]any)["b"].(map[string]any)["c"] = "via-get"
	found, _ := cfg.Find("a")
	found.(map[string]any)["b"].(map[string]any)["c"] = "via-find"
	cfg.Values()["a"].(map[string]any)["b"].(map[string]any)["c"] = "via-values"
	if got := cfg.Get("a.b.c"); got != "orig" {
		t.Fatalf("a.b.c = %v after changing returned maps, want orig", got)
	}
}

// Docs: modules/config.mdx#reading-and-setting-values
// "`Set(key, value)` writes a value, overriding what is there. `SetDefault(key, value)` writes only when the key is not set yet."
func TestSetAndSetDefault(t *testing.T) {
	t.Parallel()
	cfg := config.New(values("m", map[string]any{}))
	cfg.Set("a.b", "one")
	cfg.Set("a.b", "two")
	cfg.SetDefault("a.b", "default")
	cfg.SetDefault("a.c", "default")
	if cfg.Get("a.b") != "two" || cfg.Get("a.c") != "default" {
		t.Fatalf("a.b=%v a.c=%v, want two and default", cfg.Get("a.b"), cfg.Get("a.c"))
	}
}

// Docs: modules/config.mdx#reading-and-setting-values
// "`SetDefaults(values)` does the same for a whole `map[string]any` or struct; it returns `config.ErrNilValues` for `nil`. Map keys passed to `SetDefaults` are taken as one level each and not split on `.`, so pass nested maps."
func TestSetDefaults(t *testing.T) {
	t.Parallel()
	cfg := config.New(values("m", map[string]any{}))
	cfg.Set("db.host", "set")
	if err := cfg.SetDefaults(map[string]any{"db": map[string]any{"host": "default", "port": "5432"}, "x.y": "dotted"}); err != nil {
		t.Fatal(err)
	}
	if cfg.Get("db.host") != "set" || cfg.Get("db.port") != "5432" {
		t.Fatalf("map defaults: db.host=%v db.port=%v, want set and 5432", cfg.Get("db.host"), cfg.Get("db.port"))
	}
	if cfg.Get("x.y") != nil || cfg.Values()["x.y"] != "dotted" {
		t.Fatalf("dotted key: Get(x.y)=%v Values[x.y]=%v, want one flat key", cfg.Get("x.y"), cfg.Values()["x.y"])
	}
	type defaults struct {
		Cache struct{ TTL string }
	}
	var d defaults
	d.Cache.TTL = "5m"
	if err := cfg.SetDefaults(d); err != nil {
		t.Fatal(err)
	}
	if got := cfg.Get("cache.ttl"); got != "5m" {
		t.Fatalf("struct defaults: cache.ttl = %v, want 5m", got)
	}
	if err := cfg.SetDefaults(nil); !errors.Is(err, config.ErrNilValues) {
		t.Fatalf("SetDefaults(nil) = %v, want ErrNilValues", err)
	}
}

// Docs: modules/config.mdx#extensions
// "A failing hook stops `Load` with an error wrapping `config.ErrExtensionPreLoadHookFailed` or `config.ErrExtensionPostLoadHookFailed` and the extension's name."
func TestFailingHookStopsLoad(t *testing.T) {
	t.Parallel()
	j := &journal{}
	err := config.New(
		config.WithExtension(journalExtension{name: "docsclaim-pre", j: j, failPre: true}),
		config.WithExtension(journalExtension{name: "after", j: j}),
		config.WithProvider(journalProvider{name: "p", j: j}),
	).Load()
	if !errors.Is(err, config.ErrExtensionPreLoadHookFailed) || !strings.Contains(err.Error(), "docsclaim-pre") {
		t.Fatalf("PreLoad failure: Load = %v, want ErrExtensionPreLoadHookFailed naming docsclaim-pre", err)
	}
	if got := j.list(); !slices.Equal(got, []string{"pre docsclaim-pre"}) {
		t.Fatalf("PreLoad failure: ran %v, want Load stopped after the failing hook", got)
	}

	j = &journal{}
	err = config.New(
		config.WithExtension(journalExtension{name: "docsclaim-post", j: j, failPost: true}),
		config.WithExtension(journalExtension{name: "after", j: j}),
		config.WithProvider(journalProvider{name: "p", j: j}),
	).Load()
	if !errors.Is(err, config.ErrExtensionPostLoadHookFailed) || !strings.Contains(err.Error(), "docsclaim-post") {
		t.Fatalf("PostLoad failure: Load = %v, want ErrExtensionPostLoadHookFailed naming docsclaim-post", err)
	}
	if got := j.list(); slices.Contains(got, "post after") {
		t.Fatalf("PostLoad failure: ran %v, want Load stopped after the failing hook", got)
	}
}

type ctxKey struct{}

// ctxProvider records the context value its LoadContext saw.
type ctxProvider struct{ got *[]any }

func (p ctxProvider) Name() string                  { return "ctx" }
func (p ctxProvider) Load() (map[string]any, error) { return map[string]any{"via": "load"}, nil }
func (p ctxProvider) LoadContext(ctx context.Context) (map[string]any, error) {
	*p.got = append(*p.got, ctx.Value(ctxKey{}))
	return map[string]any{"via": "context"}, nil
}

// Docs: modules/config.mdx#writing-a-provider
// "If it also implements `providers.ContextProvider`, `LoadContext(ctx) (map[string]any, error)`, `LoadWithContext` and `LoadProviderContext` call that with their context instead"
func TestContextProvider(t *testing.T) {
	t.Parallel()
	var got []any
	ctx := context.WithValue(t.Context(), ctxKey{}, "caller")
	cfg := config.New(config.WithProvider(ctxProvider{got: &got}))
	if err := cfg.LoadWithContext(ctx); err != nil {
		t.Fatal(err)
	}
	if err := cfg.LoadProviderContext(ctx, ctxProvider{got: &got}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []any{"caller", "caller"}) || cfg.Get("via") != "context" {
		t.Fatalf("LoadContext saw %v, via=%v; want the caller's context twice and no Load", got, cfg.Get("via"))
	}
}

// Docs: modules/config.mdx#writing-a-provider
// "A provider implements `providers.Provider`: `Name() string` and `Load() (map[string]any, error)`, returning a nested map."
func TestCustomProvider(t *testing.T) {
	t.Parallel()
	cfg := load(t, values("mine", map[string]any{"outer": map[string]any{"inner": "v"}}))
	if got := cfg.Get("outer.inner"); got != "v" {
		t.Fatalf("outer.inner = %v, want v", got)
	}
}

// Docs: modules/config.mdx#errors
// "The provider errors reach `Load`'s caller wrapped in `config.ErrProviderLoadFailed`, and `errors.Is` finds both."
func TestProviderErrorsWrapped(t *testing.T) {
	t.Parallel()
	err := config.New(config.WithProvider(jsonProvider(""))).Load()
	if !errors.Is(err, config.ErrProviderLoadFailed) || !errors.Is(err, errJSONFilePathNotSet) {
		t.Fatalf("Load = %v, want ErrProviderLoadFailed and ErrJSONFilePathNotSet", err)
	}
}

// Docs: modules/config.mdx#errors
// "The environment provider never fails."
func TestEnvProviderNeverFails(t *testing.T) {
	t.Setenv("DOCSCLAIM_MODCFG__", "trailing separator")
	t.Setenv("DOCSCLAIM_MODCFG_X__Y", "nested")
	t.Setenv("DOCSCLAIM_MODCFG_X", "flat over nested")
	t.Setenv("DOCSCLAIM_MODCFG_EMPTY", "")
	for range 20 {
		if _, err := envProvider().Load(); err != nil {
			t.Fatalf("env provider failed: %v", err)
		}
	}
}
