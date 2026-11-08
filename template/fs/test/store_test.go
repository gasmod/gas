package fs_test

import (
	"context"
	iofs "io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/template"
	templatefs "github.com/gasmod/gas/template/fs"
	"github.com/gasmod/gas/template/internal/conformance"
)

// newStore returns a store over fsys with the conformance extensions.
func newStore(t *testing.T, fsys iofs.FS) *templatefs.Store {
	t.Helper()

	s, err := templatefs.NewStore(fsys, templatefs.WithExtensions(conformance.Extensions...))()
	require.NoError(t, err)

	return s
}

// The fs store keeps the contract of the template package over an
// fstest.MapFS, over os.DirFS and over os.Root's FS, the last two on the
// machine's file system, which on macOS ignores case.
func TestStore_Conformance(t *testing.T) {
	t.Parallel()

	t.Run("MapFS", func(t *testing.T) {
		t.Parallel()

		conformance.Run(t, conformance.Subject{
			ReadOnly: true,
			New: func(t *testing.T, seed fstest.MapFS) gas.TemplateProvider {
				t.Helper()

				return newStore(t, seed)
			},
		})
	})

	t.Run("DirFS", func(t *testing.T) {
		t.Parallel()

		conformance.Run(t, conformance.Subject{
			ReadOnly: true,
			New: func(t *testing.T, seed fstest.MapFS) gas.TemplateProvider {
				t.Helper()

				dir := t.TempDir()
				conformance.WriteFiles(t, dir, seed)

				return newStore(t, os.DirFS(dir))
			},
		})
	})

	t.Run("RootFS", func(t *testing.T) {
		t.Parallel()

		conformance.Run(t, conformance.Subject{
			ReadOnly: true,
			New: func(t *testing.T, seed fstest.MapFS) gas.TemplateProvider {
				t.Helper()

				dir := t.TempDir()
				conformance.WriteFiles(t, dir, seed)

				root, err := os.OpenRoot(dir)
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, root.Close()) })

				return newStore(t, root.FS())
			},
		})
	})
}

// The constructor refuses a nil file system, a typed nil included, and a
// bad extension set.
func TestNewStore_Errors(t *testing.T) {
	t.Parallel()

	_, err := templatefs.NewStore(nil)()
	require.ErrorIs(t, err, template.ErrNilFS)
	require.EqualError(t, err, "template: file system is nil")

	_, err = templatefs.NewStore(fstest.MapFS(nil))()
	require.ErrorIs(t, err, template.ErrNilFS)

	_, err = templatefs.NewStore(fstest.MapFS{}, templatefs.WithExtensions("html"))()
	require.ErrorIs(t, err, template.ErrInvalidExtension)
	require.EqualError(t, err,
		"template: invalid extension: extension 1 must be a dot and one or more characters, "+
			"none a slash, backslash or NUL")
}

// A store accepts `.html` unless WithExtensions gives it others.
func TestNewStore_DefaultExtension(t *testing.T) {
	t.Parallel()

	s, err := templatefs.NewStore(fstest.MapFS{
		"a.html": {Data: []byte("a")},
		"a.txt":  {Data: []byte("a")},
	})()
	require.NoError(t, err)

	names, err := s.List(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []string{"a.html"}, names)

	_, err = s.Get(t.Context(), "a.txt")
	require.ErrorIs(t, err, template.ErrInvalidName)
}

// symlinkTree writes a directory with a template, an outside file, and
// symlinks of every kind to them, and returns the directory.
func symlinkTree(t *testing.T) string {
	t.Helper()

	base := t.TempDir()
	dir := filepath.Join(base, "templates")
	conformance.WriteFiles(t, dir, fstest.MapFS{
		"page.html":  {Data: []byte("page")},
		"sub/a.html": {Data: []byte("a")},
	})
	conformance.WriteFiles(t, filepath.Join(base, "outside"), fstest.MapFS{
		"passwd.html": {Data: []byte("OUTSIDE CONTENT")},
	})

	for link, target := range map[string]string{
		"rel.html":      "page.html",
		"abs.html":      filepath.Join(dir, "page.html"),
		"evil.html":     "../outside/passwd.html",
		"dangling.html": "nope.html",
		"linkdir":       "sub",
		"outdir":        "../outside",
		"loop":          ".",
	} {
		require.NoError(t, os.Symlink(target, filepath.Join(dir, link)))
	}

	return dir
}

