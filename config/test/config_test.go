package config_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/config"
)

// A Config holds nothing until it is loaded.
func TestConfig_EmptyBeforeLoad(t *testing.T) {
	t.Parallel()

	c := config.New(config.WithProvider(newStatic("static", m{"a": 1})))
	assert.Equal(t, m{}, c.Values())
	assert.Nil(t, c.Get("a"))
}

// Without a provider, New adds an EnvProvider with the default separator
// and no prefix.
//
// Not parallel: it sets an environment variable.
func TestConfig_DefaultEnvProvider(t *testing.T) {
	t.Setenv("CONFIGTEST_DEFAULT__HOST", "db")

	c := config.New()
	require.NoError(t, c.Load())
	assert.Equal(t, "db", c.Get("configtest_default.host"))
}

// A key is split on dots into levels, each lowercased and trimmed, for
// Get, Find, Set and SetDefault alike.
func TestConfig_KeyLevels(t *testing.T) {
	t.Parallel()

	c := newConfig(t, m{"database": m{"host": "db"}})

	for _, key := range []string{"database.host", "Database.HOST", " database . host "} {
		assert.Equal(t, "db", c.Get(key), key)

		got, ok := c.Find(key)
		assert.True(t, ok, key)
		assert.Equal(t, "db", got, key)
	}

	require.NoError(t, c.Set("Cache . Host", "c"))
	require.NoError(t, c.SetDefault(" Cache.Port", 6379))
	assert.Equal(t, m{"host": "c", "port": 6379}, c.Get("cache"))
}

// A key that is empty, or has a level that is empty once trimmed, is no
// key: Get and Find find nothing, and Set and SetDefault return an error
// wrapping ErrInvalidKey and store nothing.
func TestConfig_InvalidKey(t *testing.T) {
	t.Parallel()

	for _, key := range []string{"", " ", "a..b", ".a", "a.", "a. .b"} {
		c := newConfig(t, m{"a": 1})

		err := c.Set(key, 2)
		require.ErrorIs(t, err, config.ErrInvalidKey, key)
		require.EqualError(t, err, fmt.Sprintf("invalid key %q: a level is empty", key), key)
		require.ErrorIs(t, c.SetDefault(key, 3), config.ErrInvalidKey, key)

		assert.Nil(t, c.Get(key), key)

		_, ok := c.Find(key)
		assert.False(t, ok, key)
		assert.Equal(t, m{"a": 1}, c.Values(), key)
	}
}

// Get returns nil for a key that holds nothing or nil; Find tells the two
// apart. Neither finds a key under a level that holds a value.
func TestConfig_GetAndFind(t *testing.T) {
	t.Parallel()

	c := newConfig(t, m{"a": m{"b": nil}, "s": "x"})

	assert.Nil(t, c.Get("a.b"))

	got, ok := c.Find("a.b")
	assert.True(t, ok)
	assert.Nil(t, got)

	for _, key := range []string{"a.c", "missing", "missing.b", "s.b"} {
		assert.Nil(t, c.Get(key), key)

		_, ok := c.Find(key)
		assert.False(t, ok, key)
	}
}

// Get, Find and Values return copies, so changing what they return does
// not change the configuration.
func TestConfig_ReadsReturnCopies(t *testing.T) {
	t.Parallel()

	c := newConfig(t, m{"db": m{"hosts": []any{"a"}}})

	c.Get("db").(m)["hosts"].([]any)[0] = "x"

	got, _ := c.Find("db.hosts")

	got.([]any)[0] = "y"

	values := c.Values()

	values["db"].(m)["new"] = true

	assert.Equal(t, m{"db": m{"hosts": []any{"a"}}}, c.Values())
}

// Set replaces what a key holds, and adds the maps missing on its path.
func TestConfig_Set(t *testing.T) {
	t.Parallel()

	c := newConfig(t, m{"db": m{"host": "a", "port": 1}})

	require.NoError(t, c.Set("db.host", "b"))
	require.NoError(t, c.Set("db", m{"host": "c"}))
	require.NoError(t, c.Set("cache.redis.host", "r"))

	assert.Equal(t, m{
		"db":    m{"host": "c"},
		"cache": m{"redis": m{"host": "r"}},
	}, c.Values())
}

// Set cannot reach a key under a level that holds a value that is not a
// map: it returns an error wrapping ErrKeyPathBlocked and changes nothing,
// adding no map on the way. SetDefault leaves such a key alone, as it
// leaves any key that holds a value, without an error.
func TestConfig_SetUnderValue(t *testing.T) {
	t.Parallel()

	c := newConfig(t, m{"db": "postgres://db", "app": m{"name": "x"}})

	err := c.Set("db.host", "h")
	require.ErrorIs(t, err, config.ErrKeyPathBlocked)
	require.EqualError(t, err, `set "db.host": a level of the key holds a value that is not a map`)
	require.ErrorIs(t, c.Set("app.name.first", "y"), config.ErrKeyPathBlocked)
	require.NoError(t, c.SetDefault("db.host", "h"))

	assert.Equal(t, m{"db": "postgres://db", "app": m{"name": "x"}}, c.Values())
}

