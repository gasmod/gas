package modulescache_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/cache"
	cachemem "github.com/gasmod/gas/cache/memory"
	cachevk "github.com/gasmod/gas/cache/valkey"
	"github.com/gasmod/gas/config/configtest"
)

// Docs: modules/cache.mdx#what-it-is-for
// "`gas/cache` implements `gas.CacheProvider`, the core interface for a
// key-value cache of `[]byte` values with a TTL: `Get`, `Set`, `Delete` and
// `Exists`. It has two backends"
var (
	_ gas.CacheProvider = (*cachemem.Service)(nil)
	_ gas.CacheProvider = (*cachevk.Service)(nil)
)

// Docs: modules/cache.mdx#what-it-is-for
// "`gas.CacheProvider`, the core interface for a key-value cache of `[]byte`
// values with a TTL: `Get`, `Set`, `Delete` and `Exists`."
func TestCacheProviderShape(t *testing.T) {
	t.Parallel()
	typ := reflect.TypeFor[gas.CacheProvider]()
	want := map[string]string{
		"Get":    "func(context.Context, string) ([]uint8, error)",
		"Set":    "func(context.Context, string, []uint8, time.Duration) error",
		"Delete": "func(context.Context, string) error",
		"Exists": "func(context.Context, string) (bool, error)",
	}
	for name, sig := range want {
		m, ok := typ.MethodByName(name)
		if !ok {
			t.Errorf("gas.CacheProvider lacks %s", name)
			continue
		}
		if got := m.Type.String(); got != sig {
			t.Errorf("%s = %s, want %s", name, got, sig)
		}
	}
	if typ.NumMethod() != len(want) {
		t.Errorf("gas.CacheProvider has %d methods, want %d", typ.NumMethod(), len(want))
	}
}

