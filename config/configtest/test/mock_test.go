package configtest_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/config/configtest"
)

type m = map[string]any

var _ config.ConfigProvider = (*configtest.MockConfig)(nil)

var errSet = errors.New("set")

type static map[string]any

func (s static) Name() string                  { return "static" }
func (s static) Load() (map[string]any, error) { return s, nil }

// With no field set, every method returns zero values and records its
// name and arguments. LoadProviderContext leaves the context out.
func TestMock_ZeroValues(t *testing.T) {
	t.Parallel()

	var mock configtest.MockConfig

	p := static{}
	dest := &struct{}{}

	require.NoError(t, mock.SetDefault("a", 1))
	require.NoError(t, mock.SetDefaults(m{"b": 2}))
	require.NoError(t, mock.Set("c", 3))
	require.NoError(t, mock.Bind(dest))
	assert.Nil(t, mock.Get("d"))

	got, ok := mock.Find("e")
	assert.Nil(t, got)
	assert.False(t, ok)
	assert.Nil(t, mock.Values())
	require.NoError(t, mock.LoadProvider(p))
	require.NoError(t, mock.LoadProviderContext(context.Background(), p))

	assert.Equal(t, []configtest.Call{
		{Method: "SetDefault", Args: []any{"a", 1}},
		{Method: "SetDefaults", Args: []any{m{"b": 2}}},
		{Method: "Set", Args: []any{"c", 3}},
		{Method: "Bind", Args: []any{dest}},
		{Method: "Get", Args: []any{"d"}},
		{Method: "Find", Args: []any{"e"}},
		{Method: "Values", Args: nil},
		{Method: "LoadProvider", Args: []any{p}},
		{Method: "LoadProviderContext", Args: []any{p}},
	}, mock.Calls)
}

// Each method calls its field, and returns what it returns.
func TestMock_Fields(t *testing.T) {
	t.Parallel()

	var setDefault, set []any

	mock := configtest.MockConfig{
		SetDefaultFn: func(key string, value any) error {
			setDefault = []any{key, value}

			return errSet
		},
		SetDefaultsFn: func(any) error { return errSet },
		SetFn: func(key string, value any) error {
			set = []any{key, value}

			return errSet
		},
		BindFn:                func(any) error { return errSet },
		GetFn:                 func(key string) any { return key + "!" },
		FindFn:                func(key string) (any, bool) { return key, true },
		ValuesFn:              func() map[string]any { return m{"a": 1} },
		LoadProviderFn:        func(config.Provider) error { return errSet },
		LoadProviderContextFn: func(context.Context, config.Provider) error { return errSet },
	}

	require.ErrorIs(t, mock.SetDefault("a", 1), errSet)
	require.ErrorIs(t, mock.Set("b", 2), errSet)
	assert.Equal(t, []any{"a", 1}, setDefault)
	assert.Equal(t, []any{"b", 2}, set)
	require.ErrorIs(t, mock.SetDefaults(nil), errSet)
	require.ErrorIs(t, mock.Bind(nil), errSet)
	assert.Equal(t, "k!", mock.Get("k"))

	got, ok := mock.Find("k")
	assert.Equal(t, "k", got)
	assert.True(t, ok)
	assert.Equal(t, m{"a": 1}, mock.Values())
	require.ErrorIs(t, mock.LoadProvider(nil), errSet)
	require.ErrorIs(t, mock.LoadProviderContext(context.Background(), nil), errSet)
}

// CallCount counts the calls of one method, and Reset clears them. Calls
// may come from several goroutines at once.
func TestMock_CallCountAndReset(t *testing.T) {
	t.Parallel()

	var mock configtest.MockConfig

	var wg sync.WaitGroup

	for range 8 {
		wg.Go(func() { _ = mock.Get("a") })
		wg.Go(func() { _ = mock.Set("a", 1) })
	}

	wg.Wait()

	assert.Equal(t, 8, mock.CallCount("Get"))
	assert.Equal(t, 8, mock.CallCount("Set"))
	assert.Zero(t, mock.CallCount("Find"))

	mock.Reset()
	assert.Empty(t, mock.Calls)
	assert.Zero(t, mock.CallCount("Get"))
}

// NewMockConfigWithValues behaves as a config.Config holding the values,
// with keys trimmed and lowercased, and still records calls.
func TestNewMockConfigWithValues(t *testing.T) {
	t.Parallel()

	mock, err := configtest.NewMockConfigWithValues(m{"Database": m{" Host ": "db"}})
	require.NoError(t, err)

	assert.Equal(t, "db", mock.Get("database.host"))

	got, ok := mock.Find("DATABASE.HOST")
	assert.True(t, ok)
	assert.Equal(t, "db", got)

	var app struct{ Database struct{ Host string } }

	require.NoError(t, mock.Bind(&app))
	assert.Equal(t, "db", app.Database.Host)

	require.NoError(t, mock.Set("database.port", 5432))
	require.NoError(t, mock.LoadProvider(static{"cache": "on"}))
	assert.Equal(t, m{"database": m{"host": "db", "port": 5432}, "cache": "on"}, mock.Values())

	assert.Equal(t, 1, mock.CallCount("Get"))
	assert.Equal(t, 1, mock.CallCount("LoadProvider"))

	mock.GetFn = func(string) any { return "replaced" }
	assert.Equal(t, "replaced", mock.Get("database.host"))
}

// Nil values make an empty configuration, which the environment does not
// reach.
//
// Not parallel: it sets an environment variable.
func TestNewMockConfigWithValues_Empty(t *testing.T) {
	t.Setenv("CONFIGTEST__LEAK", "x")

	mock, err := configtest.NewMockConfigWithValues(nil)
	require.NoError(t, err)
	assert.Equal(t, m{}, mock.Values())
	assert.Nil(t, mock.Get("configtest.leak"))
}

// Two keys that become the same are an error.
func TestNewMockConfigWithValues_Conflict(t *testing.T) {
	t.Parallel()

	mock, err := configtest.NewMockConfigWithValues(m{"Host": 1, "host": 2})
	assert.Nil(t, mock)
	require.EqualError(
		t,
		err,
		`config.SetDefaults: set defaults: conflicting keys: "Host" and "host"`,
	)
}