// Set and SetDefault store a map value with its keys trimmed and
// lowercased at every depth, as a provider's are, so every key in it can
// be reached. A map of any key type becomes a map[string]any.
func TestConfig_SetNormalizesMapKeys(t *testing.T) {
	t.Parallel()

	c := newConfig(t, m{})
	require.NoError(t, c.Set("app", m{"Name": "x", "DB": m{"Host": "h"}, "list": []any{m{"K": 1}}}))
	require.NoError(t, c.SetDefault("ports", map[string]int{"HTTP": 80}))

	assert.Equal(t, "x", c.Get("app.name"))
	assert.Equal(t, "h", c.Get("app.db.host"))
	assert.Equal(t, []any{m{"k": 1}}, c.Get("app.list"))
	assert.Equal(t, m{"http": 80}, c.Get("ports"))
}

// Two keys of a map value that become the same key are an error wrapping
// ErrKeyConflict, and the configuration is unchanged.
func TestConfig_SetKeyConflict(t *testing.T) {
	t.Parallel()

	c := newConfig(t, m{"a": 1})

	err := c.Set("app", m{"Name": "x", "name": "y"})
	require.ErrorIs(t, err, config.ErrKeyConflict)
	require.EqualError(t, err, `set "app": conflicting keys: "Name" and "name"`)

	err = c.SetDefault("app", m{"db": m{"Host": "x", "host": "y"}})
	require.ErrorIs(t, err, config.ErrKeyConflict)
	require.EqualError(t, err, `set default "app": db: conflicting keys: "Host" and "host"`)

	assert.Equal(t, m{"a": 1}, c.Values())
}

// SetDefault stores a value only at a key that holds none. A key holding
// nil holds a value.
func TestConfig_SetDefault(t *testing.T) {
	t.Parallel()

	c := newConfig(t, m{"db": m{"host": "a", "user": nil}})

	require.NoError(t, c.SetDefault("db.host", "b"))
	require.NoError(t, c.SetDefault("db.user", "u"))
	require.NoError(t, c.SetDefault("db.port", 5432))
	require.NoError(t, c.SetDefault("cache.host", "c"))

	assert.Equal(t, m{
		"db":    m{"host": "a", "user": nil, "port": 5432},
		"cache": m{"host": "c"},
	}, c.Values())
}

// SetDefaults takes a map[string]any, a pointer to one, a struct or a
// pointer to one, and adds what the configuration does not hold. Two maps
// under one key are merged; anything the configuration holds stays.
func TestConfig_SetDefaults(t *testing.T) {
	t.Parallel()

	type DB struct {
		Host string
		Port int `json:"port"`
	}

	type Defaults struct {
		DB    DB `json:"database"`
		Debug bool
	}

	for name, defaults := range map[string]any{
		"map":            m{"Database": m{"Host": "b", "port": 5432}, "debug": false},
		"map pointer":    &m{"Database": m{"Host": "b", "port": 5432}, "debug": false},
		"struct":         Defaults{DB: DB{Host: "b", Port: 5432}},
		"struct pointer": &Defaults{DB: DB{Host: "b", Port: 5432}},
	} {
		c := newConfig(t, m{"database": m{"host": "a"}, "debug": "yes"})
		require.NoError(t, c.SetDefaults(defaults), name)
		assert.Equal(t, m{
			"database": m{"host": "a", "port": 5432},
			"debug":    "yes",
		}, c.Values(), name)
	}
}

// Defaults are a base that providers load over.
func TestConfig_DefaultsUnderProviders(t *testing.T) {
	t.Parallel()

	c := config.New(config.WithProvider(newStatic("static", m{"host": "provider"})))
	require.NoError(t, c.SetDefault("host", "default"))
	require.NoError(t, c.SetDefault("port", 1))
	require.NoError(t, c.Load())

	assert.Equal(t, m{"host": "provider", "port": 1}, c.Values())
}

func TestConfig_SetDefaultsErrors(t *testing.T) {
	t.Parallel()

	c := newConfig(t, m{"a": 1})

	require.ErrorIs(t, c.SetDefaults(nil), config.ErrNilValues)
	require.EqualError(t, c.SetDefaults(nil), "values cannot be nil")

	require.EqualError(
		t,
		c.SetDefaults(map[string]string{"a": "b"}),
		"unbind defaults: invalid source: want a struct or a non-nil pointer to one, "+
			"got map[string]string",
	)
	require.EqualError(
		t,
		c.SetDefaults(m{"B": 1, "b": 2}),
		`set defaults: conflicting keys: "B" and "b"`,
	)
	require.ErrorIs(t, c.SetDefaults(m{"B": 1, "b": 2}), config.ErrKeyConflict)

	assert.Equal(t, m{"a": 1}, c.Values())
}
