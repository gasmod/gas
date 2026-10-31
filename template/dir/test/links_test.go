package dir_test

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/template"
	"github.com/gasmod/gas/template/dir"
	"github.com/gasmod/gas/template/internal/conformance"
)

// A symlink is listed and read only when every target on its way ends in
// one of the store's extensions and no target passes through a symlinked
// directory, so a `.html` name cannot read `secret.env` through a link.
func TestStore_LinkTargets(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	d := filepath.Join(base, "templates")
	conformance.WriteFiles(t, d, fstest.MapFS{
		"secret.env":      {Data: []byte("SECRET=1")},
		"pages/home.html": {Data: []byte("home")},
		"notes.txt":       {Data: []byte("notes")},
	})

	for link, target := range map[string]string{
		"envalias.html": "secret.env",
		"chain1.html":   "chain2.html",
		"chain2.html":   "pages/home.html",
		"viaenv.html":   "hop.env",
		"hop.env":       "pages/home.html",
		"dirlink":       "pages",
		"viadir.html":   "dirlink/home.html",
		"back.html":     "../templates/pages/home.html",
		"slash.html":    `pages\home.html`,
		"text.html":     "notes.txt",
		"pages/up.html": "../chain1.html",
	} {
		require.NoError(t, os.Symlink(target, filepath.Join(d, link)))
	}

	s := openAt(t, d)

	names, err := s.List(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []string{
		"chain1.html", "chain2.html", "notes.txt", "pages/home.html", "pages/up.html", "text.html",
	}, names)

	for _, n := range names {
		_, getErr := s.Get(t.Context(), n)
		require.NoError(t, getErr, n)
	}

	b, err := s.Get(t.Context(), "pages/up.html")
	require.NoError(t, err)
	assert.Equal(t, "home", string(b))

	for _, n := range []string{"envalias.html", "viaenv.html", "viadir.html", "back.html", "slash.html"} {
		_, err := s.Get(t.Context(), n)
		require.ErrorIs(t, err, template.ErrTemplateNotFound, n)
	}
}

// A long chain of symlinks is taken as a loop: neither listed nor read.
func TestStore_LinkChainTooLong(t *testing.T) {
	t.Parallel()

	d := t.TempDir()
	conformance.WriteFiles(t, d, fstest.MapFS{"end.html": {Data: []byte("end")}})

	prev := "end.html"

	for i := range 41 {
		link := "l" + string(rune('a'+i/26)) + string(rune('a'+i%26)) + ".html"
		require.NoError(t, os.Symlink(prev, filepath.Join(d, link)))

		prev = link
	}

	s := openAt(t, d)

	_, err := s.Get(t.Context(), prev)
	require.ErrorIs(t, err, template.ErrTemplateNotFound)

	names, err := s.List(t.Context())
	require.NoError(t, err)
	assert.NotContains(t, names, prev)
	assert.Contains(t, names, "laa.html")
}

// A name is found only as the directory spells it, element by element,
// and a symlink's name too, on a file system that ignores case or
// normalization as on one that does not.
func TestStore_ExactCase(t *testing.T) {
	t.Parallel()

	nfd := "café.html"
	s, d := open(t, fstest.MapFS{
		"page.html":  {Data: []byte("page")},
		"sub/a.html": {Data: []byte("a")},
		nfd:          {Data: []byte("nfd")},
	})
	require.NoError(t, os.Symlink("page.html", filepath.Join(d, "rel.html")))

	for _, n := range []string{"page.html", "sub/a.html", nfd, "rel.html"} {
		_, err := s.Get(t.Context(), n)
		require.NoError(t, err, n)
	}

	for _, n := range []string{"Page.html", "PAGE.html", "Sub/a.html", "sub/A.html", "café.html", "Rel.html"} {
		_, err := s.Get(t.Context(), n)
		require.ErrorIs(t, err, template.ErrTemplateNotFound, n)
	}

	// A rename that changes only the case shows at once.
	require.NoError(t, os.Rename(filepath.Join(d, "page.html"), filepath.Join(d, "Page.html")))

	_, err := s.Get(t.Context(), "page.html")
	require.ErrorIs(t, err, template.ErrTemplateNotFound)

	b, err := s.Get(t.Context(), "Page.html")
	require.NoError(t, err)
	assert.Equal(t, "page", string(b))
}

// Get returns the context's error, naming the template, before it reads.
func TestStore_GetCanceled(t *testing.T) {
	t.Parallel()

	s, _ := open(t, fstest.MapFS{"page.html": {Data: []byte("page")}})

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := s.Get(ctx, "page.html")
	require.ErrorIs(t, err, context.Canceled)
	require.EqualError(t, err, `template: get "page.html": context canceled`)
}

