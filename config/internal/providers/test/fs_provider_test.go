package providers_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/config/internal/providers"
	"github.com/gasmod/gas/config/internal/sysfs"
)

// chdirWork makes a fresh directory the working directory for the test and
// returns its parent, which lies outside it. Both have symlinks resolved.
// Tests that use it change the process working directory, so they cannot
// run in parallel.
func chdirWork(t *testing.T) (work, parent string) {
	t.Helper()

	parent, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	work = filepath.Join(parent, "work")
	require.NoError(t, os.Mkdir(work, 0o700))
	t.Chdir(work)

	return work, parent
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()

	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

var errBoom = errors.New("boom")

// errFS fails every Open with err.
type errFS struct{ err error }

func (f errFS) Open(string) (fs.File, error) { return nil, f.err }

// partialFS is an fs.ReadFileFS whose ReadFile returns data and err, as
// fs.ReadFile does when a read fails partway.
type partialFS struct {
	err  error
	data []byte
}

func (f partialFS) Open(string) (fs.File, error) { return nil, fs.ErrInvalid }

func (f partialFS) ReadFile(string) ([]byte, error) { return f.data, f.err }

func TestFSProvider_ReadsThroughSetFS(t *testing.T) {
	t.Parallel()

	var p providers.FSProvider

	p.SetFS(fstest.MapFS{"conf/app.json": {Data: []byte(`{"a": 1}`)}})

	got, err := p.ReadFile("conf/app.json")
	require.NoError(t, err)
	assert.Equal(t, []byte(`{"a": 1}`), got)
}

func TestFSProvider_SetFSReplacesFS(t *testing.T) {
	t.Parallel()

	var p providers.FSProvider

	p.SetFS(fstest.MapFS{"app.json": {Data: []byte("first")}})
	p.SetFS(fstest.MapFS{"app.json": {Data: []byte("second")}})

	got, err := p.ReadFile("app.json")
	require.NoError(t, err)
	assert.Equal(t, []byte("second"), got)
}

// The zero value reads through sysfs: a file in the working directory
// reads, and one outside it is refused although it exists.
func TestFSProvider_ZeroValueReadsThroughSysFS(t *testing.T) {
	work, parent := chdirWork(t)
	writeFile(t, filepath.Join(work, "app.json"), "inside")
	writeFile(t, filepath.Join(parent, "outside.json"), "outside")

	var p providers.FSProvider

	got, err := p.ReadFile("app.json")
	require.NoError(t, err)
	assert.Equal(t, []byte("inside"), got)

	_, err = p.ReadFile("../outside.json")
	assert.ErrorIs(t, err, sysfs.ErrUnsafeFilePathOutsideDirectory)
}

// SetFS(nil) restores the default, so it undoes an earlier SetFS.
//
// Not parallel: it changes the working directory.
func TestFSProvider_SetFSNilReadsThroughSysFS(t *testing.T) {
	work, _ := chdirWork(t)
	writeFile(t, filepath.Join(work, "app.json"), "disk")

	var p providers.FSProvider

	p.SetFS(fstest.MapFS{"app.json": {Data: []byte("map")}})
	p.SetFS(nil)

	got, err := p.ReadFile("app.json")
	require.NoError(t, err)
	assert.Equal(t, []byte("disk"), got)
}

// A missing file can be told apart with errors.Is, through sysfs and
// through a file system that is set, as the `.env` provider does to treat a
// missing file as empty.
func TestFSProvider_MissingFileIsErrNotExist(t *testing.T) {
	chdirWork(t)

	var p providers.FSProvider

	_, err := p.ReadFile("missing.json")
	require.ErrorIs(t, err, fs.ErrNotExist, "sysfs")

	p.SetFS(fstest.MapFS{})

	_, err = p.ReadFile("missing.json")
	require.ErrorIs(t, err, fs.ErrNotExist, "set FS")
}

// An error other than a missing file is returned too, and does not match
// fs.ErrNotExist.
func TestFSProvider_ReadFileError(t *testing.T) {
	t.Parallel()

	var p providers.FSProvider

	p.SetFS(errFS{err: fs.ErrPermission})

	_, err := p.ReadFile("app.json")
	require.ErrorIs(t, err, fs.ErrPermission)
	assert.NotErrorIs(t, err, fs.ErrNotExist)
}

// The error is the file system's own, with nothing added, so the provider
// that wraps it names the file once.
func TestFSProvider_ReadFileReturnsFSErrorAsIs(t *testing.T) {
	t.Parallel()

	var p providers.FSProvider

	p.SetFS(errFS{err: errBoom})

	_, err := p.ReadFile("app.json")
	assert.Same(t, errBoom, err)
}

// Data read before an error is dropped.
func TestFSProvider_ReadFileErrorReturnsNilData(t *testing.T) {
	t.Parallel()

	var p providers.FSProvider

	p.SetFS(partialFS{data: []byte("part"), err: fs.ErrClosed})

	got, err := p.ReadFile("app.json")
	require.ErrorIs(t, err, fs.ErrClosed)
	assert.Nil(t, got)
}
