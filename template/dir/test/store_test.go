package dir_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/template"
	"github.com/gasmod/gas/template/dir"
	"github.com/gasmod/gas/template/internal/conformance"
)

// The dir store keeps the contract of the template package, on the
// machine's file system, which on macOS ignores case.
func TestStore_Conformance(t *testing.T) {
	t.Parallel()

	conformance.Run(t, conformance.Subject{
		New: func(t *testing.T, seed fstest.MapFS) gas.TemplateProvider {
			t.Helper()

			s, _ := open(t, seed)

			return s
		},
	})
}

// The constructor refuses an empty path and a bad extension set, and
// opens nothing.
func TestNewStore_Errors(t *testing.T) {
	t.Parallel()

	_, err := dir.NewStore("")()
	require.ErrorIs(t, err, dir.ErrEmptyDir)
	require.EqualError(t, err, "template: directory path is empty")

	_, err = dir.NewStore(t.TempDir(), dir.WithExtensions("/x"))()
	require.ErrorIs(t, err, template.ErrInvalidExtension)

	_, err = dir.NewStore(filepath.Join(t.TempDir(), "missing"))()
	require.NoError(t, err)
}

// Before Init every method but Init and Close returns ErrNotInitialized;
// after Close, ErrClosed; Init on an open store ErrAlreadyInitialized; and
// Init after Close opens the directory again, without the templates
// registered before.
func TestStore_Lifecycle(t *testing.T) {
	t.Parallel()

	d := t.TempDir()
	conformance.WriteFiles(t, d, fstest.MapFS{"a.html": {Data: []byte("a")}})

	s, err := dir.NewStore(d)()
	require.NoError(t, err)
	assert.Equal(t, dir.ServiceName, s.Name())
	assert.Equal(t, "gas/template/dir", s.Name())

	ctx := t.Context()
	require.NoError(t, s.Close(ctx))
	requireState(t, s, template.ErrNotInitialized, "template: store not initialized")

	require.NoError(t, s.Init(ctx))

	err = s.Init(ctx)
	require.ErrorIs(t, err, template.ErrAlreadyInitialized)
	require.EqualError(t, err, "template: store already initialized")

	require.NoError(t, s.Register(ctx, "b.html", []byte("b")))
	require.NoError(t, s.Close(ctx))
	require.NoError(t, s.Close(ctx))
	requireState(t, s, template.ErrClosed, "template: store closed")

	require.NoError(t, s.Init(ctx))

	names, err := s.List(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"a.html"}, names)
	require.NoError(t, s.Close(ctx))
}

// requireState checks that every method but Init and Close returns want.
func requireState(t *testing.T, s *dir.Store, want error, text string) {
	t.Helper()

	ctx := t.Context()

	_, err := s.Get(ctx, "a.html")
	require.ErrorIs(t, err, want)
	require.EqualError(t, err, text)

	_, err = s.List(ctx)
	require.ErrorIs(t, err, want)

	require.ErrorIs(t, s.Register(ctx, "a.html", nil), want)
	require.ErrorIs(t, s.RegisterFS(ctx, fstest.MapFS{}), want)
}

// Init fails with ErrDirUnavailable, naming the reason and not the path,
// for a directory that is missing or a file, and can be tried again once
// the directory exists; the old store cached the first failure for good
// (survey P5).
func TestStore_InitUnavailable(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	d := filepath.Join(base, "later")

	s, err := dir.NewStore(d)()
	require.NoError(t, err)

	err = s.Init(t.Context())
	require.ErrorIs(t, err, dir.ErrDirUnavailable)
	require.ErrorIs(t, err, os.ErrNotExist)
	require.EqualError(t, err, "template: directory unavailable: no such file or directory")
	assert.NotContains(t, err.Error(), base)

	_, err = s.Get(t.Context(), "a.html")
	require.ErrorIs(t, err, template.ErrNotInitialized)

	conformance.WriteFiles(t, d, fstest.MapFS{"a.html": {Data: []byte("a")}})
	require.NoError(t, s.Init(t.Context()))

	b, err := s.Get(t.Context(), "a.html")
	require.NoError(t, err)
	assert.Equal(t, "a", string(b))
	require.NoError(t, s.Close(t.Context()))

	f, err := dir.NewStore(filepath.Join(d, "a.html"))()
	require.NoError(t, err)

	err = f.Init(t.Context())
	require.ErrorIs(t, err, dir.ErrDirUnavailable)
	require.EqualError(t, err, "template: directory unavailable: not a directory")
}

// Get applies the extension filter, so a file under the root without one
// of the store's extensions is not readable (survey P3).
func TestStore_GetExtensionFilter(t *testing.T) {
	t.Parallel()

	s, _ := open(t, fstest.MapFS{"secret.env": {Data: []byte("DB_PASSWORD=hunter2")}})

	_, err := s.Get(t.Context(), "secret.env")
	require.ErrorIs(t, err, template.ErrInvalidName)
	require.EqualError(t, err, "template: invalid name: extension not allowed")
}

