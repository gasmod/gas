package providers_test

import (
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/config/providers"
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

// The default file system must not follow a symlinked directory out of the
// working directory. This changes the working directory, so it is not
// parallel.
func TestJSONProvider_SymlinkedDirectoryOutside(t *testing.T) {
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(outside, "secret.json"), []byte(`{"k":"v"}`), 0o600,
	))

	wd, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	t.Chdir(wd)
	require.NoError(t, os.Symlink(outside, filepath.Join(wd, "link")))

	values, err := providers.NewJSONProvider(
		providers.WithJSONFilePath("link/secret.json"),
	).Load()
	require.ErrorIs(t, err, providers.ErrJSONFileReadFailed)
	assert.Nil(t, values)
}
