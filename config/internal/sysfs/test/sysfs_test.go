package sysfs_test

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/config/internal/sysfs"
)

// maxConfigFileSize is the 1 MiB limit Open enforces.
const maxConfigFileSize = 1 << 20

// chdirWork makes a fresh directory the working directory for the test and
// returns it with its parent, which lies outside it. Both have symlinks
// resolved. Tests that use it change the process working directory, so
// they cannot run in parallel.
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

	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

func symlink(t *testing.T, target, path string) {
	t.Helper()

	require.NoError(t, os.Symlink(target, path))
}

func read(name string) (string, error) {
	data, err := fs.ReadFile(sysfs.SysFS{}, name)

	return string(data), err
}

// Not parallel: it changes the working directory.
func TestOpen_ReadsInsideWorkingDirectory(t *testing.T) {
	work, _ := chdirWork(t)
	writeFile(t, filepath.Join(work, "app.json"), "app")
	writeFile(t, filepath.Join(work, "conf", "db.json"), "db")

	for _, name := range []string{
		"app.json",
		"./app.json",
		filepath.Join(work, "app.json"),
		"conf/../app.json",
		"../work/app.json",
	} {
		got, err := read(name)
		require.NoError(t, err, name)
		assert.Equal(t, "app", got, name)
	}

	got, err := read("conf/db.json")
	require.NoError(t, err)
	assert.Equal(t, "db", got)
}

// A path outside the working directory is refused, also when its directory
// shares the working directory's name as a prefix.
//
// Not parallel: it changes the working directory.
func TestOpen_RefusesOutsideWorkingDirectory(t *testing.T) {
	_, parent := chdirWork(t)
	writeFile(t, filepath.Join(parent, "outside.json"), "outside")
	writeFile(t, filepath.Join(parent, "work2", "sibling.json"), "sibling")

	for _, name := range []string{
		"../outside.json",
		filepath.Join(parent, "outside.json"),
		"../work2/sibling.json",
		filepath.Join(parent, "work2", "sibling.json"),
	} {
		_, err := read(name)
		assert.ErrorIs(t, err, sysfs.ErrUnsafeFilePathOutsideDirectory, name)
	}
}

// With the working directory /, every absolute path is inside.
//
// Not parallel: it changes the working directory.
func TestOpen_RootWorkingDirectory(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	path := filepath.Join(dir, "app.json")
	writeFile(t, path, "app")
	t.Chdir("/")

	for _, name := range []string{path, strings.TrimPrefix(path, "/")} {
		got, err := read(name)
		require.NoError(t, err, name)
		assert.Equal(t, "app", got, name)
	}
}

// A relative symlink that stays inside is followed, in the directory part
// or as the last element.
//
// Not parallel: it changes the working directory.
func TestOpen_FollowsRelativeSymlinkInside(t *testing.T) {
	work, _ := chdirWork(t)
	writeFile(t, filepath.Join(work, "real", "app.json"), "app")
	symlink(t, "real", filepath.Join(work, "dirlink"))
	symlink(t, "real/app.json", filepath.Join(work, "filelink.json"))
	require.NoError(t, os.Mkdir(filepath.Join(work, "conf"), 0o700))
	symlink(t, "../real/app.json", filepath.Join(work, "conf", "up.json"))

	for _, name := range []string{"dirlink/app.json", "filelink.json", "conf/up.json"} {
		got, err := read(name)
		require.NoError(t, err, name)
		assert.Equal(t, "app", got, name)
	}
}

// The layout of a Kubernetes ConfigMap volume: each file is a symlink into
// ..data, itself a symlink to a timestamped directory.
//
// Not parallel: it changes the working directory.
func TestOpen_FollowsConfigMapLayout(t *testing.T) {
	work, _ := chdirWork(t)
	cfg := filepath.Join(work, "config")
	writeFile(t, filepath.Join(cfg, "..2026_10_28_00_00_00.000000000", "app.json"), "app")
	symlink(t, "..2026_10_28_00_00_00.000000000", filepath.Join(cfg, "..data"))
	symlink(t, "..data/app.json", filepath.Join(cfg, "app.json"))

	got, err := read("config/app.json")
	require.NoError(t, err)
	assert.Equal(t, "app", got)
}

