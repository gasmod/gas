package files_test

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
	"testing/iotest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/template"
	"github.com/gasmod/gas/template/internal/files"
)

// errRead is a read failure.
var errRead = errors.New("read failed")

// ReadLimited reads up to the maximum, refuses more whether the size hint
// tells it or the content does, and returns a reader's failure.
func TestReadLimited(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		content  string
		hint     int64
		maxSize  int64
		tooLarge bool
	}{
		{name: "exact hint", content: "abc", hint: 3, maxSize: 3},
		{name: "low hint grows", content: "abcdef", hint: 1, maxSize: 10},
		{name: "unknown hint", content: "abc", hint: -1, maxSize: 3},
		{name: "empty", content: "", hint: 0, maxSize: 1},
		{name: "hint over", content: "abcd", hint: 4, maxSize: 3, tooLarge: true},
		{name: "content over", content: "abcd", hint: 0, maxSize: 3, tooLarge: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			b, tooLarge, err := files.ReadLimited(
				iotest.OneByteReader(strings.NewReader(tc.content)), tc.hint, tc.maxSize)
			require.NoError(t, err)
			assert.Equal(t, tc.tooLarge, tooLarge)

			if !tc.tooLarge {
				assert.NotNil(t, b)
				assert.Equal(t, tc.content, string(b))
			}
		})
	}

	_, _, err := files.ReadLimited(iotest.ErrReader(errRead), 0, 10)
	require.ErrorIs(t, err, errRead)
}

// Read and ReadAll refuse a file over the maximum size with ErrTooLarge,
// naming it.
func TestRead_TooLarge(t *testing.T) {
	t.Parallel()

	fsys := fstest.MapFS{"a.html": {Data: []byte("abcd")}}

	_, err := files.Read(fsys, "a.html", 3)
	require.ErrorIs(t, err, template.ErrTooLarge)
	require.EqualError(t, err, `template: too large: "a.html": over 3 bytes`)

	_, err = files.ReadAll(t.Context(), fsys, html, 3)
	require.ErrorIs(t, err, template.ErrTooLarge)

	b, err := files.Read(fsys, "a.html", 4)
	require.NoError(t, err)
	assert.Equal(t, "abcd", string(b))
}

// failFS is a file system whose files fail as configured.
type failFS struct {
	fs.FS

	// open fails Open of a name.
	open map[string]error
	// stat fails Stat of an open file of a name.
	stat map[string]error
	// read fails Read of an open file of a name.
	read map[string]error
	// close fails Close of an open file of a name.
	close map[string]error
}

// Open opens name, failing as configured.
func (f failFS) Open(name string) (fs.File, error) {
	err := f.open[name]
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: "/abs/" + name, Err: err}
	}

	file, err := f.FS.Open(name)
	if err != nil {
		return nil, err
	}

	return failFile{File: file, name: name, fs: f}, nil
}

// failFile is a file that fails as its failFS says.
type failFile struct {
	fs.File

	fs   failFS
	name string
}

// Stat fails as configured.
func (f failFile) Stat() (fs.FileInfo, error) {
	err := f.fs.stat[f.name]
	if err != nil {
		return nil, err
	}

	return f.File.Stat()
}

// Read fails as configured.
func (f failFile) Read(b []byte) (int, error) {
	err := f.fs.read[f.name]
	if err != nil {
		return 0, err
	}

	return f.File.Read(b)
}

// Close fails as configured, after closing.
func (f failFile) Close() error {
	err := f.File.Close()

	cerr := f.fs.close[f.name]
	if cerr != nil {
		return cerr
	}

	return err
}

// ReadDir reads the directory's entries.
func (f failFile) ReadDir(n int) ([]fs.DirEntry, error) {
	err := f.fs.read[f.name]
	if err != nil {
		return nil, err
	}

	return f.File.(fs.ReadDirFile).ReadDir(n)
}

// plainDirs is a file system whose open directories cannot read their
// entries, which only its own ReadDir reads.
type plainDirs struct{ fstest.MapFS }

// Open opens name as a plainFile.
func (p plainDirs) Open(name string) (fs.File, error) {
	f, err := p.MapFS.Open(name)
	if err != nil {
		return nil, err
	}

	return plainFile{f}, nil
}

// plainFile hides a directory's ReadDir.
type plainFile struct{ fs.File }

