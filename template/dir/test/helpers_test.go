package dir_test

import (
	"context"
	"os"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/template/dir"
	"github.com/gasmod/gas/template/internal/conformance"
)

// open returns a store over a new directory holding seed, with the
// conformance extensions, initialized and closed when the test ends, and
// the directory.
func open(t *testing.T, seed fstest.MapFS) (*dir.Store, string) {
	t.Helper()

	d := t.TempDir()
	conformance.WriteFiles(t, d, seed)

	return openAt(t, d), d
}

// openAt returns a store over d with the conformance extensions,
// initialized and closed when the test ends.
func openAt(t *testing.T, d string) *dir.Store {
	t.Helper()

	s, err := dir.NewStore(d, dir.WithExtensions(conformance.Extensions...))()
	require.NoError(t, err)
	require.NoError(t, s.Init(t.Context()))
	t.Cleanup(func() { require.NoError(t, s.Close(context.Background())) })

	return s
}

// skipAsRoot skips a test that relies on file permissions when it runs as
// root, which reads every file.
func skipAsRoot(t *testing.T) {
	t.Helper()

	if os.Geteuid() == 0 {
		t.Skip("root ignores file permissions")
	}
}

// unlock gives the directory at path back its owner's permissions.
func unlock(t *testing.T, path string) {
	t.Helper()

	err := os.Chmod(path, 0o700) //nolint:gosec // G302: a directory needs its search bit.
	require.NoError(t, err)
}