// A symlink whose target leads outside is refused, in the directory part
// or as the last element.
//
// Not parallel: it changes the working directory.
func TestOpen_RefusesSymlinkOutside(t *testing.T) {
	work, parent := chdirWork(t)
	writeFile(t, filepath.Join(parent, "secret", "secret.json"), "secret")
	symlink(t, "../secret", filepath.Join(work, "reldir"))
	symlink(t, filepath.Join(parent, "secret"), filepath.Join(work, "absdir"))
	symlink(t, "../secret/secret.json", filepath.Join(work, "relfile.json"))
	symlink(t, filepath.Join(parent, "secret", "secret.json"), filepath.Join(work, "absfile.json"))

	for _, name := range []string{
		"reldir/secret.json",
		"absdir/secret.json",
		"relfile.json",
		"absfile.json",
	} {
		_, err := read(name)
		assert.ErrorIs(t, err, sysfs.ErrUnsafeFilePathOutsideDirectory, name)
	}
}

// An absolute symlink is refused even when its target lies inside.
//
// Not parallel: it changes the working directory.
func TestOpen_RefusesAbsoluteSymlinkInside(t *testing.T) {
	work, _ := chdirWork(t)
	writeFile(t, filepath.Join(work, "real", "app.json"), "app")
	symlink(t, filepath.Join(work, "real"), filepath.Join(work, "dirlink"))
	symlink(t, filepath.Join(work, "real", "app.json"), filepath.Join(work, "filelink.json"))

	for _, name := range []string{"dirlink/app.json", "filelink.json"} {
		_, err := read(name)
		assert.ErrorIs(t, err, sysfs.ErrUnsafeFilePathOutsideDirectory, name)
	}
}

// The working directory may itself lie under a symlink.
//
// Not parallel: it changes the working directory.
func TestOpen_WorkingDirectoryUnderSymlink(t *testing.T) {
	_, parent := chdirWork(t)
	target := filepath.Join(parent, "real")
	writeFile(t, filepath.Join(target, "app.json"), "app")

	link := filepath.Join(parent, "link")
	symlink(t, target, link)
	t.Chdir(link)

	got, err := read("app.json")
	require.NoError(t, err)
	assert.Equal(t, "app", got)
}

// A symlink loop, or a path that needs more than eight symlinks, fails.
//
// Not parallel: it changes the working directory.
func TestOpen_SymlinkLimit(t *testing.T) {
	work, _ := chdirWork(t)
	writeFile(t, filepath.Join(work, "app.json"), "app")
	// l1 -> app.json, l2 -> l1, ..., l9 -> l8.
	symlink(t, "app.json", filepath.Join(work, "l1"))

	for i := 2; i <= 9; i++ {
		symlink(t, "l"+strconv.Itoa(i-1), filepath.Join(work, "l"+strconv.Itoa(i)))
	}

	symlink(t, "loop2", filepath.Join(work, "loop1"))
	symlink(t, "loop1", filepath.Join(work, "loop2"))

	got, err := read("l8")
	require.NoError(t, err)
	assert.Equal(t, "app", got)

	_, err = read("l9")
	require.ErrorIs(t, err, syscall.ELOOP)

	_, err = read("loop1")
	require.ErrorIs(t, err, syscall.ELOOP)
}

// A directory is not a regular file, including the working directory
// itself, named by "" or ".".
//
// Not parallel: it changes the working directory.
func TestOpen_RefusesDirectory(t *testing.T) {
	work, _ := chdirWork(t)
	require.NoError(t, os.Mkdir(filepath.Join(work, "conf"), 0o700))

	for _, name := range []string{"conf", "conf/", "", ".", work} {
		_, err := sysfs.SysFS{}.Open(name)
		assert.ErrorIs(t, err, sysfs.ErrNotRegularFile, name)
	}
}

// A path that ends in a separator names a directory, so it does not open a
// file.
//
// Not parallel: it changes the working directory.
func TestOpen_TrailingSeparatorNeedsDirectory(t *testing.T) {
	work, _ := chdirWork(t)
	writeFile(t, filepath.Join(work, "app.json"), "app")

	_, err := sysfs.SysFS{}.Open("app.json/")
	assert.ErrorIs(t, err, syscall.ENOTDIR)
}