// A file over the maximum size is ErrTooLarge and is not read; Register
// and RegisterFS refuse such content and register nothing; a maximum that
// is not positive is refused by the constructor.
func TestStore_MaxSize(t *testing.T) {
	t.Parallel()

	d := t.TempDir()
	conformance.WriteFiles(t, d, fstest.MapFS{
		"big.html": {Data: []byte("abcd")},
		"ok.html":  {Data: []byte("abc")},
	})

	s, err := dir.NewStore(d, dir.WithMaxSize(3))()
	require.NoError(t, err)
	require.NoError(t, s.Init(t.Context()))
	t.Cleanup(func() { require.NoError(t, s.Close(context.Background())) })

	_, err = s.Get(t.Context(), "big.html")
	require.ErrorIs(t, err, template.ErrTooLarge)
	require.EqualError(t, err, `template: too large: "big.html": over 3 bytes`)

	b, err := s.Get(t.Context(), "ok.html")
	require.NoError(t, err)
	assert.Equal(t, "abc", string(b))

	require.ErrorIs(t, s.Register(t.Context(), "r.html", []byte("abcd")), template.ErrTooLarge)

	err = s.RegisterFS(t.Context(), fstest.MapFS{
		"a.html": {Data: []byte("a")},
		"b.html": {Data: []byte("abcd")},
	})
	require.ErrorIs(t, err, template.ErrTooLarge)

	_, err = s.Get(t.Context(), "a.html")
	require.ErrorIs(t, err, template.ErrTemplateNotFound)

	for _, n := range []int64{0, -1} {
		_, err = dir.NewStore(d, dir.WithMaxSize(n))()
		require.ErrorIs(t, err, template.ErrInvalidMaxSize)
		require.EqualError(t, err, "template: invalid max size: must be at least 1 byte")
	}
}

// gateFS is a file system whose Open waits until its gate closes.
type gateFS struct {
	fstest.MapFS

	// entered is closed once Open is called.
	entered chan struct{}
	// gate is closed to let Open go on.
	gate chan struct{}
}

// Open waits for the gate, then opens name.
func (g gateFS) Open(name string) (fs.File, error) {
	select {
	case <-g.entered:
	default:
		close(g.entered)
	}

	<-g.gate

	return g.MapFS.Open(name)
}

// RegisterFS reads the caller's file system before it takes the store's
// lock, so Close does not wait for a slow one, and it then registers
// nothing on the closed store.
func TestStore_RegisterFSDoesNotHoldClose(t *testing.T) {
	t.Parallel()

	s, _ := open(t, fstest.MapFS{})
	g := gateFS{
		MapFS:   fstest.MapFS{"a.html": {Data: []byte("a")}},
		entered: make(chan struct{}),
		gate:    make(chan struct{}),
	}

	done := make(chan error, 1)

	go func() { done <- s.RegisterFS(context.Background(), g) }()

	<-g.entered

	closed := make(chan error, 1)

	go func() { closed <- s.Close(context.Background()) }()

	select {
	case err := <-closed:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Close waited for RegisterFS's reads")
	}

	close(g.gate)
	require.ErrorIs(t, <-done, template.ErrClosed)
}

// List stops at a canceled context with ErrList.
func TestStore_ListCanceled(t *testing.T) {
	t.Parallel()

	s, _ := open(t, fstest.MapFS{"page.html": {Data: []byte("page")}})

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := s.List(ctx)
	require.ErrorIs(t, err, template.ErrList)
	require.ErrorIs(t, err, context.Canceled)
}

// A symlink whose target passes through a directory that cannot be
// searched is an error, ErrList from List and ErrRead from Get, as a
// file there would be, naming no absolute path.
func TestStore_LinkThroughLockedDir(t *testing.T) {
	t.Parallel()
	skipAsRoot(t)

	s, d := open(t, fstest.MapFS{"locked/sub/a.html": {Data: []byte("a")}})
	require.NoError(t, os.Symlink("locked/sub/a.html", filepath.Join(d, "a.html")))
	require.NoError(t, os.Chmod(filepath.Join(d, "locked"), 0))
	t.Cleanup(func() { unlock(t, filepath.Join(d, "locked")) })

	// The walk meets the link, `a.html`, before the directory.
	_, err := s.List(t.Context())
	require.ErrorIs(t, err, template.ErrList)
	require.EqualError(t, err, `template: list failed: "a.html": permission denied`)

	_, err = s.Get(t.Context(), "a.html")
	require.ErrorIs(t, err, template.ErrRead)
	require.EqualError(t, err, `template: read failed: "a.html": permission denied`)
}

// List fails with ErrList, never an empty list and nil, when the
// directory was removed, moved away or replaced after Init, or cannot be
// read: the survey's first bug was a List that found nothing and said
// nothing, so gas/ui built no pages. A directory put back at the path is
// not the one opened, and stays an error until Init runs again.
func TestStore_ListRootGone(t *testing.T) {
	t.Parallel()

	for name, gone := range map[string]func(t *testing.T, d string){
		"removed": func(t *testing.T, d string) {
			t.Helper()
			require.NoError(t, os.RemoveAll(d))
		},
		"moved": func(t *testing.T, d string) {
			t.Helper()
			require.NoError(t, os.Rename(d, d+".old"))
		},
		"replaced": func(t *testing.T, d string) {
			t.Helper()
			require.NoError(t, os.Rename(d, d+".old"))
			require.NoError(t, os.Mkdir(d, 0o700))
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			d := filepath.Join(t.TempDir(), "templates")
			conformance.WriteFiles(t, d, fstest.MapFS{"page.html": {Data: []byte("page")}})

			s := openAt(t, d)

			gone(t, d)

			names, err := s.List(t.Context())
			require.ErrorIs(t, err, template.ErrList)
			require.EqualError(t, err, "template: list failed: directory removed or moved")
			assert.Nil(t, names)
			assert.NotContains(t, err.Error(), d)
		})
	}
}

// List of a root that cannot be read any more fails with ErrList.
func TestStore_ListRootUnreadable(t *testing.T) {
	t.Parallel()
	skipAsRoot(t)

	s, d := open(t, fstest.MapFS{"page.html": {Data: []byte("page")}})
	require.NoError(t, os.Chmod(d, 0))
	t.Cleanup(func() { unlock(t, d) })

	names, err := s.List(t.Context())
	require.ErrorIs(t, err, template.ErrList)
	assert.Nil(t, names)
	assert.NotContains(t, err.Error(), d)
}