// A registered template hides the file of its name and is listed once;
// files changed on disk show at once.
func TestStore_Overlay(t *testing.T) {
	t.Parallel()

	s, d := open(t, fstest.MapFS{"a.html": {Data: []byte("disk")}})
	ctx := t.Context()

	require.NoError(t, s.Register(ctx, "a.html", []byte("overlay")))

	b, err := s.Get(ctx, "a.html")
	require.NoError(t, err)
	assert.Equal(t, "overlay", string(b))

	conformance.WriteFiles(t, d, fstest.MapFS{"new.html": {Data: []byte("new")}})

	names, err := s.List(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"a.html", "new.html"}, names)

	require.NoError(t, os.Remove(filepath.Join(d, "new.html")))

	_, err = s.Get(ctx, "new.html")
	require.ErrorIs(t, err, template.ErrTemplateNotFound)
}

// symlinkTree writes a directory with templates, an outside file, and
// symlinks of every kind, and returns the directory.
func symlinkTree(t *testing.T) string {
	t.Helper()

	base := t.TempDir()
	d := filepath.Join(base, "templates")
	conformance.WriteFiles(t, d, fstest.MapFS{
		"page.html":  {Data: []byte("page")},
		"sub/a.html": {Data: []byte("a")},
	})
	conformance.WriteFiles(t, filepath.Join(base, "outside"), fstest.MapFS{
		"passwd.html": {Data: []byte("OUTSIDE CONTENT")},
	})

	for link, target := range map[string]string{
		"rel.html":      "page.html",
		"deep.html":     "sub/a.html",
		"abs.html":      filepath.Join(d, "page.html"),
		"evil.html":     "../outside/passwd.html",
		"dangling.html": "nope.html",
		"self.html":     "self.html",
		"todir.html":    "sub",
		"linkdir":       "sub",
		"outdir":        "../outside",
		"loop":          ".",
	} {
		require.NoError(t, os.Symlink(target, filepath.Join(d, link)))
	}

	return d
}

// A symlink to a file inside the root is listed and read; one that leads
// out, is absolute, dangles, loops or names a directory is neither; a
// symlinked directory is neither walked nor passed through. So List and
// Get agree (survey P3, P4b, P12).
func TestStore_Symlinks(t *testing.T) {
	t.Parallel()

	s := openAt(t, symlinkTree(t))

	names, err := s.List(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []string{"deep.html", "page.html", "rel.html", "sub/a.html"}, names)

	for _, n := range names {
		_, getErr := s.Get(t.Context(), n)
		require.NoError(t, getErr, n)
	}

	b, err := s.Get(t.Context(), "rel.html")
	require.NoError(t, err)
	assert.Equal(t, "page", string(b))

	for _, n := range []string{
		"abs.html", "evil.html", "dangling.html", "self.html", "todir.html", "linkdir/a.html",
		"outdir/passwd.html", "loop/page.html",
	} {
		_, err := s.Get(t.Context(), n)
		require.ErrorIs(t, err, template.ErrTemplateNotFound, n)
	}
}

// An unreadable directory makes List fail with ErrList, naming the
// directory, instead of returning an empty list and no error as the old
// store did (survey P6). An unreadable file is listed, and Get fails with
// ErrRead.
func TestStore_ReadErrors(t *testing.T) {
	t.Parallel()
	skipAsRoot(t)

	s, d := open(t, fstest.MapFS{
		"a_locked/x.html": {Data: []byte("x")},
		"locked.html":     {Data: []byte("x")},
		"page.html":       {Data: []byte("x")},
	})
	require.NoError(t, os.Chmod(filepath.Join(d, "locked.html"), 0))
	require.NoError(t, os.Chmod(filepath.Join(d, "a_locked"), 0))
	t.Cleanup(func() { unlock(t, filepath.Join(d, "a_locked")) })

	_, err := s.List(t.Context())
	require.ErrorIs(t, err, template.ErrList)
	require.EqualError(t, err, `template: list failed: "a_locked": permission denied`)

	_, err = s.Get(t.Context(), "locked.html")
	require.ErrorIs(t, err, template.ErrRead)
	require.EqualError(t, err, `template: read failed: "locked.html": permission denied`)

	_, err = s.Get(t.Context(), "a_locked/x.html")
	require.ErrorIs(t, err, template.ErrRead)
	assert.NotContains(t, err.Error(), d)

	unlock(t, filepath.Join(d, "a_locked"))

	names, err := s.List(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []string{"a_locked/x.html", "locked.html", "page.html"}, names)
}

// Close waits for the reads in flight, and every read either completes
// or returns ErrClosed.
func TestStore_CloseDuringReads(t *testing.T) {
	t.Parallel()

	s, _ := open(t, conformance.Seed())

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)

	for range 8 {
		wg.Go(func() {
			for range 50 {
				_, getErr := s.Get(context.Background(), "home.html")
				_, listErr := s.List(context.Background())

				mu.Lock()

				errs = append(errs, getErr, listErr)
				mu.Unlock()
			}
		})
	}

	require.NoError(t, s.Close(context.Background()))
	wg.Wait()

	for _, err := range errs {
		if err != nil {
			require.ErrorIs(t, err, template.ErrClosed)
		}
	}
}
