package ui_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/template/dir"
	tfs "github.com/gasmod/gas/template/fs"
	"github.com/gasmod/gas/template/memory"
	"github.com/gasmod/gas/ui/internal/conformance"
)

// extensions are the extensions of the suite's stores.
var extensions = []string{".html", ".txt"}

// The service keeps the contract over a memory store.
func TestConformance_Memory(t *testing.T) {
	t.Parallel()

	conformance.Run(t, conformance.Subject{
		New: func(t *testing.T, seed fstest.MapFS) gas.TemplateProvider {
			t.Helper()

			s, err := memory.NewStore(memory.WithExtensions(extensions...))()
			require.NoError(t, err)
			require.NoError(t, s.RegisterFS(t.Context(), seed))

			return s
		},
	})
}

// The service keeps the contract over an fs store, which is read-only.
func TestConformance_FS(t *testing.T) {
	t.Parallel()

	conformance.Run(t, conformance.Subject{
		New: func(t *testing.T, seed fstest.MapFS) gas.TemplateProvider {
			t.Helper()

			s, err := tfs.NewStore(seed, tfs.WithExtensions(extensions...))()
			require.NoError(t, err)

			return s
		},
	})
}

// The service keeps the contract over a directory store.
func TestConformance_Dir(t *testing.T) {
	t.Parallel()

	conformance.Run(t, conformance.Subject{
		New: func(t *testing.T, seed fstest.MapFS) gas.TemplateProvider {
			t.Helper()

			root := t.TempDir()

			for name, f := range seed {
				path := filepath.Join(root, filepath.FromSlash(name))
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
				require.NoError(t, os.WriteFile(path, f.Data, 0o600))
			}

			s, err := dir.NewStore(root, dir.WithExtensions(extensions...))()
			require.NoError(t, err)
			require.NoError(t, s.Init(t.Context()))
			t.Cleanup(func() { require.NoError(t, s.Close(context.Background())) })

			return s
		},
	})
}