// The store never follows a symlink that the file system reports, so a
// symlink out of the directory is not read even over os.DirFS, which
// follows it (survey P3b), and List and Get agree on every symlink.
func TestStore_Symlinks(t *testing.T) {
	t.Parallel()

	dir := symlinkTree(t)
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	for name, fsys := range map[string]iofs.FS{"DirFS": os.DirFS(dir), "RootFS": root.FS()} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s := newStore(t, fsys)

			names, err := s.List(t.Context())
			require.NoError(t, err)
			assert.Equal(t, []string{"page.html", "sub/a.html"}, names)

			for _, n := range []string{
				"rel.html", "abs.html", "evil.html", "dangling.html", "linkdir/a.html",
				"outdir/passwd.html", "loop/page.html",
			} {
				_, err := s.Get(t.Context(), n)
				require.ErrorIs(t, err, template.ErrTemplateNotFound, n)
			}
		})
	}
}

// skipAsRoot skips a test that relies on file permissions when it runs as
// root, which reads every file.
func skipAsRoot(t *testing.T) {
	t.Helper()

	if os.Geteuid() == 0 {
		t.Skip("root ignores file permissions")
	}
}

// A file that is there but cannot be read is listed, and Get returns an
// error wrapping ErrRead that names it and not the directory's path; an
// unreadable directory fails Get under it with ErrRead and List with
// ErrList.
func TestStore_ReadErrors(t *testing.T) {
	t.Parallel()
	skipAsRoot(t)

	dir := t.TempDir()
	conformance.WriteFiles(t, dir, fstest.MapFS{
		"locked.html":      {Data: []byte("x")},
		"shut/inside.html": {Data: []byte("x")},
		"zz/readable.html": {Data: []byte("x")},
	})
	require.NoError(t, os.Chmod(filepath.Join(dir, "locked.html"), 0))
	require.NoError(t, os.Chmod(filepath.Join(dir, "shut"), 0))
	t.Cleanup(func() { unlock(t, filepath.Join(dir, "shut")) })

	s := newStore(t, os.DirFS(dir))

	_, err := s.Get(t.Context(), "locked.html")
	require.ErrorIs(t, err, template.ErrRead)
	require.EqualError(t, err, `template: read failed: "locked.html": permission denied`)

	_, err = s.Get(t.Context(), "shut/inside.html")
	require.ErrorIs(t, err, template.ErrRead)
	require.EqualError(t, err, `template: read failed: "shut/inside.html": permission denied`)

	_, err = s.List(t.Context())
	require.ErrorIs(t, err, template.ErrList)
	require.EqualError(t, err, `template: list failed: "shut": permission denied`)
	assert.NotContains(t, err.Error(), dir)
}

// List checks the context and stops when it ends.
func TestStore_ListCanceled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := newStore(t, conformance.Seed()).List(ctx)
	require.ErrorIs(t, err, template.ErrList)
	require.ErrorIs(t, err, context.Canceled)
	require.EqualError(t, err, "template: list failed: context canceled")
}

// Register checks the name before it refuses the write.
func TestStore_RegisterChecksName(t *testing.T) {
	t.Parallel()

	err := newStore(t, fstest.MapFS{}).Register(t.Context(), "../x.html", nil)
	require.ErrorIs(t, err, template.ErrInvalidName)
}

// An fs.FS that cannot read directories cannot be looked up: Get returns
// ErrRead.
func TestStore_NoReadDir(t *testing.T) {
	t.Parallel()

	_, err := newStore(t, openOnly{}).Get(t.Context(), "a.html")
	require.ErrorIs(t, err, template.ErrRead)
}

// openOnly is a file system whose directories cannot be read.
type openOnly struct{}

// Open fails with fs.ErrInvalid.
func (openOnly) Open(name string) (iofs.File, error) {
	return nil, &iofs.PathError{Op: "open", Path: name, Err: iofs.ErrInvalid}
}

// unlock gives the directory at path back its owner's permissions.
func unlock(t *testing.T, path string) {
	t.Helper()

	err := os.Chmod(path, 0o700) //nolint:gosec // G302: a directory needs its search bit.
	require.NoError(t, err)
}