// A file of exactly 1 MiB is read; one byte more is refused.
//
// Not parallel: it changes the working directory.
func TestOpen_SizeLimit(t *testing.T) {
	work, _ := chdirWork(t)
	writeFile(t, filepath.Join(work, "max.json"), strings.Repeat("a", maxConfigFileSize))
	writeFile(t, filepath.Join(work, "big.json"), strings.Repeat("a", maxConfigFileSize+1))

	got, err := read("max.json")
	require.NoError(t, err)
	assert.Len(t, got, maxConfigFileSize)

	_, err = sysfs.SysFS{}.Open("big.json")
	assert.ErrorIs(t, err, sysfs.ErrConfigFileTooLarge)
}

// A file that grows after Open is read up to the limit, and fails past it.
//
// Not parallel: it changes the working directory.
func TestOpen_FileGrowsAfterOpen(t *testing.T) {
	work, _ := chdirWork(t)

	for _, tc := range []struct {
		name    string
		size    int
		wantErr bool
	}{
		{name: "to the limit", size: maxConfigFileSize},
		{name: "past the limit", size: maxConfigFileSize + 1, wantErr: true},
	} {
		path := filepath.Join(work, "grow.json")
		writeFile(t, path, "a")

		f, err := sysfs.SysFS{}.Open("grow.json")
		require.NoError(t, err, tc.name)
		writeFile(t, path, strings.Repeat("a", tc.size))

		got, err := io.ReadAll(f)
		require.NoError(t, f.Close(), tc.name)

		if tc.wantErr {
			require.ErrorIs(t, err, sysfs.ErrConfigFileTooLarge, tc.name)

			continue
		}

		require.NoError(t, err, tc.name)
		assert.Len(t, got, tc.size, tc.name)
	}
}

// A refusal is the bare sentinel, without the file name, which the
// provider adds.
//
// Not parallel: it changes the working directory.
func TestOpen_RefusalIsBareSentinel(t *testing.T) {
	work, _ := chdirWork(t)
	writeFile(t, filepath.Join(work, "big.json"), strings.Repeat("a", maxConfigFileSize+1))
	require.NoError(t, os.Mkdir(filepath.Join(work, "conf"), 0o700))

	for name, want := range map[string]error{
		"../outside.json": sysfs.ErrUnsafeFilePathOutsideDirectory,
		"conf":            sysfs.ErrNotRegularFile,
		"big.json":        sysfs.ErrConfigFileTooLarge,
	} {
		_, err := sysfs.SysFS{}.Open(name)
		assert.Same(t, want, err, name)
	}
}

// A missing file is the *fs.PathError from os, which names the path
// relative to the working directory and matches fs.ErrNotExist.
//
// Not parallel: it changes the working directory.
func TestOpen_MissingFile(t *testing.T) {
	work, _ := chdirWork(t)

	for _, name := range []string{"app.json", filepath.Join(work, "app.json")} {
		_, err := sysfs.SysFS{}.Open(name)
		require.ErrorIs(t, err, fs.ErrNotExist, name)

		var pathErr *fs.PathError

		require.ErrorAs(t, err, &pathErr, name)
		require.EqualError(t, err, "openat app.json: no such file or directory", name)
	}

	_, err := sysfs.SysFS{}.Open("conf/app.json")
	require.ErrorIs(t, err, fs.ErrNotExist)
}

// Each Open looks up the working directory again.
//
// Not parallel: it changes the working directory.
func TestOpen_FollowsWorkingDirectoryChange(t *testing.T) {
	work, parent := chdirWork(t)
	writeFile(t, filepath.Join(work, "app.json"), "app")

	_, err := read("app.json")
	require.NoError(t, err)

	t.Chdir(parent)

	_, err = read("app.json")
	require.ErrorIs(t, err, fs.ErrNotExist)
}

// The returned file reports the opened file's info.
//
// Not parallel: it changes the working directory.
func TestOpen_FileStat(t *testing.T) {
	work, _ := chdirWork(t)
	writeFile(t, filepath.Join(work, "app.json"), "app")

	f, err := sysfs.SysFS{}.Open("app.json")
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })

	info, err := f.Stat()
	require.NoError(t, err)
	assert.Equal(t, "app.json", info.Name())
	assert.Equal(t, int64(3), info.Size())
	assert.True(t, info.Mode().IsRegular())
}
