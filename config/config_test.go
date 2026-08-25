package config_test

import (
	"context"
	"errors"
	"testing"
	"testing/fstest"
	"time"

	config "github.com/gasmod/gas/config"
	"github.com/gasmod/gas/config/providers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockProvider is a mock implementation of the Provider interface for testing.
type mockProvider struct {
	err  error
	data map[string]any
	name string
}

func (m *mockProvider) Name() string {
	return m.name
}

func (m *mockProvider) Load() (map[string]any, error) {
	if m.err != nil {
		return nil, m.err
	}

	return m.data, nil
}

func TestConfig_WithProviders(t *testing.T) {
	t.Parallel()

	mockP1 := &mockProvider{name: "mock1", data: map[string]any{"mock1key": "m1value"}}
	mockP2 := &mockProvider{name: "mock2", data: map[string]any{"mock2key": "m2value"}}
	cfg := config.New(config.WithProvider(mockP1), config.WithProvider(mockP2))

	require.NoError(t, cfg.Load())

	// Check that data from mocks is loaded
	assert.Equal(t, "m1value", cfg.Get("mock1key"))
	assert.Equal(t, "m2value", cfg.Get("mock2key"))
}

func TestConfig_WithEnvProvider_AlreadyPresent(t *testing.T) {
	t.Parallel()

	mockP1 := &mockProvider{name: "env", data: map[string]any{"customEnv": "custom"}}
	mockP2 := &mockProvider{name: "mock2", data: map[string]any{"mockKey": "mockValue"}}
	cfg := config.New(config.WithProvider(mockP1), config.WithProvider(mockP2))

	err := cfg.Load()
	require.NoError(t, err)

	assert.Equal(t, "custom", cfg.Get("customEnv"))
	assert.Equal(t, "mockValue", cfg.Get("mockKey"))
}

func TestConfig_NoProviders(t *testing.T) {
	t.Parallel()

	cfg := config.New()

	require.NoError(t, cfg.Load())
	// NOTE: actual env variables may be present in Values()
	assert.NotNil(t, cfg.Values())
}

func TestConfig_LoadProviderError(t *testing.T) {
	t.Parallel()

	mockP1 := &mockProvider{name: "mock1", err: errors.New("load failed")}
	cfg := config.New(config.WithProvider(mockP1))

	err := cfg.Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to load from provider mock1: load failed")
	assert.ErrorIs(t, err, config.ErrProviderLoadFailed)
}

func TestConfig_Bind(t *testing.T) {
	t.Parallel()

	mockP1 := &mockProvider{name: "mock", data: map[string]any{
		"myKey": "value",
	}}

	cfg := config.New(config.WithProvider(mockP1))

	require.NoError(t, cfg.Load())

	obj := struct {
		MyKey string
	}{}

	require.NoError(t, cfg.Bind(&obj))

	assert.Equal(t, "value", obj.MyKey)
}

func TestConfig_BindError(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{}
	obj := "not a struct"
	err := cfg.Bind(obj)
	assert.Error(t, err)
}

func TestConfig_Bind_WithValidate(t *testing.T) {
	t.Parallel()

	mockP1 := &mockProvider{name: "mock", data: map[string]any{
		"myKey": "longer-than-10-characters",
	}}

	cfg := config.New(config.WithProvider(mockP1))

	require.NoError(t, cfg.Load())

	obj := struct {
		MyKey string `validate:"required,min=1,max=10"`
	}{}

	err := cfg.Bind(&obj)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MyKey")
	assert.Contains(t, err.Error(), "max")
}

func TestConfig_Get(t *testing.T) {
	t.Parallel()

	mockP1 := &mockProvider{name: "mock", data: map[string]any{
		"simple": "value",
		"nested": map[string]any{
			"key": "nestedvalue",
		},
	}}
	cfg := config.New(config.WithProvider(mockP1))

	err := cfg.Load()
	require.NoError(t, err)

	assert.Equal(t, "value", cfg.Get("simple"))
	assert.Equal(t, "nestedvalue", cfg.Get("nested.key"))
	assert.Nil(t, cfg.Get("nonexistent"))
	assert.Nil(t, cfg.Get("nested.nonexistent"))
	assert.Nil(t, cfg.Get("")) // empty key
}

func TestConfig_Values(t *testing.T) {
	t.Parallel()

	mockP1 := &mockProvider{name: "mock", data: map[string]any{"key": "value"}}
	cfg := config.New(config.WithProvider(mockP1))

	err := cfg.Load()
	require.NoError(t, err)

	values := cfg.Values()
	assert.NotNil(t, values)
	// Since env vars are loaded, check specific key
	assert.Equal(t, "value", cfg.Get("key"))
}

func TestConfig_SetDefault_Basic(t *testing.T) {
	t.Parallel()

	cfg := config.New()

	cfg.SetDefault("key", "value")

	assert.Equal(t, "value", cfg.Get("key"))
}

func TestConfig_SetDefault_Nested(t *testing.T) {
	t.Parallel()

	cfg := config.New()

	cfg.SetDefault("database.host", "localhost")

	assert.Equal(t, "localhost", cfg.Get("database.host"))

	// Verify nested structure
	values := cfg.Values()
	db, ok := values["database"]
	assert.True(t, ok)
	dbMap, ok := db.(map[string]any)
	assert.True(t, ok)
	assert.Equal(t, "localhost", dbMap["host"])
}

func TestConfig_SetDefault_MultipleLevels(t *testing.T) {
	t.Parallel()

	cfg := config.New()

	cfg.SetDefault("a.b.c.d", "deepvalue")

	assert.Equal(t, "deepvalue", cfg.Get("a.b.c.d"))

	values := cfg.Values()
	a, ok := values["a"].(map[string]any)
	require.True(t, ok)
	b, ok := a["b"].(map[string]any)
	require.True(t, ok)
	c, ok := b["c"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "deepvalue", c["d"])
}

func TestConfig_SetDefault_EmptyKey(t *testing.T) {
	t.Parallel()

	cfg := config.New()

	cfg.SetDefault("", "value")

	values := cfg.Values()
	// Empty key should be ignored, no value should be set
	assert.NotNil(t, values)
	// But since it's ignored, no specific key
}

func TestConfig_SetDefault_NoOverride(t *testing.T) {
	t.Parallel()

	cfg := config.New()

	cfg.SetDefault("key", "oldvalue")
	assert.Equal(t, "oldvalue", cfg.Get("key"))

	// SetDefault should NOT override existing values
	cfg.SetDefault("key", "newvalue")
	assert.Equal(t, "oldvalue", cfg.Get("key"))
}

func TestConfig_SetDefault_WithSpaces(t *testing.T) {
	t.Parallel()

	cfg := config.New()

	cfg.SetDefault("key.with spaces", "value")

	assert.Equal(t, "value", cfg.Get("key.with spaces"))
}

func TestConfig_SetDefaults_ValidStruct(t *testing.T) {
	t.Parallel()

	cfg := config.New()

	s := struct {
		Key   string
		Value int
	}{
		Key:   "testkey",
		Value: 42,
	}

	err := cfg.SetDefaults(&s)
	require.NoError(t, err)

	assert.Equal(t, "testkey", cfg.Get("key"))
	assert.Equal(t, 42, cfg.Get("value"))
}

func TestConfig_SetDefaults_StructWithoutJSONTags(t *testing.T) {
	t.Parallel()

	cfg := config.New()

	s := struct {
		Key   string
		Value int
	}{
		Key:   "test",
		Value: 123,
	}

	err := cfg.SetDefaults(&s)
	require.NoError(t, err)

	assert.Equal(t, "test", cfg.Get("key"))
	assert.Equal(t, 123, cfg.Get("value"))
}

func TestConfig_SetDefaults_Map(t *testing.T) {
	t.Parallel()

	cfg := config.New()

	m := map[string]any{
		"key1": "value1",
		"key2": map[string]any{
			"nested": "nestedvalue",
		},
	}

	err := cfg.SetDefaults(&m)
	require.NoError(t, err)

	assert.Equal(t, "value1", cfg.Get("key1"))
	assert.Equal(t, "nestedvalue", cfg.Get("key2.nested"))
}

func TestConfig_SetDefaults_NilValues(t *testing.T) {
	t.Parallel()

	cfg := config.New()

	err := cfg.SetDefaults(nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, config.ErrNilValues)
}

func TestConfig_SetDefaults_InvalidType(t *testing.T) {
	t.Parallel()

	cfg := config.New()

	err := cfg.SetDefaults(42) // not a pointer
	require.Error(t, err)
}

func TestConfig_SetDefaults_NilPointer(t *testing.T) {
	t.Parallel()

	cfg := config.New()

	var s *struct{}

	err := cfg.SetDefaults(s) // nil pointer
	require.Error(t, err)
}

func TestConfig_SetDefaults_PointerToNonStructNonMap(t *testing.T) {
	t.Parallel()

	cfg := config.New()

	x := 42

	err := cfg.SetDefaults(&x)
	require.Error(t, err)
}

func TestConfig_SetDefaults_EmptyStruct(t *testing.T) {
	t.Parallel()

	cfg := config.New()

	s := struct{}{}

	err := cfg.SetDefaults(&s)
	require.NoError(t, err)

	values := cfg.Values()
	assert.NotNil(t, values)
	// Empty struct should not add any keys
}

func TestConfig_SetDefaults_Merge(t *testing.T) {
	t.Parallel()

	cfg := config.New()

	s1 := struct {
		Key1 string `json:"key1"`
	}{Key1: "value1"}

	s2 := struct {
		Key2 string `json:"key2"`
	}{Key2: "value2"}

	err := cfg.SetDefaults(&s1)
	require.NoError(t, err)
	err = cfg.SetDefaults(&s2)
	require.NoError(t, err)

	assert.Equal(t, "value1", cfg.Get("key1"))
	assert.Equal(t, "value2", cfg.Get("key2"))
}

func TestConfig_SetDefaults_NestedStruct(t *testing.T) {
	t.Parallel()

	cfg := config.New()

	s := struct {
		Database struct {
			Host string `json:"host"`
			Port int    `json:"port"`
		} `json:"database"`
	}{}

	s.Database.Host = "localhost"
	s.Database.Port = 5432

	err := cfg.SetDefaults(&s)
	require.NoError(t, err)

	assert.Equal(t, "localhost", cfg.Get("database.host"))
	assert.Equal(t, 5432, cfg.Get("database.port"))
}

func TestConfig_GetAfterSetDefaults(t *testing.T) {
	t.Parallel()

	cfg := config.New()

	m := map[string]any{
		"app": map[string]any{
			"name":    "myapp",
			"version": "1.0.0",
		},
	}

	err := cfg.SetDefaults(&m)
	require.NoError(t, err)

	assert.Equal(t, "myapp", cfg.Get("app.name"))
	assert.Equal(t, "1.0.0", cfg.Get("app.version"))
}

func TestConfig_SetDefaultAndSetDefaultsInteraction(t *testing.T) {
	t.Parallel()

	cfg := config.New()

	cfg.SetDefault("key", "setdefault")
	assert.Equal(t, "setdefault", cfg.Get("key"))

	s := struct {
		Key string `json:"key"`
	}{Key: "setdefaults"}

	err := cfg.SetDefaults(&s)
	require.NoError(t, err)

	// SetDefaults should NOT override existing values
	assert.Equal(t, "setdefault", cfg.Get("key"))
}

func TestConfig_SetDefault_NestedNoOverride(t *testing.T) {
	t.Parallel()

	cfg := config.New()

	cfg.SetDefault("database.host", "localhost")
	cfg.SetDefault("database.port", 5432)
	assert.Equal(t, "localhost", cfg.Get("database.host"))
	assert.Equal(t, 5432, cfg.Get("database.port"))

	// Should not override existing nested values
	cfg.SetDefault("database.host", "remotehost")
	cfg.SetDefault("database.timeout", 30)
	assert.Equal(t, "localhost", cfg.Get("database.host")) // not overridden
	assert.Equal(t, 5432, cfg.Get("database.port"))        // unchanged
	assert.Equal(t, 30, cfg.Get("database.timeout"))       // new value added
}

func TestConfig_SetDefaults_NoOverride(t *testing.T) {
	t.Parallel()

	cfg := config.New()

	// Set initial values
	cfg.SetDefault("key1", "original1")
	cfg.SetDefault("nested.key", "originalnested")

	s := struct {
		Key1   string `json:"key1"`
		Key2   string `json:"key2"`
		Nested struct {
			Key    string `json:"key"`
			NewKey string `json:"newkey"`
		} `json:"nested"`
	}{
		Key1: "new1",
		Key2: "new2",
		Nested: struct {
			Key    string `json:"key"`
			NewKey string `json:"newkey"`
		}{
			Key:    "newnested",
			NewKey: "addednested",
		},
	}

	err := cfg.SetDefaults(&s)
	require.NoError(t, err)

	// Existing values should not be overridden
	assert.Equal(t, "original1", cfg.Get("key1"))
	assert.Equal(t, "originalnested", cfg.Get("nested.key"))
	// New values should be added
	assert.Equal(t, "new2", cfg.Get("key2"))
	assert.Equal(t, "addednested", cfg.Get("nested.newkey"))
}

func TestConfig_Set_Basic(t *testing.T) {
	t.Parallel()

	cfg := config.New()

	cfg.Set("key", "value")
	assert.Equal(t, "value", cfg.Get("key"))
}

func TestConfig_Set_Nested(t *testing.T) {
	t.Parallel()

	cfg := config.New()

	cfg.Set("database.host", "localhost")
	assert.Equal(t, "localhost", cfg.Get("database.host"))

	// Verify nested structure
	values := cfg.Values()
	db, ok := values["database"]
	assert.True(t, ok)
	dbMap, ok := db.(map[string]any)
	assert.True(t, ok)
	assert.Equal(t, "localhost", dbMap["host"])
}

func TestConfig_Set_Override(t *testing.T) {
	t.Parallel()

	cfg := config.New()

	cfg.Set("key", "oldvalue")
	assert.Equal(t, "oldvalue", cfg.Get("key"))

	// Set should override existing values
	cfg.Set("key", "newvalue")
	assert.Equal(t, "newvalue", cfg.Get("key"))
}

func TestConfig_Set_EmptyKey(t *testing.T) {
	t.Parallel()

	cfg := config.New()

	cfg.Set("", "value")

	values := cfg.Values()
	// Empty key should be ignored, no value should be set
	assert.NotNil(t, values)
}

func TestConfig_Set_MultipleLevels(t *testing.T) {
	t.Parallel()

	cfg := config.New()

	cfg.Set("a.b.c.d", "deepvalue")
	assert.Equal(t, "deepvalue", cfg.Get("a.b.c.d"))

	values := cfg.Values()
	a, ok := values["a"].(map[string]any)
	require.True(t, ok)
	b, ok := a["b"].(map[string]any)
	require.True(t, ok)
	c, ok := b["c"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "deepvalue", c["d"])
}

func TestConfig_Set_OverrideNested(t *testing.T) {
	t.Parallel()

	cfg := config.New()

	cfg.Set("database.host", "localhost")
	cfg.Set("database.port", 5432)
	assert.Equal(t, "localhost", cfg.Get("database.host"))
	assert.Equal(t, 5432, cfg.Get("database.port"))

	// Should override existing nested values
	cfg.Set("database.host", "remotehost")
	cfg.Set("database.timeout", 30)
	assert.Equal(t, "remotehost", cfg.Get("database.host")) // overridden
	assert.Equal(t, 5432, cfg.Get("database.port"))         // unchanged
	assert.Equal(t, 30, cfg.Get("database.timeout"))        // new value added
}

func TestConfig_Find_Basic(t *testing.T) {
	t.Parallel()

	cfg := config.New()
	cfg.Set("key", "value")

	value, exists := cfg.Find("key")
	assert.True(t, exists)
	assert.Equal(t, "value", value)
}

func TestConfig_Find_Nested(t *testing.T) {
	t.Parallel()

	cfg := config.New()
	cfg.Set("database.host", "localhost")

	value, exists := cfg.Find("database.host")
	assert.True(t, exists)
	assert.Equal(t, "localhost", value)
}

func TestConfig_Find_NonExistent(t *testing.T) {
	t.Parallel()

	cfg := config.New()

	value, exists := cfg.Find("nonexistent")
	assert.False(t, exists)
	assert.Nil(t, value)
}

func TestConfig_Find_NestedNonExistent(t *testing.T) {
	t.Parallel()

	cfg := config.New()
	cfg.Set("database.host", "localhost")

	value, exists := cfg.Find("database.nonexistent")
	assert.False(t, exists)
	assert.Nil(t, value)

	value, exists = cfg.Find("nonexistent.key")
	assert.False(t, exists)
	assert.Nil(t, value)
}

func TestConfig_Find_EmptyKey(t *testing.T) {
	t.Parallel()

	cfg := config.New()

	value, exists := cfg.Find("")
	assert.False(t, exists)
	assert.Nil(t, value)
}

func TestConfig_Find_WithProvider(t *testing.T) {
	t.Parallel()

	mockP1 := &mockProvider{name: "mock", data: map[string]any{
		"simple": "value",
		"nested": map[string]any{
			"key": "nestedvalue",
		},
	}}
	cfg := config.New(config.WithProvider(mockP1))

	err := cfg.Load()
	require.NoError(t, err)

	value, exists := cfg.Find("simple")
	assert.True(t, exists)
	assert.Equal(t, "value", value)

	value, exists = cfg.Find("nested.key")
	assert.True(t, exists)
	assert.Equal(t, "nestedvalue", value)

	value, exists = cfg.Find("nonexistent")
	assert.False(t, exists)
	assert.Nil(t, value)
}

func TestConfig_SetDefaultVsSet_Behavior(t *testing.T) {
	t.Parallel()

	cfg := config.New()

	// Test SetDefault doesn't override
	cfg.SetDefault("key1", "default1")
	assert.Equal(t, "default1", cfg.Get("key1"))

	cfg.SetDefault("key1", "default2")
	assert.Equal(t, "default1", cfg.Get("key1")) // Not overridden

	// Test Set does override
	cfg.Set("key1", "set1")
	assert.Equal(t, "set1", cfg.Get("key1")) // Overridden

	cfg.Set("key1", "set2")
	assert.Equal(t, "set2", cfg.Get("key1")) // Overridden again
}

func TestConfig_SetDefaults_NoOverride_Complex(t *testing.T) {
	t.Parallel()

	cfg := config.New()

	// Set some initial configuration
	cfg.Set("app.name", "myapp")
	cfg.Set("database.host", "localhost")
	cfg.Set("database.port", 5432)

	defaults := map[string]any{
		"app": map[string]any{
			"name":    "defaultapp", // should not override
			"version": "1.0.0",      // should be added
			"debug":   false,        // should be added
		},
		"database": map[string]any{
			"host":    "defaulthost", // should not override
			"port":    3306,          // should not override
			"timeout": 30,            // should be added
			"ssl":     true,          // should be added
		},
		"cache": map[string]any{ // should be added entirely
			"enabled": true,
			"ttl":     300,
		},
	}

	err := cfg.SetDefaults(defaults)
	require.NoError(t, err)

	// Existing values should not be overridden
	assert.Equal(t, "myapp", cfg.Get("app.name"))
	assert.Equal(t, "localhost", cfg.Get("database.host"))
	assert.Equal(t, 5432, cfg.Get("database.port"))

	// New values should be added
	assert.Equal(t, "1.0.0", cfg.Get("app.version"))
	assert.Equal(t, false, cfg.Get("app.debug"))
	assert.Equal(t, 30, cfg.Get("database.timeout"))
	assert.Equal(t, true, cfg.Get("database.ssl"))
	assert.Equal(t, true, cfg.Get("cache.enabled"))
	assert.Equal(t, 300, cfg.Get("cache.ttl"))
}

// contextAwareProvider implements providers.ContextProvider for testing.
type contextAwareProvider struct {
	gotCtx context.Context
}

func (p *contextAwareProvider) Name() string { return "ContextAware" }

func (p *contextAwareProvider) Load() (map[string]any, error) {
	return nil, errors.New("Load must not be called when LoadContext is implemented")
}

func (p *contextAwareProvider) LoadContext(ctx context.Context) (map[string]any, error) {
	p.gotCtx = ctx

	return map[string]any{"source": "context"}, nil
}

type loadCtxKey struct{}

func TestLoadWithContext_PrefersContextProvider(t *testing.T) {
	ctx := context.WithValue(context.Background(), loadCtxKey{}, "marker")

	p := &contextAwareProvider{}
	cfg := config.New(config.WithProvider(p))

	require.NoError(t, cfg.LoadWithContext(ctx))
	assert.Equal(t, "context", cfg.Get("source"))
	require.NotNil(t, p.gotCtx)
	assert.Equal(t, "marker", p.gotCtx.Value(loadCtxKey{}))
}

// failingContextProvider always fails from LoadContext.
type failingContextProvider struct{}

func (p *failingContextProvider) Name() string { return "FailingContextAware" }

func (p *failingContextProvider) Load() (map[string]any, error) {
	return nil, errors.New("Load must not be called")
}

func (p *failingContextProvider) LoadContext(_ context.Context) (map[string]any, error) {
	return nil, errors.New("boom")
}

func TestLoadWithContext_ContextProviderErrorWrapped(t *testing.T) {
	p := &failingContextProvider{}
	cfg := config.New(config.WithProvider(p))

	err := cfg.LoadWithContext(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, config.ErrProviderLoadFailed)
}

func TestConfig_NullValuesArePreserved(t *testing.T) {
	t.Parallel()

	fsys := fstest.MapFS{
		"config.json": &fstest.MapFile{
			Data: []byte(`{"database": {"host": "localhost", "password": null}}`),
		},
	}

	cfg := config.New(config.WithProvider(providers.NewJSONProvider(
		providers.WithJSONFilePath("config.json"),
		providers.WithJSONFileFS(fsys),
	)))
	require.NoError(t, cfg.Load())

	value, exist := cfg.Find("database.password")
	assert.True(t, exist, "a key explicitly set to null must still exist")
	assert.Nil(t, value)

	db, ok := cfg.Get("database").(map[string]any)
	require.True(t, ok)
	assert.Contains(t, db, "password", "Get must not drop keys whose value is nil")

	values, ok := cfg.Values()["database"].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, values, "password", "Values must not drop keys whose value is nil")
}

