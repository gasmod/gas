package providers_test

import (
	"testing"
	"testing/fstest"

	"github.com/gasmod/gas/config/providers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJSONProvider_DefaultOptions(t *testing.T) {
	t.Parallel()

	p := providers.NewJSONProvider()
	_, err := p.Load()
	assert.Error(t, err)
}

func TestJSONProvider_WithJSONFile_FileNotFound(t *testing.T) {
	t.Parallel()

	p := providers.NewJSONProvider(
		providers.WithJSONFilePath("non-existing.json"),
	)
	_, err := p.Load()
	assert.Error(t, err)
}

func TestJSONProvider_WithJSONFile(t *testing.T) {
	t.Parallel()

	fsys := fstest.MapFS{
		"config.json": &fstest.MapFile{
			Data: []byte(`{"testKey": "test_value"}`),
		},
	}

	p := providers.NewJSONProvider(
		providers.WithJSONFilePath("config.json"),
		providers.WithJSONFileFS(&fsys),
	)

	values, err := p.Load()
	require.NoError(t, err)

	assert.Equal(t, "test_value", values["testKey"])
}
