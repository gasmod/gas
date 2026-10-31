package conformance

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/template"
)

// Two seeded templates the rules read.
const (
	// nameA is a template at the root.
	nameA = "a.html"
	// nameAbout is a template in a directory.
	nameAbout = "pages/about.html"
)

// Extensions are the extensions a Subject's store must be built with.
var Extensions = []string{".html", ".txt"}

// Subject is a store under test.
type Subject struct {
	// New returns a new store built with Extensions that holds the
	// templates of seed: a writable store registers seed with RegisterFS,
	// a file store reads its files.
	New func(t *testing.T, seed fstest.MapFS) gas.TemplateProvider
	// ReadOnly reports whether the store cannot be written to.
	ReadOnly bool
}

// Seed is the file system every store starts from. Its templates are
// Listed; the files without an extension of Extensions are not.
func Seed() fstest.MapFS {
	return fstest.MapFS{
		"home.html":   {Data: []byte("home")},
		"B.html":      {Data: []byte("B")},
		"_x.html":     {Data: []byte("_x")},
		nameA:         {Data: []byte("a")},
		"a/b.html":    {Data: []byte("a/b")},
		"empty.html":  {Data: []byte{}},
		"notes.txt":   {Data: []byte("notes")},
		nameAbout:     {Data: []byte("about")},
		"secret.env":  {Data: []byte("DB_PASSWORD=hunter2")},
		"UPPER.HTML":  {Data: []byte("upper")},
		"layout.tmpl": {Data: []byte("tmpl")},
	}
}

// Listed are the names of Seed's templates, in byte order.
var Listed = []string{
	"B.html", "_x.html", nameA, "a/b.html", "empty.html", "home.html", "notes.txt", nameAbout,
}

// InvalidNames are names every store refuses with template.ErrInvalidName.
var InvalidNames = []string{
	"",
	"/a.html",
	"a.html/",
	"./a.html",
	"a/./b.html",
	"a/../a.html",
	"../a.html",
	"a//b.html",
	"a\\b.html",
	"bad\xff.html",
	"nul\x00.html",
	".",
	"..",
	"a.HTML",
	"secret.env",
	"layout.tmpl",
	"a",
}

// Run runs the suite on s.
func Run(t *testing.T, s Subject) {
	t.Helper()

	t.Run("GetSeed", func(t *testing.T) {
		t.Parallel()
		getSeed(t, s.New(t, Seed()))
	})
	t.Run("NotFound", func(t *testing.T) {
		t.Parallel()
		notFound(t, s.New(t, Seed()))
	})
	t.Run("InvalidNames", func(t *testing.T) {
		t.Parallel()
		invalidNames(t, s.New(t, Seed()))
	})
	t.Run("ListAgreesWithGet", func(t *testing.T) {
		t.Parallel()
		listAgrees(t, s.New(t, Seed()))
	})
	t.Run("ExactCase", func(t *testing.T) {
		t.Parallel()
		exactCase(t, s.New(t, Seed()), s.ReadOnly)
	})
	t.Run("GetCopies", func(t *testing.T) {
		t.Parallel()
		getCopies(t, s.New(t, Seed()))
	})
	t.Run("EmptyStore", func(t *testing.T) {
		t.Parallel()
		emptyStore(t, s.New(t, fstest.MapFS{}))
	})
	t.Run("Concurrent", func(t *testing.T) {
		t.Parallel()
		concurrent(t, s.New(t, Seed()), s.ReadOnly)
	})

	if s.ReadOnly {
		t.Run("ReadOnly", func(t *testing.T) {
			t.Parallel()
			readOnly(t, s.New(t, Seed()))
		})

		return
	}

	t.Run("Register", func(t *testing.T) {
		t.Parallel()
		register(t, s.New(t, Seed()))
	})
	t.Run("RegisterFS", func(t *testing.T) {
		t.Parallel()
		registerFS(t, s.New(t, fstest.MapFS{}))
	})
	t.Run("RegisterFSAllOrNothing", func(t *testing.T) {
		t.Parallel()
		registerFSAllOrNothing(t, s.New(t, fstest.MapFS{}))
	})
}

// getSeed checks that every seeded template reads back as it was
// written, empty content as an empty, non-nil slice.
func getSeed(t *testing.T, p gas.TemplateProvider) {
	t.Helper()

	seed := Seed()

	for _, name := range Listed {
		b, err := p.Get(t.Context(), name)
		require.NoError(t, err, name)
		assert.Equal(t, seed[name].Data, b, name)
		assert.NotNil(t, b, name)
	}
}