// The failures of opening, statting, reading and closing a file are
// ErrRead naming the template, without the file system's path.
func TestRead_Failures(t *testing.T) {
	t.Parallel()

	base := fstest.MapFS{"a.html": {Data: []byte("a")}, "d/b.html": {Data: []byte("b")}}

	for _, tc := range []struct {
		name string
		fsys failFS
		want string
	}{
		{
			name: "open",
			fsys: failFS{FS: base, open: map[string]error{"a.html": fs.ErrPermission}},
			want: `template: read failed: "a.html": permission denied`,
		},
		{
			name: "stat",
			fsys: failFS{FS: base, stat: map[string]error{"a.html": errRead}},
			want: `template: read failed: "a.html": read failed`,
		},
		{
			name: "read",
			fsys: failFS{FS: base, read: map[string]error{"a.html": errRead}},
			want: `template: read failed: "a.html": read failed`,
		},
		{
			name: "close",
			fsys: failFS{FS: base, close: map[string]error{"a.html": errRead}},
			want: `template: read failed: "a.html": read failed`,
		},
		{
			name: "directory",
			fsys: failFS{FS: base, read: map[string]error{".": errRead}},
			want: `template: read failed: "a.html": read failed`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := files.Read(tc.fsys, "a.html", template.DefaultMaxSize)
			require.ErrorIs(t, err, template.ErrRead)
			require.EqualError(t, err, tc.want)
		})
	}
}

// ReadAll's failures to open or read a file are ErrRead naming it.
func TestReadAll_Failures(t *testing.T) {
	t.Parallel()

	base := fstest.MapFS{"a.html": {Data: []byte("a")}, "d/b.html": {Data: []byte("b")}}

	_, err := files.ReadAll(
		t.Context(),
		failFS{
			FS:   base,
			open: map[string]error{"d/b.html": fs.ErrPermission},
		},
		html,
		template.DefaultMaxSize,
	)
	require.EqualError(t, err, `template: read failed: "d/b.html": permission denied`)

	_, err = files.ReadAll(
		t.Context(),
		failFS{
			FS:   base,
			read: map[string]error{"d/b.html": errRead},
		},
		html,
		template.DefaultMaxSize,
	)
	require.EqualError(t, err, `template: read failed: "d/b.html": read failed`)
}

// Entry finds an entry past the first chunk, stops at it, reads a
// directory that is no fs.ReadDirFile through fs.ReadDir, and tells a
// missing directory or entry by nil.
func TestEntry(t *testing.T) {
	t.Parallel()

	fsys := fstest.MapFS{}
	for i := range 300 {
		fsys[fmt.Sprintf("d/f%03d.html", i)] = &fstest.MapFile{}
	}

	for _, sys := range []fs.FS{fsys, plainDirs{fsys}} {
		e, err := files.Entry(sys, "d", "f299.html")
		require.NoError(t, err)
		require.NotNil(t, e)
		assert.Equal(t, "f299.html", e.Name())

		e, err = files.Entry(sys, "d", "F299.html")
		require.NoError(t, err)
		assert.Nil(t, e)

		e, err = files.Entry(sys, "nope", "f299.html")
		require.NoError(t, err)
		assert.Nil(t, e)
	}

	_, err := files.Entry(failFS{FS: fsys, open: map[string]error{"d": fs.ErrPermission}}, "d", "x")
	require.ErrorIs(t, err, fs.ErrPermission)

	_, err = files.Entry(failFS{FS: fsys, read: map[string]error{"d": errRead}}, "d", "x")
	require.ErrorIs(t, err, errRead)

	_, err = files.Entry(failFS{FS: fsys, close: map[string]error{"d": errRead}}, "d", "x")
	require.ErrorIs(t, err, errRead)
}

// Find tells a name whose element differs in case, or that is not there,
// as absent, and a directory it cannot read as an error.
func TestFind(t *testing.T) {
	t.Parallel()

	fsys := fstest.MapFS{"d/a.html": {}}

	for name, want := range map[string]bool{"d/a.html": true, "D/a.html": false, "d/A.html": false} {
		ok, err := files.Find(fsys, name)
		require.NoError(t, err)
		assert.Equal(t, want, ok, name)
	}

	_, err := files.Find(failFS{FS: fsys, read: map[string]error{"d": errRead}}, "d/a.html")
	require.ErrorIs(t, err, errRead)
}
