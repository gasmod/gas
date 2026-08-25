package providers_test

import (
	"os"
	"testing"
	"testing/fstest"

	"github.com/gasmod/gas/config/providers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDotEnvProvider_DefaultOptions(t *testing.T) {
	t.Parallel()

	p := providers.NewDotEnvProvider()
	_, err := p.Load()
	require.Error(t, err)
}

func TestDotEnvProvider_WithDotEnvFile_FileNotFound(t *testing.T) {
	t.Parallel()

	p := providers.NewDotEnvProvider(
		providers.WithDotEnvFilePath(".env.non-existing"),
	)
	_, err := p.Load()
	require.Error(t, err)
}

func TestDotEnvProvider_WithDotEnvFile(t *testing.T) {
	t.Parallel()

	fsys := fstest.MapFS{
		".env": &fstest.MapFile{
			Data: []byte(`
				TEST_KEY=test_value
			`),
		},
	}

	p := providers.NewDotEnvProvider(
		providers.WithDotEnvFilePath(".env"),
		providers.WithDotEnvFileFS(&fsys),
	)

	values, err := p.Load()
	require.NoError(t, err)
	assert.Equal(t, "test_value", values["test_key"])
}

func TestDotEnvProvider_WithEnvSeparator(t *testing.T) {
	t.Parallel()

	fsys := fstest.MapFS{
		".env": &fstest.MapFile{
			Data: []byte(`
				TEST__KEY=test_value
			`),
		},
	}

	p := providers.NewDotEnvProvider(
		providers.WithDotEnvFilePath(".env"),
		providers.WithDotEnvFileFS(&fsys),
		providers.WithDotEnvSeparator("__"),
	)

	values, err := p.Load()
	require.NoError(t, err)
	assert.IsType(t, map[string]any{}, values["test"])
	assert.Equal(t, "test_value", values["test"].(map[string]any)["key"])
}

func TestDotEnvProvider_WithEnvNormalizeVarNames(t *testing.T) {
	t.Parallel()

	fsys := fstest.MapFS{
		".env": &fstest.MapFile{
			Data: []byte(`
				TEST_KEY=test_value
			`),
		},
	}

	p := providers.NewDotEnvProvider(
		providers.WithDotEnvFilePath(".env"),
		providers.WithDotEnvFileFS(&fsys),
	)

	values, err := p.Load()
	require.NoError(t, err)
	assert.Equal(t, "test_value", values["test_key"])
}

func TestDotEnvProvider_Syntax(t *testing.T) {
	t.Parallel()

	fsys := fstest.MapFS{
		".env": &fstest.MapFile{
			Data: []byte(`
				# This is a comment
				TEST_KEY=test_value
				TEST_KEY2=test_value2 # This is an inline comment
			`),
		},
	}

	p := providers.NewDotEnvProvider(
		providers.WithDotEnvFilePath(".env"),
		providers.WithDotEnvFileFS(&fsys),
	)

	values, err := p.Load()
	require.NoError(t, err)
	assert.Equal(t, "test_value", values["test_key"])
	assert.Equal(t, "test_value2", values["test_key2"])
}

func TestDotEnvProvider_WithDotEnvFile_FileNotFoundNoPanic(t *testing.T) {
	t.Parallel()

	p := providers.NewDotEnvProvider(
		providers.WithDotEnvFilePath(".env.non-existing"),
		providers.WithDotEnvFileNotFoundPanic(false),
	)
	_, err := p.Load()
	require.NoError(t, err)
}

func TestDotEnvProvider_AppendToOSEnv(t *testing.T) {
	// NOT parallel: mutates the global process environment via os.Setenv.
	const key = "TEST_KEY"
	orig, had := os.LookupEnv(key)
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(key, orig)
		} else {
			_ = os.Unsetenv(key)
		}
	})

	fsys := fstest.MapFS{
		".env": &fstest.MapFile{
			Data: []byte(key + "=test_value"),
		},
	}

	// Mirroring must be opted into explicitly; it is not the default.
	p := providers.NewDotEnvProvider(
		providers.WithDotEnvFilePath(".env"),
		providers.WithDotEnvFileFS(&fsys),
		providers.WithDotEnvFileAppendToOSEnv(true),
	)

	values, err := p.Load()
	require.NoError(t, err)
	assert.Equal(t, "test_value", values["test_key"])
	assert.Equal(t, "test_value", os.Getenv(key))
}

func TestDotEnvProvider_NoAppendToOSEnv(t *testing.T) {
	t.Parallel()

	fsys := fstest.MapFS{
		".env": &fstest.MapFile{
			Data: []byte("MY_KEY=test_value"),
		},
	}

	p := providers.NewDotEnvProvider(
		providers.WithDotEnvFilePath(".env"),
		providers.WithDotEnvFileFS(&fsys),
		providers.WithDotEnvFileAppendToOSEnv(false),
	)

	values, err := p.Load()
	require.NoError(t, err)
	assert.Equal(t, "test_value", values["my_key"])
	assert.Empty(t, os.Getenv("MY_KEY"), "Expected os.Getenv(\"MY_KEY\") to be empty")
}
