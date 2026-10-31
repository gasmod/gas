package template_test

import (
	"context"
	"io/fs"
	"maps"
	"slices"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/template"
	"github.com/gasmod/gas/template/dir"
	templatefs "github.com/gasmod/gas/template/fs"
	"github.com/gasmod/gas/template/internal/conformance"
	"github.com/gasmod/gas/template/memory"
)

// The example in the package documentation's Usage section.
func TestDoc_Usage(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	store, err := memory.NewStore()()
	require.NoError(t, err)

	err = store.Register(ctx, "pages/home.html", []byte("<h1>{{.Title}}</h1>"))
	require.NoError(t, err)

	b, err := store.Get(ctx, "pages/home.html")
	require.NoError(t, err)
	assert.Equal(t, "<h1>{{.Title}}</h1>", string(b))

	names, err := store.List(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"pages/home.html"}, names)

	_, err = store.Get(ctx, "missing.html")
	require.ErrorIs(t, err, template.ErrTemplateNotFound)

	w := gas.NewWorker(gas.WithSingletonService[gas.TemplateProvider](memory.NewStore()))
	require.NoError(t, w.Start(ctx))
}

// denyFS is a file system that fails to open one path with
// fs.ErrPermission, giving an absolute path as some file systems do.
type denyFS struct {
	fs.FS

	// deny is the path it fails to open.
	deny string
}

// Open opens name, or fails for deny.
func (d denyFS) Open(name string) (fs.File, error) {
	if name == d.deny {
		return nil, &fs.PathError{Op: "open", Path: "/srv/app/" + name, Err: fs.ErrPermission}
	}

	return d.FS.Open(name)
}

// The error messages the package documentation lists are the ones the
// stores return, and it lists every message this test makes.
//
//nolint:funlen // one case per documented message.
func TestDoc_Errors(t *testing.T) {
	t.Parallel()

	seed := fstest.MapFS{"pages/home.html": {Data: []byte("home")}}
	newFS := func(fsys fs.FS) *templatefs.Store {
		s, err := templatefs.NewStore(fsys)()
		require.NoError(t, err)

		return s
	}
	newDir := func() *dir.Store {
		s, err := dir.NewStore(t.TempDir())()
		require.NoError(t, err)

		return s
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()

	cases := map[string]func() error{
		"template: invalid name: empty": func() error {
			return template.CheckName("", nil)
		},
		"template: invalid name: not valid UTF-8": func() error {
			return template.CheckName("\xff.html", nil)
		},
		"template: invalid name: holds a NUL byte": func() error {
			return template.CheckName("\x00.html", nil)
		},
		"template: invalid name: holds a backslash": func() error {
			return template.CheckName(`a\b.html`, nil)
		},
		"template: invalid name: not a clean slash path": func() error {
			return template.CheckName("../a.html", nil)
		},
		"template: invalid name: extension not allowed": func() error {
			return template.CheckName("a.txt", html)
		},
		"template: invalid extension: none given": func() error {
			return template.CheckExtensions(nil)
		},
		"template: invalid extension: extension 2 must be a dot and one or more characters, " +
			"none a slash, backslash or NUL": func() error {
			return template.CheckExtensions([]string{".html", "txt"})
		},
		`template: not found: "pages/home.html"`: func() error {
			_, err := newFS(fstest.MapFS{}).Get(t.Context(), "pages/home.html")

			return err
		},
		"template: invalid max size: must be at least 1 byte": func() error {
			_, err := memory.NewStore(memory.WithMaxSize(0))()

			return err
		},
		`template: too large: "pages/home.html": over 16777216 bytes`: func() error {
			s, err := memory.NewStore()()
			require.NoError(t, err)

			return s.Register(
				t.Context(),
				"pages/home.html",
				make([]byte, template.DefaultMaxSize+1),
			)
		},
		"template: read-only store": func() error {
			return newFS(seed).Register(t.Context(), "pages/home.html", nil)
		},
		"template: file system is nil": func() error {
			s, err := memory.NewStore()()
			require.NoError(t, err)

			return s.RegisterFS(t.Context(), nil)
		},
		"template: store not initialized": func() error {
			_, err := newDir().Get(t.Context(), "a.html")

			return err
		},
		"template: store already initialized": func() error {
			s := newDir()
			require.NoError(t, s.Init(t.Context()))
			t.Cleanup(func() { require.NoError(t, s.Close(context.Background())) })

			return s.Init(t.Context())
		},
		"template: store closed": func() error {
			s := newDir()
			require.NoError(t, s.Init(t.Context()))
			require.NoError(t, s.Close(t.Context()))

			_, err := s.List(t.Context())

			return err
		},
		`template: read failed: "pages/home.html": permission denied`: func() error {
			_, err := newFS(
				denyFS{FS: seed, deny: "pages/home.html"},
			).Get(t.Context(), "pages/home.html")

			return err
		},
		`template: list failed: "pages": permission denied`: func() error {
			_, err := newFS(denyFS{FS: seed, deny: "pages"}).List(t.Context())

			return err
		},
		"template: list failed: context canceled": func() error {
			_, err := newFS(seed).List(canceled)

			return err
		},
		"template: register files: context canceled": func() error {
			s, err := memory.NewStore()()
			require.NoError(t, err)

			return s.RegisterFS(canceled, seed)
		},
	}

	documented := conformance.DocLines(t, "../doc.go", "template: ")
	assert.ElementsMatch(t, documented, slices.Collect(maps.Keys(cases)))

	for msg, produce := range cases {
		assert.EqualError(t, produce(), msg)
	}
}