// Docs: modules/cache.mdx#what-it-is-for
// "The `cache` package itself holds the two sentinel errors both backends
// return"
func TestBothBackendsReturnTheSentinels(t *testing.T) {
	t.Parallel()
	for name, c := range backends(t) {
		if _, err := c.Get(t.Context(), "sentinel-missing"); err != cache.ErrKeyNotFound { //nolint:errorlint // identity is the claim
			t.Errorf("%s Get missing = %v, want cache.ErrKeyNotFound", name, err)
		}
	}
	unstarted := map[string]gas.CacheProvider{
		"memory": must(cachemem.New(cachemem.WithConfig(cachemem.DefaultConfig()))(nil, &gas.NopLogger{})),
		"valkey": must(cachevk.New(cachevk.WithConfig(cachevk.DefaultConfig()))(nil, &gas.NopLogger{})),
	}
	for name, c := range unstarted {
		if _, err := c.Get(t.Context(), "k"); err != cache.ErrClosed { //nolint:errorlint // identity is the claim
			t.Errorf("%s Get before Init = %v, want cache.ErrClosed", name, err)
		}
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

type consumer struct{ c gas.CacheProvider }

// constructors are the two backends' DI constructors, configured to connect
// nowhere useful so nothing here needs a server.
func constructors(t *testing.T) map[string]any {
	return map[string]any{
		"memory": cachemem.New(),
		"valkey": cachevk.New(cachevk.WithConfig(valkeyConfig(closedAddr(t)))),
	}
}

// Docs: modules/cache.mdx#wiring
// "Both `memory.New(opts...)` and `valkey.New(opts...)` return a constructor that
// the container calls with `gas.ConfigProvider` and `gas.Logger`, so both must
// be registered."
func TestConstructorsNeedConfigAndLogger(t *testing.T) {
	t.Parallel()
	for name, ctor := range constructors(t) {
		ctorType := reflect.TypeOf(ctor)
		if ctorType.NumIn() != 2 || ctorType.In(0) != reflect.TypeFor[gas.ConfigProvider]() ||
			ctorType.In(1) != reflect.TypeFor[gas.Logger]() {
			t.Errorf("%s constructor = %v, want func(gas.ConfigProvider, gas.Logger) ...", name, ctorType)
		}
		for missing, opt := range map[string]gas.Option{
			"gas.Logger":         gas.WithServiceInstance[gas.ConfigProvider](mustConfig(t, nil)),
			"gas.ConfigProvider": gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
		} {
			app := gas.NewApp(opt, gas.WithSingletonService[gas.CacheProvider](ctor))
			err := app.Start(t.Context())
			if err == nil {
				_ = app.Shutdown(context.Background())
				t.Errorf("%s without %s: Start succeeded", name, missing)
				continue
			}
			if !strings.Contains(err.Error(), missing) {
				t.Errorf("%s without %s: Start error = %v, want it to name %s", name, missing, err, missing)
			}
		}
	}
}

// Docs: modules/cache.mdx#wiring
// "The constructors never return an error and do no I/O."
func TestConstructorsNoErrorNoIO(t *testing.T) {
	t.Parallel()
	addr, accepted := blackhole(t)
	cfg := mustConfig(t, map[string]any{"cache": map[string]any{"addr": addr}})
	if _, err := cachevk.New()(cfg, &gas.NopLogger{}); err != nil {
		t.Errorf("valkey constructor error = %v", err)
	}
	if _, err := cachevk.New(cachevk.WithConfig(valkeyConfig(addr)))(cfg, &gas.NopLogger{}); err != nil {
		t.Errorf("valkey constructor with WithConfig error = %v", err)
	}
	if _, err := cachemem.New()(cfg, &gas.NopLogger{}); err != nil {
		t.Errorf("memory constructor error = %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if n := accepted.Load(); n != 0 {
		t.Errorf("valkey constructor opened %d connections, want 0", n)
	}
}

// Docs: modules/cache.mdx#wiring
// "Register them under `gas.CacheProvider`: the container matches by exact type, so a
// backend registered as `*memory.Service` or `*valkey.Service` does not satisfy
// a `gas.CacheProvider` dependency."
func TestRegisterUnderCacheProvider(t *testing.T) {
	t.Parallel()
	base := func() []gas.Option {
		return []gas.Option{
			gas.WithServiceInstance[gas.ConfigProvider](mustConfig(t, nil)),
			gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
			gas.WithSingletonService[*consumer](func(c gas.CacheProvider) *consumer { return &consumer{c: c} }),
		}
	}
	for name, opt := range map[string]gas.Option{
		"*memory.Service": gas.WithSingletonService[*cachemem.Service](cachemem.New()),
		"*valkey.Service": gas.WithSingletonService[*cachevk.Service](cachevk.New(cachevk.WithConfig(valkeyConfig(valkeyServer(t))))),
	} {
		app := gas.NewApp(append(base(), opt)...)
		err := app.Start(t.Context())
		if err == nil {
			_ = app.Shutdown(context.Background())
			t.Errorf("registered as %s: Start succeeded, want gas.CacheProvider unresolved", name)
			continue
		}
		if !errors.Is(err, gas.ErrNotRegistered) {
			t.Errorf("registered as %s: Start error = %v, want gas.ErrNotRegistered", name, err)
		}
	}

	app := gas.NewApp(append(base(), gas.WithSingletonService[gas.CacheProvider](cachemem.New()))...)
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("registered as gas.CacheProvider: Start = %v", err)
	}
	_ = app.Shutdown(context.Background())
}

// boundTo reports whether cfg's Bind was called with a destination of type T.
func boundTo[T any](cfg *configtest.MockConfig) bool {
	for _, c := range cfg.Calls {
		if c.Method == "Bind" && len(c.Args) > 0 {
			if _, ok := c.Args[0].(T); ok {
				return true
			}
		}
	}
	return false
}

// Docs: modules/cache.mdx#wiring
// "Each backend takes one option, `WithConfig(cfg)`, which replaces the whole
// configuration and skips binding from the `gas.ConfigProvider`."
func TestWithConfigSkipsBinding(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	cfg := mustConfig(t, map[string]any{"cache": map[string]any{"maxentries": 1}})
	mem, _ := cachemem.New(cachemem.WithConfig(cachemem.DefaultConfig()))(cfg, &gas.NopLogger{})
	if err := mem.Init(ctx); err != nil {
		t.Fatalf("memory Init: %v", err)
	}
	defer func() { _ = mem.Close(context.Background()) }()
	for _, k := range []string{"a", "b", "c"} {
		if err := mem.Set(ctx, k, []byte("v"), 0); err != nil {
			t.Errorf("memory Set %s: %v; the bound maxentries 1 should not apply", k, err)
		}
	}
	if boundTo[*cachemem.Config](cfg) {
		t.Error("memory with WithConfig bound its config from the provider")
	}

	// The provider points at a listener; WithConfig points elsewhere.
	addr, accepted := blackhole(t)
	vcfg := mustConfig(t, map[string]any{"cache": map[string]any{"addr": addr}})
	vk, _ := cachevk.New(cachevk.WithConfig(valkeyConfig(closedAddr(t))))(vcfg, &gas.NopLogger{})
	if err := vk.Init(ctx); err == nil {
		t.Fatal("valkey Init against a closed port succeeded")
	}
	if accepted.Load() != 0 {
		t.Error("valkey with WithConfig connected to the provider's addr")
	}
	if boundTo[*cachevk.Config](vcfg) {
		t.Error("valkey with WithConfig bound its config from the provider")
	}
}

// Docs: modules/cache.mdx#wiring
// "Two instances behind a load balancer have two separate caches, so an
// invalidation on one is invisible to the other."
func TestMemoryInstancesAreSeparate(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	a := newMemory(t, cachemem.DefaultConfig())
	b := newMemory(t, cachemem.DefaultConfig())
	for _, c := range []*cachemem.Service{a, b} {
		if err := c.Set(ctx, "k", []byte("v"), 0); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}
	if err := a.Delete(ctx, "k"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got, err := b.Get(ctx, "k"); err != nil || string(got) != "v" {
		t.Fatalf("other instance Get = %q, %v; want v, nil", got, err)
	}
}

// Docs: modules/cache.mdx#cache-aside
// "A miss is `cache.ErrKeyNotFound`, a sentinel error rather than a nil value, so
// an empty value stays distinguishable from an absent one."
func TestMissVersusEmptyValue(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	for name, c := range backends(t) {
		key := key(t, "empty-"+name)
		if got, err := c.Get(ctx, key); !errors.Is(err, cache.ErrKeyNotFound) || got != nil {
			t.Errorf("%s Get missing = %q, %v; want nil, ErrKeyNotFound", name, got, err)
		}
		if err := c.Set(ctx, key, []byte{}, time.Minute); err != nil {
			t.Fatalf("%s Set empty: %v", name, err)
		}
		if got, err := c.Get(ctx, key); err != nil || len(got) != 0 {
			t.Errorf("%s Get empty = %q, %v; want empty, nil", name, got, err)
		}
	}
}

// Docs: modules/cache.mdx#invalidating
// "`Delete` of
// a missing key succeeds on both backends."
func TestDeleteMissingKeySucceeds(t *testing.T) {
	t.Parallel()
	for name, c := range backends(t) {
		if err := c.Delete(t.Context(), "never-set-"+t.Name()); err != nil {
			t.Errorf("%s Delete missing = %v, want nil", name, err)
		}
	}
}

// Docs: modules/cache.mdx#ttls
// "| positive | expires after the TTL | expires after the TTL, set in milliseconds on the server |
// | zero | uses `cache.defaultttl`; when that is also zero, never expires | never expires |
// | negative | never expires | never expires |"
func TestTTLTable(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	addr := valkeyServer(t)
	vk := newValkey(t, addr)

	memDefault := cachemem.DefaultConfig()
	memDefault.Cache.DefaultTTL = 100 * time.Millisecond
	backendsWithDefault := map[string]gas.CacheProvider{
		"memory":            newMemory(t, cachemem.DefaultConfig()),
		"memory-defaultttl": newMemory(t, memDefault),
		"valkey":            vk,
	}
	cases := []struct {
		backend string
		ttl     time.Duration
		expires bool
	}{
		{"memory", 100 * time.Millisecond, true},
		{"memory", 0, false},
		{"memory", -time.Second, false},
		{"memory-defaultttl", 0, true},
		{"memory-defaultttl", -time.Second, false},
		{"valkey", 100 * time.Millisecond, true},
		{"valkey", 0, false},
		{"valkey", -time.Second, false},
	}
	for i, c := range cases {
		key := strings.ReplaceAll(t.Name(), "/", "-") + "-" + string(rune('a'+i))
		if err := backendsWithDefault[c.backend].Set(ctx, key, []byte("v"), c.ttl); err != nil {
			t.Fatalf("%s Set ttl %v: %v", c.backend, c.ttl, err)
		}
	}
	time.Sleep(400 * time.Millisecond)
	for i, c := range cases {
		key := strings.ReplaceAll(t.Name(), "/", "-") + "-" + string(rune('a'+i))
		_, err := backendsWithDefault[c.backend].Get(ctx, key)
		if c.expires && !errors.Is(err, cache.ErrKeyNotFound) {
			t.Errorf("%s ttl %v: Get after expiry = %v, want ErrKeyNotFound", c.backend, c.ttl, err)
		}
		if !c.expires && err != nil {
			t.Errorf("%s ttl %v: Get = %v, want the value (never expires)", c.backend, c.ttl, err)
		}
	}

	// On the server, a positive TTL is a millisecond expiry and the others
	// leave the key persistent.
	client := vk.Client()
	if err := vk.Set(ctx, "ttl-ms", []byte("v"), 1500*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	pttl, err := client.Do(ctx, client.B().Pttl().Key("ttl-ms").Build()).AsInt64()
	if err != nil || pttl <= 1000 || pttl > 1500 {
		t.Errorf("PTTL after Set 1500ms = %d, %v; want 1000 < pttl <= 1500", pttl, err)
	}
	for _, ttl := range []time.Duration{0, -time.Second} {
		if err := vk.Set(ctx, "ttl-none", []byte("v"), ttl); err != nil {
			t.Fatal(err)
		}
		if pttl, err := client.Do(ctx, client.B().Pttl().Key("ttl-none").Build()).AsInt64(); err != nil || pttl != -1 {
			t.Errorf("PTTL after Set ttl %v = %d, %v; want -1 (no expiry)", ttl, pttl, err)
		}
	}
}

// Docs: modules/cache.mdx#configuration
// "Unless `WithConfig` is given, `Init` binds the settings from the
// `gas.ConfigProvider` under the `cache` key, over the backend's
// `DefaultConfig()`, so a key you do not set keeps its default. Both backends
// read the same `cache` key, each its own fields. Keys are case-insensitive"
func TestConfigBinding(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	vkAddr := closedAddr(t)
	cfg := mustConfig(t, map[string]any{"Cache": map[string]any{
		"MaxEntries":  1,
		"DEFAULTTTL":  "50ms",
		"addr":        vkAddr,
		"DialTimeout": "1s",
	}})

	mem, _ := cachemem.New()(cfg, &gas.NopLogger{})
	if err := mem.Init(ctx); err != nil {
		t.Fatalf("memory Init: %v", err)
	}
	defer func() { _ = mem.Close(context.Background()) }()
	if err := mem.Set(ctx, "a", []byte("v"), 0); err != nil {
		t.Fatal(err)
	}
	if err := mem.Set(ctx, "b", []byte("v"), 0); err == nil {
		t.Error("memory: second key accepted, want bound maxentries 1 to reject it")
	}
	time.Sleep(150 * time.Millisecond)
	if _, err := mem.Get(ctx, "a"); !errors.Is(err, cache.ErrKeyNotFound) {
		t.Errorf("memory: Get after bound defaultttl = %v, want ErrKeyNotFound", err)
	}

	vk, _ := cachevk.New()(cfg, &gas.NopLogger{})
	err := vk.Init(ctx)
	if err == nil || !strings.Contains(err.Error(), vkAddr) {
		t.Errorf("valkey Init = %v, want a connection error naming the bound addr %s", err, vkAddr)
	}
}

// Docs: modules/cache.mdx#configuration
// "| `cache.maxentries` | int | `0` | Maximum entries; 0 is unlimited |
// | `cache.cleanupinterval` | duration | `1m` | ... | `cache.defaultttl` | duration | `0` |"
func TestMemoryDefaults(t *testing.T) {
	t.Parallel()
	got := cachemem.DefaultConfig().Cache
	if got.MaxEntries != 0 || got.CleanupInterval != time.Minute || got.DefaultTTL != 0 {
		t.Errorf("memory defaults = %+v, want MaxEntries 0, CleanupInterval 1m, DefaultTTL 0", got)
	}
}

// Docs: modules/cache.mdx#configuration
// "| `cache.addr` | string | `localhost:6379` | ... | `cache.password` | string | | ... |
// `cache.db` | int | `0` | ... | `cache.dialtimeout` | duration | `5s` | ... |
// `cache.writetimeout` | duration | `3s` | ... | `cache.connretries` | int | `0` | ... |
// `cache.connretryinterval` | duration | `2s` |"
func TestValkeyDefaults(t *testing.T) {
	t.Parallel()
	got := cachevk.DefaultConfig().Cache
	want := cachevk.Settings{
		Addr:              "localhost:6379",
		DialTimeout:       5 * time.Second,
		WriteTimeout:      3 * time.Second,
		ConnRetryInterval: 2 * time.Second,
	}
	if got != want {
		t.Errorf("valkey defaults = %+v, want %+v", got, want)
	}
}

// Docs: modules/cache.mdx#configuration
// "No memory setting is validated."
func TestMemoryNotValidated(t *testing.T) {
	t.Parallel()
	cfg := mustConfig(t, map[string]any{"cache": map[string]any{
		"maxentries": -5, "cleanupinterval": "-1s", "defaultttl": "-1s",
	}})
	mem, _ := cachemem.New()(cfg, &gas.NopLogger{})
	if err := mem.Init(t.Context()); err != nil {
		t.Fatalf("memory Init with negative settings = %v, want nil", err)
	}
	_ = mem.Close(context.Background())
}

// Docs: modules/cache.mdx#configuration
// "| `cache.addr` | string | `localhost:6379` | Server address, `host:port`. Required | ... | `cache.db` | int | `0` | Database number; must not be negative | ... The rules on `addr` and `db` are `validate` tags, checked when the config is
// bound; a configuration given with `WithConfig` is used as given."
func TestValkeyValidation(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	for name, values := range map[string]map[string]any{
		"empty addr":  {"addr": ""},
		"negative db": {"addr": closedAddr(t), "db": -1},
	} {
		vk, _ := cachevk.New()(mustConfig(t, map[string]any{"cache": values}), &gas.NopLogger{})
		err := vk.Init(ctx)
		if err == nil || !strings.Contains(err.Error(), "config binding") {
			t.Errorf("%s bound: Init = %v, want a config binding error", name, err)
		}
	}
	for name, mutate := range map[string]func(*cachevk.Config){
		"empty addr":  func(c *cachevk.Config) { c.Cache.Addr = "" },
		"negative db": func(c *cachevk.Config) { c.Cache.Addr = closedAddr(t); c.Cache.DB = -1 },
	} {
		cfg := cachevk.DefaultConfig()
		mutate(cfg)
		cfg.Cache.DialTimeout = time.Second
		vk := buildValkey(t, cfg, nil)
		if err := vk.Init(ctx); err != nil && strings.Contains(err.Error(), "config binding") {
			t.Errorf("%s via WithConfig: Init = %v, want it used as given, not validated", name, err)
		}
	}
}

// Docs: modules/cache.mdx#errors
// "| `cache.ErrKeyNotFound` | `Get` of a key that is missing or has expired |
// | `cache.ErrClosed` | Any operation before `Init` or after `Close`. ... Both are returned unwrapped"
func TestErrorTable(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	addr := valkeyServer(t)
	type svc interface {
		gas.CacheProvider
		gas.Service
	}
	build := map[string]func() svc{
		"memory": func() svc {
			return must(cachemem.New(cachemem.WithConfig(cachemem.DefaultConfig()))(nil, &gas.NopLogger{}))
		},
		"valkey": func() svc {
			return must(cachevk.New(cachevk.WithConfig(valkeyConfig(addr)))(nil, &gas.NopLogger{}))
		},
	}
	ops := map[string]func(c gas.CacheProvider) error{
		"Get":    func(c gas.CacheProvider) error { _, err := c.Get(ctx, "k"); return err },
		"Set":    func(c gas.CacheProvider) error { return c.Set(ctx, "k", []byte("v"), 0) },
		"Delete": func(c gas.CacheProvider) error { return c.Delete(ctx, "k") },
		"Exists": func(c gas.CacheProvider) error { _, err := c.Exists(ctx, "k"); return err },
	}
	for name, mk := range build {
		c := mk()
		for op, f := range ops {
			if err := f(c); err != cache.ErrClosed { //nolint:errorlint // unwrapped is the claim
				t.Errorf("%s %s before Init = %v, want cache.ErrClosed unwrapped", name, op, err)
			}
		}
		if err := c.Init(ctx); err != nil {
			t.Fatalf("%s Init: %v", name, err)
		}
		key := "expiring-" + t.Name()
		if err := c.Set(ctx, key, []byte("v"), 50*time.Millisecond); err != nil {
			t.Fatal(err)
		}
		time.Sleep(150 * time.Millisecond)
		if _, err := c.Get(ctx, key); err != cache.ErrKeyNotFound { //nolint:errorlint // unwrapped is the claim
			t.Errorf("%s Get expired = %v, want cache.ErrKeyNotFound unwrapped", name, err)
		}
		if _, err := c.Get(ctx, "missing-"+t.Name()); err != cache.ErrKeyNotFound { //nolint:errorlint // unwrapped is the claim
			t.Errorf("%s Get missing = %v, want cache.ErrKeyNotFound unwrapped", name, err)
		}
		if err := c.Close(ctx); err != nil {
			t.Fatalf("%s Close: %v", name, err)
		}
		for op, f := range ops {
			if err := f(c); err != cache.ErrClosed { //nolint:errorlint // unwrapped is the claim
				t.Errorf("%s %s after Close = %v, want cache.ErrClosed unwrapped", name, op, err)
			}
		}
	}
}

// Docs: modules/cache.mdx#errors
// "Other errors
// start with the backend's service name, `gas/cache/memory` or
// `gas/cache/valkey`."
func TestOtherErrorsNameTheBackend(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	cfg := cachemem.DefaultConfig()
	cfg.Cache.MaxEntries = 1
	mem := newMemory(t, cfg)
	_ = mem.Set(ctx, "a", []byte("v"), 0)
	memErrs := []error{mem.Set(ctx, "b", []byte("v"), 0)}
	bad, _ := cachemem.New()(mustConfig(t, map[string]any{"cache": map[string]any{"maxentries": "lots"}}), &gas.NopLogger{})
	memErrs = append(memErrs, bad.Init(ctx))
	for _, err := range memErrs {
		if err == nil || !strings.HasPrefix(err.Error(), "gas/cache/memory") {
			t.Errorf("memory error = %v, want it to start with gas/cache/memory", err)
		}
	}

	vkBound, _ := cachevk.New()(mustConfig(t, map[string]any{"cache": map[string]any{"db": -1}}), &gas.NopLogger{})
	vcfg := valkeyConfig(closedAddr(t))
	vcfg.Cache.ConnRetries = 1
	vcfg.Cache.ConnRetryInterval = 10 * time.Millisecond
	for _, err := range []error{vkBound.Init(ctx), buildValkey(t, valkeyConfig(closedAddr(t)), nil).Init(ctx), buildValkey(t, vcfg, nil).Init(ctx)} {
		if err == nil || !strings.HasPrefix(err.Error(), "gas/cache/valkey") {
			t.Errorf("valkey error = %v, want it to start with gas/cache/valkey", err)
		}
	}
}

// Docs: modules/cache.mdx#testing
// "To test against real cache behavior with no server, use the in-memory
// backend: build it with `memory.New(...)` and call `Init` yourself, or
// register it in a test app."
func TestMemoryInTests(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	direct, err := cachemem.New()(mustConfig(t, nil), &gas.NopLogger{})
	if err != nil {
		t.Fatal(err)
	}
	if err := direct.Init(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = direct.Close(context.Background()) }()
	if err := direct.Set(ctx, "k", []byte("v"), 0); err != nil {
		t.Fatal(err)
	}
	if got, err := direct.Get(ctx, "k"); err != nil || string(got) != "v" {
		t.Errorf("direct Get = %q, %v", got, err)
	}

	app := gas.NewApp(
		gas.WithServiceInstance[gas.ConfigProvider](mustConfig(t, nil)),
		gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
		gas.WithSingletonService[gas.CacheProvider](cachemem.New()),
	)
	if err := app.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Shutdown(context.Background()) }()
	c, err := app.ServiceContainer().Resolve[gas.CacheProvider]()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Set(ctx, "k", []byte("v"), 0); err != nil {
		t.Errorf("app cache Set: %v", err)
	}
}