// notFound checks the error of a name the store does not have.
func notFound(t *testing.T, p gas.TemplateProvider) {
	t.Helper()

	for _, name := range []string{"missing.html", "pages/missing.html", "missing/x.html", "a.html/x.html"} {
		_, err := p.Get(t.Context(), name)
		require.ErrorIs(t, err, template.ErrTemplateNotFound, name)
		require.EqualError(t, err, fmt.Sprintf("template: not found: %q", name))
	}
}

// invalidNames checks that every method that takes a name refuses an
// invalid one, without quoting it.
func invalidNames(t *testing.T, p gas.TemplateProvider) {
	t.Helper()

	for _, name := range InvalidNames {
		_, err := p.Get(t.Context(), name)
		require.ErrorIs(t, err, template.ErrInvalidName, "Get %q", name)

		err = p.Register(t.Context(), name, []byte("x"))
		require.ErrorIs(t, err, template.ErrInvalidName, "Register %q", name)

		if len(name) > len("..") {
			assert.NotContains(t, err.Error(), name)
		}
	}
}

// listAgrees checks that List returns exactly the seeded templates, in
// byte order, and that each reads.
func listAgrees(t *testing.T, p gas.TemplateProvider) {
	t.Helper()

	names, err := p.List(t.Context())
	require.NoError(t, err)
	assert.Equal(t, Listed, names)
	assert.True(t, slices.IsSorted(names))

	for _, name := range names {
		_, err := p.Get(t.Context(), name)
		require.NoError(t, err, name)
	}

	for name := range Seed() {
		if slices.Contains(names, name) {
			continue
		}

		_, err := p.Get(t.Context(), name)
		require.Error(t, err, name)
		assert.True(t,
			errors.Is(err, template.ErrInvalidName) || errors.Is(err, template.ErrTemplateNotFound),
			"%s: %v", name, err)
	}
}

// exactCase checks that a name differing from a template's only in case
// is another name.
func exactCase(t *testing.T, p gas.TemplateProvider, readOnly bool) {
	t.Helper()

	for _, name := range []string{"Home.html", "HOME.html", "Pages/about.html", "A/b.html"} {
		_, err := p.Get(t.Context(), name)
		require.ErrorIs(t, err, template.ErrTemplateNotFound, name)
	}

	if readOnly {
		return
	}

	require.NoError(t, p.Register(t.Context(), "Home.html", []byte("upper")))

	b, err := p.Get(t.Context(), "home.html")
	require.NoError(t, err)
	assert.Equal(t, "home", string(b))

	b, err = p.Get(t.Context(), "Home.html")
	require.NoError(t, err)
	assert.Equal(t, "upper", string(b))

	names, err := p.List(t.Context())
	require.NoError(t, err)
	assert.Contains(t, names, "Home.html")
	assert.Contains(t, names, "home.html")
}

// getCopies checks that changing what Get returned leaves the store as it
// was.
func getCopies(t *testing.T, p gas.TemplateProvider) {
	t.Helper()

	b, err := p.Get(t.Context(), "home.html")
	require.NoError(t, err)

	b[0] = 'X'

	again, err := p.Get(t.Context(), "home.html")
	require.NoError(t, err)
	assert.Equal(t, "home", string(again))
}

// emptyStore checks that a store with nothing lists an empty, non-nil
// list.
func emptyStore(t *testing.T, p gas.TemplateProvider) {
	t.Helper()

	names, err := p.List(t.Context())
	require.NoError(t, err)
	assert.NotNil(t, names)
	assert.Empty(t, names)
}

// readOnly checks the writes of a read-only store.
func readOnly(t *testing.T, p gas.TemplateProvider) {
	t.Helper()

	err := p.Register(t.Context(), "x.html", []byte("x"))
	require.ErrorIs(t, err, template.ErrReadOnly)
	require.EqualError(t, err, "template: read-only store")

	err = p.RegisterFS(t.Context(), Seed())
	require.ErrorIs(t, err, template.ErrReadOnly)
}

// register checks Register: it replaces, copies what it is given and
// stores nil as empty.
func register(t *testing.T, p gas.TemplateProvider) {
	t.Helper()

	content := []byte("new")
	require.NoError(t, p.Register(t.Context(), "home.html", content))

	content[0] = 'X'

	b, err := p.Get(t.Context(), "home.html")
	require.NoError(t, err)
	assert.Equal(t, "new", string(b))

	require.NoError(t, p.Register(t.Context(), "nil.txt", nil))

	b, err = p.Get(t.Context(), "nil.txt")
	require.NoError(t, err)
	assert.NotNil(t, b)
	assert.Empty(t, b)

	require.NoError(t, p.Register(t.Context(), "deep/er/x.html", []byte("deep")))

	names, err := p.List(t.Context())
	require.NoError(t, err)
	assert.Equal(
		t,
		slices.Sorted(slices.Values(append(slices.Clone(Listed), "deep/er/x.html", "nil.txt"))),
		names,
	)
}

