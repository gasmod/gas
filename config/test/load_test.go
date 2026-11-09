package config_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/config"
)

var errBoom = errors.New("boom")

// Providers are merged in the order they were added, a later one winning.
// Two maps under one key are merged key by key; anywhere else the later
// value replaces the earlier one.
func TestLoad_MergeOrder(t *testing.T) {
	t.Parallel()

	c := config.New(
		config.WithProvider(newStatic("first", m{
			"db":    m{"host": "a", "port": 1},
			"cache": m{"host": "c"},
			"mode":  "first",
			"tags":  []any{"x", "y"},
			"name":  "first",
		})),
		config.WithProvider(newStatic("second", m{
			"db":    m{"host": "b"},
			"cache": "off",
			"mode":  m{"kind": "map"},
			"tags":  []any{"z"},
			"name":  nil,
		})),
	)
	require.NoError(t, c.Load())

	assert.Equal(t, m{
		"db":    m{"host": "b", "port": 1},
		"cache": "off",
		"mode":  m{"kind": "map"},
		"tags":  []any{"z"},
		"name":  nil,
	}, c.Values())
}

// Each provider's keys are trimmed and lowercased, at every depth, before
// it is merged, so a later provider wins whatever the case of its keys.
// Two keys of one provider that become the same are an error.
func TestLoad_KeysNormalized(t *testing.T) {
	t.Parallel()

	c := config.New(
		config.WithProvider(newStatic("first", m{"DB": m{" Host ": "a"}})),
		config.WithProvider(newStatic("second", m{"db": m{"HOST": "b"}})),
	)
	require.NoError(t, c.Load())
	assert.Equal(t, m{"db": m{"host": "b"}}, c.Values())

	c = config.New(config.WithProvider(newStatic("static", m{"Host": 1, "host": 2})))

	err := c.Load()
	require.ErrorIs(t, err, config.ErrProviderLoadFailed)
	require.ErrorIs(t, err, config.ErrKeyConflict)
	require.EqualError(
		t,
		err,
		`failed to load from provider static: conflicting keys: "Host" and "host"`,
	)
}

// The configuration keeps a copy of what a provider returns, so the
// provider may keep and change its map.
func TestLoad_KeepsCopy(t *testing.T) {
	t.Parallel()

	values := m{"db": m{"host": "a"}}
	c := newConfig(t, values)

	values["db"].(m)["host"] = "changed"

	assert.Equal(t, "a", c.Get("db.host"))
}

// Every load loads every provider again and merges over what the
// configuration holds, so a key that a provider no longer returns stays,
// and a value set with Set stays unless a provider sets that key.
func TestLoad_Reload(t *testing.T) {
	t.Parallel()

	p := newStatic("static", m{"a": 1, "b": 2})
	c := config.New(config.WithProvider(p))
	require.NoError(t, c.Load())

	require.NoError(t, c.Set("a", "set"))
	require.NoError(t, c.Set("c", "set"))
	p.set(m{"a": 10}, nil)
	require.NoError(t, c.Load())

	assert.Equal(t, m{"a": 10, "b": 2, "c": "set"}, c.Values())
	assert.Equal(t, 2, p.loadCount())
}

// All providers load before anything is merged, so a failing provider
// leaves the configuration as it was, the values of the providers before
// it included. No provider after it loads.
func TestLoad_FailureMergesNothing(t *testing.T) {
	t.Parallel()

	first := newStatic("first", m{"a": 1})
	failing := newStatic("failing", m{"b": 1})
	last := newStatic("last", m{"c": 1})
	c := config.New(
		config.WithProvider(first),
		config.WithProvider(failing),
		config.WithProvider(last),
	)
	require.NoError(t, c.Load())

	first.set(m{"a": 2}, nil)
	failing.set(nil, errBoom)

	err := c.Load()
	require.ErrorIs(t, err, config.ErrProviderLoadFailed)
	require.ErrorIs(t, err, errBoom)
	require.EqualError(t, err, "failed to load from provider failing: boom")

	assert.Equal(t, m{"a": 1, "b": 1, "c": 1}, c.Values())
	assert.Equal(t, 1, last.loadCount())
}

// A ContextProvider gets the load's context through LoadContext; Load is
// LoadWithContext with context.Background().
func TestLoad_Context(t *testing.T) {
	t.Parallel()

	type key struct{}

	p := &withCtx{static: newStatic("ctx", m{"a": 1})}
	c := config.New(config.WithProvider(p))

	require.NoError(t, c.LoadWithContext(context.WithValue(context.Background(), key{}, "v")))
	assert.Equal(t, "v", p.ctx.Value(key{}))

	require.NoError(t, c.Load())
	assert.Equal(t, context.Background(), p.ctx)
}

// Extensions run in the order they were added: every PreLoad before the
// providers load, every PostLoad after their values are merged.
func TestLoad_ExtensionOrder(t *testing.T) {
	t.Parallel()

	var log []string

	seen := map[string]any{}
	fn := func(hook string, cfg *config.Config) { seen[hook] = cfg.Get("a") }

	c := config.New(
		config.WithProvider(newStatic("static", m{"a": 1})),
		config.WithExtension(&recorder{name: "one", log: &log, fn: fn}),
		config.WithExtension(&recorder{name: "two", log: &log}),
	)
	require.NoError(t, c.Load())

	assert.Equal(t, []string{"one.pre", "two.pre", "one.post", "two.post"}, log)
	assert.Equal(t, map[string]any{"pre": nil, "post": 1}, seen)
}

