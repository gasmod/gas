package providers_test

import (
	"testing"

	"github.com/gasmod/gas/config/providers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnvProvider_DefaultOptions(t *testing.T) {
	t.Setenv("TEST_KEY", "test_value")

	p := providers.NewEnvProvider()

	values, err := p.Load()
	require.NoError(t, err)

	assert.Equal(t, "test_value", values["test_key"])
}

func TestEnvProvider_WithEnvPrefix(t *testing.T) {
	t.Setenv("TEST_KEY", "unaccessible_value")
	t.Setenv("MYAPP_TEST_KEY", "test_value")

	p := providers.NewEnvProvider(
		providers.WithEnvPrefix("MYAPP_"), // load only prefixed variables
	)

	values, err := p.Load()
	require.NoError(t, err)
	assert.Equal(t, "test_value", values["test_key"])
}

func TestEnvProvider_WithEnvSeparator(t *testing.T) {
	t.Setenv("TEST__KEY", "test_value")

	p := providers.NewEnvProvider(
		providers.WithEnvSeparator("__"),
	)

	values, err := p.Load()
	require.NoError(t, err)
	assert.IsType(t, map[string]any{}, values["test"])
	assert.Equal(t, "test_value", values["test"].(map[string]any)["key"])
}

func TestEnvProvider_StandardNameDiscovery(t *testing.T) {
	t.Setenv("DB_HOST", "localhost")

	p := providers.NewEnvProvider()

	values, err := p.Load()
	require.NoError(t, err)

	assert.Equal(t, "localhost", values["db_host"])
}

func TestEnvProvider_StandardNameDiscoveryWithPrefix(t *testing.T) {
	t.Setenv("MYAPP_DB_HOST", "localhost")

	p := providers.NewEnvProvider(
		providers.WithEnvPrefix("MYAPP_"),
	)

	values, err := p.Load()
	require.NoError(t, err)

	assert.Equal(t, "localhost", values["db_host"])
}

func TestEnvProvider_WithEnvNormalizeVarNames(t *testing.T) {
	t.Setenv("TEST_KEY", "test_value")

	p := providers.NewEnvProvider()

	values, err := p.Load()
	require.NoError(t, err)

	// Value can only be accessed using the original names
	assert.Equal(t, "test_value", values["test_key"])
	assert.Empty(t, values["testkey"])
}
