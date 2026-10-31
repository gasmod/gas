package dir_test

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/template/dir"
	"github.com/gasmod/gas/template/internal/conformance"
)

// The example in the package documentation's Usage section.
func TestDoc_Usage(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	templates := t.TempDir()
	conformance.WriteFiles(
		t,
		templates,
		fstest.MapFS{"pages/home.html": {Data: []byte("<h1>home</h1>")}},
	)

	store, err := dir.NewStore(templates)()
	require.NoError(t, err)
	require.NoError(t, store.Init(ctx))

	defer func() { require.NoError(t, store.Close(ctx)) }()

	b, err := store.Get(ctx, "pages/home.html")
	require.NoError(t, err)
	assert.Equal(t, "<h1>home</h1>", string(b))
}

// The error messages the package documentation lists are the ones the
// store returns, and it lists every message this test makes.
func TestDoc_Errors(t *testing.T) {
	t.Parallel()

	initAt := func(path string) error {
		s, err := dir.NewStore(path)()
		require.NoError(t, err)

		return s.Init(context.Background())
	}

	cases := map[string]func() error{
		"template: directory path is empty": func() error {
			_, err := dir.NewStore("")()

			return err
		},
		"template: directory unavailable: no such file or directory": func() error {
			return initAt(filepath.Join(t.TempDir(), "missing"))
		},
		"template: directory unavailable: not a directory": func() error {
			d := t.TempDir()
			conformance.WriteFiles(t, d, fstest.MapFS{"f": {}})

			return initAt(filepath.Join(d, "f"))
		},
		"template: list failed: directory removed or moved": func() error {
			d := filepath.Join(t.TempDir(), "templates")
			require.NoError(t, os.Mkdir(d, 0o700))

			s, err := dir.NewStore(d)()
			require.NoError(t, err)
			require.NoError(t, s.Init(t.Context()))
			t.Cleanup(func() { require.NoError(t, s.Close(context.Background())) })
			require.NoError(t, os.Remove(d))

			_, err = s.List(t.Context())

			return err
		},
		`template: get "pages/home.html": context canceled`: func() error {
			s, err := dir.NewStore(t.TempDir())()
			require.NoError(t, err)
			require.NoError(t, s.Init(t.Context()))
			t.Cleanup(func() { require.NoError(t, s.Close(context.Background())) })

			ctx, cancel := context.WithCancel(t.Context())
			cancel()

			_, err = s.Get(ctx, "pages/home.html")

			return err
		},
	}

	documented := conformance.DocLines(t, "../doc.go", "template: ")
	assert.ElementsMatch(t, documented, slices.Collect(maps.Keys(cases)))

	for msg, produce := range cases {
		assert.EqualError(t, produce(), msg)
	}
}