// A failing PreLoad stops the load before any provider loads. A failing
// PostLoad stops it after the values are merged, and they stay.
func TestLoad_ExtensionErrors(t *testing.T) {
	t.Parallel()

	var log []string

	p := newStatic("static", m{"a": 1})
	c := config.New(
		config.WithProvider(p),
		config.WithExtension(&recorder{name: "one", log: &log, preErr: errBoom}),
		config.WithExtension(&recorder{name: "two", log: &log}),
	)

	err := c.Load()
	require.ErrorIs(t, err, config.ErrExtensionPreLoadHookFailed)
	require.ErrorIs(t, err, errBoom)
	require.EqualError(t, err, "failed to execute extension pre-load hook one: boom")
	assert.Equal(t, []string{"one.pre"}, log)
	assert.Zero(t, p.loadCount())

	log = nil
	c = config.New(
		config.WithProvider(p),
		config.WithExtension(&recorder{name: "one", log: &log, postErr: errBoom}),
		config.WithExtension(&recorder{name: "two", log: &log}),
	)

	err = c.Load()
	require.ErrorIs(t, err, config.ErrExtensionPostLoadHookFailed)
	require.EqualError(t, err, "failed to execute extension post-load hook one: boom")
	assert.Equal(t, []string{"one.pre", "two.pre", "one.post"}, log)
	assert.Equal(t, m{"a": 1}, c.Values())
}

// LoadProvider loads one provider, merges it over the configuration, and
// adds it to the providers later loads load. Extensions do not run.
func TestLoad_LoadProvider(t *testing.T) {
	t.Parallel()

	var log []string

	first := newStatic("first", m{"a": 1, "b": 1})
	c := config.New(
		config.WithProvider(first),
		config.WithExtension(&recorder{name: "ext", log: &log}),
	)
	require.NoError(t, c.Load())

	log = nil

	added := newStatic("added", m{"b": 2})
	require.NoError(t, c.LoadProvider(added))
	assert.Equal(t, m{"a": 1, "b": 2}, c.Values())
	assert.Empty(t, log)

	first.set(m{"b": 3}, nil)
	require.NoError(t, c.Load())
	assert.Equal(t, 2, c.Get("b"))
	assert.Equal(t, 2, added.loadCount())
}

// A provider that fails to load is not added, so a later load does not
// load it, and the configuration is unchanged.
func TestLoad_LoadProviderFailure(t *testing.T) {
	t.Parallel()

	c := newConfig(t, m{"a": 1})

	failing := newStatic("failing", nil)

	failing.err = errBoom

	err := c.LoadProvider(failing)
	require.ErrorIs(t, err, config.ErrProviderLoadFailed)
	require.EqualError(t, err, "failed to load from provider failing: boom")
	assert.Equal(t, m{"a": 1}, c.Values())

	require.NoError(t, c.Load())
	assert.Equal(t, 1, failing.loadCount())
}

// LoadProviderContext passes its context to a ContextProvider.
func TestLoad_LoadProviderContext(t *testing.T) {
	t.Parallel()

	type key struct{}

	c := newConfig(t, m{})
	p := &withCtx{static: newStatic("ctx", m{"a": 1})}

	require.NoError(
		t,
		c.LoadProviderContext(context.WithValue(context.Background(), key{}, "v"), p),
	)
	assert.Equal(t, "v", p.ctx.Value(key{}))
	assert.Equal(t, 1, c.Get("a"))
}

// A Config may be loaded, read and written from several goroutines at
// once; the race detector checks this test when it runs under -race.
func TestLoad_Concurrent(t *testing.T) {
	t.Parallel()

	c := config.New(
		config.WithProvider(newStatic("static", m{"db": m{"host": "a"}})),
		config.WithExtension(config.NewEnvExtension()),
	)

	var wg sync.WaitGroup

	for range 4 {
		wg.Go(func() { assert.NoError(t, c.Load()) })
		wg.Go(func() { _ = c.Get("db.host") })
		wg.Go(func() { assert.NoError(t, c.Set("db.port", 1)) })
		wg.Go(func() {
			var v struct{ DB struct{ Host string } }

			_ = c.Bind(&v)
		})
	}

	wg.Wait()

	assert.Equal(t, "a", c.Get("db.host"))
}

// gated is a provider whose Load reports that it started, then waits for
// its gate to open.
type gated struct {
	started chan struct{}
	gate    chan struct{}
	values  map[string]any
}

func (g *gated) Name() string { return "gated" }

func (g *gated) Load() (map[string]any, error) {
	close(g.started)
	<-g.gate

	return g.values, nil
}

// A provider that LoadProvider adds while a load is loading its providers
// comes after them, so its values win once both are done, as they would
// in either order of the two calls.
func TestLoad_LoadProviderDuringLoad(t *testing.T) {
	t.Parallel()

	a := &gated{started: make(chan struct{}), gate: make(chan struct{}), values: m{"k": "from-a"}}
	c := config.New(config.WithProvider(a))

	loaded := make(chan error)

	go func() { loaded <- c.Load() }()

	<-a.started

	added := make(chan error)

	go func() { added <- c.LoadProvider(newStatic("b", m{"k": "from-b"})) }()

	// LoadProvider waits for the load; give it the time it would need to
	// finish if it did not.
	select {
	case err := <-added:
		require.NoError(t, err)
		close(a.gate)
	case <-time.After(100 * time.Millisecond):
		close(a.gate)
		require.NoError(t, <-added)
	}

	require.NoError(t, <-loaded)
	assert.Equal(t, "from-b", c.Get("k"))
}
