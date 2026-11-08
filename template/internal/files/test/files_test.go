package files_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/template"
	"github.com/gasmod/gas/template/internal/files"
)

// html is the default extension set.
var html = []string{".html"}

// errStat is a failure that does not mean absence.
var errStat = errors.New("stat failed")

// Absent tells absence: not found, no directory, a loop and os.Root's
// refusal of an escape; nothing else.
func TestAbsent(t *testing.T) {
	t.Parallel()

	d := t.TempDir()
	require.NoError(t, os.Symlink("/etc/hosts", filepath.Join(d, "abs.html")))

	root, err := os.OpenRoot(d)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	_, escape := root.Stat("abs.html")
	require.Error(t, escape)

	for _, err := range []error{
		fs.ErrNotExist, syscall.ENOTDIR, syscall.ELOOP, escape,
		&fs.PathError{Op: "open", Path: "x", Err: fs.ErrNotExist},
	} {
		assert.True(t, files.Absent(err), "%v", err)
	}

	for _, err := range []error{nil, fs.ErrPermission, errStat, &fs.PathError{Op: "open", Err: errStat}} {
		assert.False(t, files.Absent(err), "%v", err)
	}
}

// Cause drops the path of a *fs.PathError and keeps any other error.
func TestCause(t *testing.T) {
	t.Parallel()

	assert.Equal(
		t,
		fs.ErrPermission,
		files.Cause(&fs.PathError{Op: "open", Path: "/abs", Err: fs.ErrPermission}),
	)
	assert.Equal(t, errStat, files.Cause(errStat))
}

// A symlink, as the file system reports it, is never read nor listed.
func TestSymlink_NotFollowed(t *testing.T) {
	t.Parallel()

	fsys := fstest.MapFS{
		"link.html": {Data: []byte("target"), Mode: fs.ModeSymlink},
		"page.html": {Data: []byte("page")},
	}

	ok, err := files.Find(fsys, "link.html")
	require.NoError(t, err)
	assert.False(t, ok)

	_, err = files.Read(fsys, "link.html", template.DefaultMaxSize)
	require.ErrorIs(t, err, template.ErrTemplateNotFound)

	names, err := files.List(t.Context(), fsys, html)
	require.NoError(t, err)
	assert.Equal(t, []string{"page.html"}, names)
}

// vanishingFS lists `gone.html` but cannot open it, as when the file is
// removed between the lookup and the read.
type vanishingFS struct{ fs.FS }

// Open fails for gone.html.
func (v vanishingFS) Open(name string) (fs.File, error) {
	if name == "gone.html" {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}

	return v.FS.Open(name)
}

// A file that goes away between the lookup and the read is not found.
func TestRead_Vanished(t *testing.T) {
	t.Parallel()

	fsys := vanishingFS{fstest.MapFS{"gone.html": {Data: []byte("x")}}}

	_, err := files.Read(fsys, "gone.html", template.DefaultMaxSize)
	require.ErrorIs(t, err, template.ErrTemplateNotFound)
}

// Read returns empty content as an empty, non-nil slice, even when the
// file system returns nil.
func TestRead_NilContent(t *testing.T) {
	t.Parallel()

	b, err := files.Read(fstest.MapFS{"a.html": {}}, "a.html", template.DefaultMaxSize)
	require.NoError(t, err)
	assert.NotNil(t, b)
	assert.Empty(t, b)
}

// A name through a file, or a directory where a file is asked for, is not
// found.
func TestFind_NotAFile(t *testing.T) {
	t.Parallel()

	fsys := fstest.MapFS{"a.html": {}, "d.html/x.html": {}}

	for _, name := range []string{"a.html/x.html", "d.html"} {
		ok, err := files.Find(fsys, name)
		require.NoError(t, err, name)
		assert.False(t, ok, name)
	}
}

// ReadAll skips symlinks and files of other extensions, sorts by name, and
// reports a walk failure as ErrRead.
func TestReadAll(t *testing.T) {
	t.Parallel()

	fsys := fstest.MapFS{
		"b.html":    {Data: []byte("b")},
		"a/x.html":  {Data: []byte("x")},
		"a.html":    {Data: []byte("a")},
		"link.html": {Data: []byte("l"), Mode: fs.ModeSymlink},
		"a.txt":     {Data: []byte("t")},
	}

	all, err := files.ReadAll(t.Context(), fsys, html, template.DefaultMaxSize)
	require.NoError(t, err)

	names := make([]string, 0, len(all))

	for _, f := range all {
		names = append(names, f.Name)
	}

	assert.Equal(t, []string{"a.html", "a/x.html", "b.html"}, names)

	_, err = files.ReadAll(context.Background(), denyDir{fsys}, html, template.DefaultMaxSize)
	require.ErrorIs(t, err, template.ErrRead)
	require.EqualError(t, err, `template: read failed: "a": permission denied`)
}

// denyDir is a file system whose directory `a` cannot be opened.
type denyDir struct{ fs.FS }

// Open fails for a.
func (d denyDir) Open(name string) (fs.File, error) {
	if name == "a" {
		return nil, &fs.PathError{Op: "open", Path: "/abs/a", Err: fs.ErrPermission}
	}

	return d.FS.Open(name)
}