// registerFS checks that RegisterFS registers the templates of a file
// system, and refuses a nil one and a canceled context.
func registerFS(t *testing.T, p gas.TemplateProvider) {
	t.Helper()

	err := p.RegisterFS(t.Context(), nil)
	require.ErrorIs(t, err, template.ErrNilFS)
	require.EqualError(t, err, "template: file system is nil")

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err = p.RegisterFS(ctx, Seed())
	require.ErrorIs(t, err, context.Canceled)
	require.EqualError(t, err, "template: register files: context canceled")

	names, err := p.List(t.Context())
	require.NoError(t, err)
	assert.Empty(t, names)

	require.NoError(t, p.RegisterFS(t.Context(), Seed()))

	names, err = p.List(t.Context())
	require.NoError(t, err)
	assert.Equal(t, Listed, names)
}

// registerFSAllOrNothing checks that a file RegisterFS cannot read, or a
// name it refuses, registers nothing.
func registerFSAllOrNothing(t *testing.T, p gas.TemplateProvider) {
	t.Helper()

	err := p.RegisterFS(t.Context(), failingFS{FS: Seed(), fail: "pages/about.html"})
	require.ErrorIs(t, err, template.ErrRead)
	require.EqualError(t, err, `template: read failed: "pages/about.html": permission denied`)

	err = p.RegisterFS(t.Context(), fstest.MapFS{
		"a.html":    {Data: []byte("a")},
		"b\\c.html": {Data: []byte("b")},
	})
	require.ErrorIs(t, err, template.ErrInvalidName)
	require.EqualError(t, err, "template: invalid name: holds a backslash")

	names, err := p.List(t.Context())
	require.NoError(t, err)
	assert.Empty(t, names)
}

// concurrent calls the store from several goroutines at once.
func concurrent(t *testing.T, p gas.TemplateProvider, readOnly bool) {
	t.Helper()

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)

	for g := range 8 {
		wg.Go(func() {
			for i := range 20 {
				var err error

				if !readOnly {
					name := fmt.Sprintf("c/%d/%d.html", g, i)

					err = p.Register(context.Background(), name, []byte(name))
				}

				_, getErr := p.Get(context.Background(), "home.html")
				_, listErr := p.List(context.Background())

				mu.Lock()

				errs = append(errs, err, getErr, listErr)
				mu.Unlock()
			}
		})
	}

	wg.Wait()

	for _, err := range errs {
		require.NoError(t, err)
	}
}

// failingFS is a file system that fails to read one file with
// fs.ErrPermission.
type failingFS struct {
	fs.FS

	// fail is the file it fails to read.
	fail string
}

// Open opens name, or fails for the file it fails.
func (f failingFS) Open(name string) (fs.File, error) {
	if name == f.fail {
		return nil, &fs.PathError{Op: "open", Path: "/abs/" + name, Err: fs.ErrPermission}
	}

	return f.FS.Open(name) //nolint:wrapcheck // the test sees the file system's own error.
}

// The modes WriteFiles creates directories and files with.
const (
	// dirMode is a directory's mode.
	dirMode = 0o750
	// fileMode is a file's mode.
	fileMode = 0o600
)

// WriteFiles writes the files of seed under dir, creating directories as
// needed.
func WriteFiles(t *testing.T, dir string, seed fstest.MapFS) {
	t.Helper()

	for name, f := range seed {
		p := filepath.Join(dir, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), dirMode))
		require.NoError(t, os.WriteFile(p, f.Data, fileMode))
	}
}

// DocLines returns the code lines of the doc comments in the Go file at
// path, without the comment marker and the tab, that start with prefix.
func DocLines(t *testing.T, path, prefix string) []string {
	t.Helper()

	b, err := os.ReadFile(path) //nolint:gosec // G304: a package's own doc.go.
	require.NoError(t, err)

	var out []string

	for line := range strings.Lines(string(b)) {
		code, ok := strings.CutPrefix(strings.TrimSuffix(line, "\n"), "//\t")
		if ok && strings.HasPrefix(code, prefix) {
			out = append(out, code)
		}
	}

	return out
}