func TestConfig_NullInsideArrayDoesNotPanic(t *testing.T) {
	t.Parallel()

	fsys := fstest.MapFS{
		"config.json": &fstest.MapFile{
			Data: []byte(`{"servers": ["a", null, "b"]}`),
		},
	}

	cfg := config.New(config.WithProvider(providers.NewJSONProvider(
		providers.WithJSONFilePath("config.json"),
		providers.WithJSONFileFS(fsys),
	)))
	require.NoError(t, cfg.Load())

	require.NotPanics(t, func() {
		assert.Equal(t, []any{"a", nil, "b"}, cfg.Get("servers"))
	})

	require.NotPanics(t, func() {
		_ = cfg.Values()
	})
}

func TestConfig_BindEmptyDurationReturnsError(t *testing.T) {
	t.Parallel()

	type Settings struct {
		Timeout time.Duration `json:"timeout"`
	}

	// An env var that is present but empty ("APP_TIMEOUT=") reaches Bind as "".
	provider := &mockProvider{name: "mock", data: map[string]any{"timeout": ""}}
	cfg := config.New(config.WithProvider(provider))
	require.NoError(t, cfg.Load())

	var settings Settings

	var err error

	require.NotPanics(t, func() {
		err = cfg.Bind(&settings, config.WithValidate(false))
	})
	require.Error(t, err)
	assert.Equal(t, time.Duration(0), settings.Timeout)
}

func TestConfig_BindFixedSizeArray(t *testing.T) {
	t.Parallel()

	type Settings struct {
		Ports [3]int `json:"ports"`
	}

	fsys := fstest.MapFS{
		"config.json": &fstest.MapFile{
			Data: []byte(`{"ports": [8080, 8081]}`),
		},
	}

	cfg := config.New(config.WithProvider(providers.NewJSONProvider(
		providers.WithJSONFilePath("config.json"),
		providers.WithJSONFileFS(fsys),
	)))
	require.NoError(t, cfg.Load())

	var settings Settings

	require.NotPanics(t, func() {
		require.NoError(t, cfg.Bind(&settings, config.WithValidate(false)))
	})
	assert.Equal(t, [3]int{8080, 8081, 0}, settings.Ports)
}
