package configtest_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/config/configtest"
)

var _ config.Provider = (*configtest.StaticProvider)(nil)

// A Config loads the provider's values as it loads any provider's, with
// keys trimmed and lowercased, and names it `static` in errors.
func TestStaticProvider_Load(t *testing.T) {
	t.Parallel()

	p := configtest.NewStaticProvider(m{"Database": m{" Host ": "db"}})
	assert.Equal(t, "static", p.Name())

	c := config.New(config.WithProvider(p))
	require.NoError(t, c.Load())

	assert.Equal(t, "db", c.Get("database.host"))
}

// Every load returns the values, so a later load restores a key that was
// set over.
func TestStaticProvider_LoadsAgain(t *testing.T) {
	t.Parallel()

	c := config.New(config.WithProvider(configtest.NewStaticProvider(m{"a": 1})))
	require.NoError(t, c.Load())
	require.NoError(t, c.Set("a", 2))
	require.NoError(t, c.Load())

	assert.Equal(t, 1, c.Get("a"))
}

// Nil values load nothing, and the load succeeds, where a Config with no
// provider fails for want of an environment prefix.
func TestStaticProvider_NilValues(t *testing.T) {
	t.Parallel()

	c := config.New(config.WithProvider(configtest.NewStaticProvider(nil)))
	require.NoError(t, c.Load())

	assert.Empty(t, c.Values())
}

// Two keys that become the same fail the load, with the provider named.
func TestStaticProvider_ConflictingKeys(t *testing.T) {
	t.Parallel()

	c := config.New(config.WithProvider(configtest.NewStaticProvider(m{"Host": 1, "host": 2})))

	err := c.Load()
	require.ErrorIs(t, err, config.ErrKeyConflict)
	assert.ErrorContains(t, err, "failed to load from provider static: conflicting keys")
}
