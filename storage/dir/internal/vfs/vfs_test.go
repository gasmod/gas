package vfs

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestNewVFS_ClosedRoot checks that NewVFS on a closed root fails with
// os.ErrClosed.
func TestNewVFS_ClosedRoot(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	if err := root.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := NewVFS(root); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("NewVFS on a closed root = %v, want ErrClosed", err)
	}
}

// TestNewVFS_DirIsFile checks that a regular file where an internal directory
// belongs makes NewVFS fail with the filesystem error for that directory.
func TestNewVFS_DirIsFile(t *testing.T) {
	for _, rel := range []string{objectsPath, locksPath} {
		t.Run(rel, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, rel)
			if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
				t.Fatalf("MkdirAll: %v", err)
			}
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatalf("OpenRoot: %v", err)
			}
			t.Cleanup(func() { _ = root.Close() })

			_, err = NewVFS(root)
			var pe *os.PathError
			if !errors.As(err, &pe) {
				t.Fatalf("NewVFS = %v, want a *os.PathError", err)
			}
		})
	}
}

// TestVFS_Root checks that Root returns the root the VFS was created on.
func TestVFS_Root(t *testing.T) {
	v, dir := newTestVFS(t)
	if got := v.Root().Name(); got != dir {
		t.Fatalf("Root().Name() = %q, want %q", got, dir)
	}
}

// TestCreateBucket_Errors checks that a file in place of a bucket dir is an
// I/O error, not ErrBucketExists.
func TestCreateBucket_Errors(t *testing.T) {
	for i, name := range []string{"meta dir", "object dir", "locks dir"} {
		t.Run(name, func(t *testing.T) {
			v, dir := newTestVFS(t)
			m, o, l := bucketPaths(dir)
			// A regular file where the directory should go makes the bucket
			// impossible to create; it is an I/O error, not "exists".
			if err := os.WriteFile([]string{m, o, l}[i], nil, 0o600); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			_, err := v.CreateBucket(t.Context(), "b")
			if err == nil {
				t.Fatal("CreateBucket succeeded with a file in place of a bucket dir")
			}
			if errors.Is(err, ErrBucketExists) {
				t.Fatalf("CreateBucket = %v, want an I/O error, not ErrBucketExists", err)
			}
			var pe *os.PathError
			if !errors.As(err, &pe) {
				t.Fatalf("CreateBucket = %v, want a *os.PathError", err)
			}
		})
	}
}

// TestCreateBucket_Exists checks that CreateBucket of an existing bucket is
// ErrBucketExists and changes nothing.
func TestCreateBucket_Exists(t *testing.T) {
	v, _ := newTestVFS(t)
	b := newTestBucket(t, v)
	putObject(t, b, "k", "x", "")

	for range 2 {
		if _, err := v.CreateBucket(t.Context(), "b"); !errors.Is(err, ErrBucketExists) {
			t.Fatalf("CreateBucket of an existing bucket = %v, want ErrBucketExists", err)
		}
	}
	// The existing bucket and its objects are untouched.
	if body, _ := getObject(t, b, "k"); body != "x" {
		t.Fatalf("body = %q, want x", body)
	}
}

// TestCreateBucket_CompletesPartialBucket checks that a bucket whose creation
// stopped before the metadata dir was made (the commit point) is created
// normally on the next attempt.
func TestCreateBucket_CompletesPartialBucket(t *testing.T) {
	v, dir := newTestVFS(t)
	_, o, l := bucketPaths(dir)
	for _, p := range []string{o, l} {
		if err := os.MkdirAll(p, 0o750); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
	}
	if _, err := v.OpenBucket("b"); !errors.Is(err, ErrBucketNotFound) {
		t.Fatalf("OpenBucket of a partial bucket = %v, want ErrBucketNotFound", err)
	}
	if _, err := v.CreateBucket(t.Context(), "b"); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	b := newOpenBucket(t, v)
	putObject(t, b, "k", "x", "")
}

// TestOpenBucket_Missing checks that any missing bucket dir is
// ErrBucketNotFound.
func TestOpenBucket_Missing(t *testing.T) {
	tests := []struct {
		name   string
		remove int // index of the dir to remove after creating the bucket; -1 creates nothing
	}{
		{"whole bucket", -1},
		{"meta dir", 0},
		{"object dir", 1},
		{"locks dir", 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, dir := newTestVFS(t)
			if tt.remove >= 0 {
				if _, err := v.CreateBucket(t.Context(), "b"); err != nil {
					t.Fatalf("CreateBucket: %v", err)
				}
				m, o, l := bucketPaths(dir)
				if err := os.Remove([]string{m, o, l}[tt.remove]); err != nil {
					t.Fatalf("Remove: %v", err)
				}
			}
			b, err := v.OpenBucket("b")
			if err == nil {
				_ = b.Close()
				t.Fatal("OpenBucket succeeded on a missing bucket")
			}
			if !errors.Is(err, ErrBucketNotFound) {
				t.Fatalf("OpenBucket error = %v, want ErrBucketNotFound", err)
			}
			if !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("OpenBucket error = %v, want ErrNotExist", err)
			}
		})
	}
}

// TestOpenBucket_NotADirectory checks that a bucket dir that exists but
// cannot be opened is an I/O error, not a missing bucket.
func TestOpenBucket_NotADirectory(t *testing.T) {
	for i, name := range []string{"meta dir", "object dir", "locks dir"} {
		t.Run(name, func(t *testing.T) {
			v, dir := newTestVFS(t)
			if _, err := v.CreateBucket(t.Context(), "b"); err != nil {
				t.Fatalf("CreateBucket: %v", err)
			}
			m, o, l := bucketPaths(dir)
			path := []string{m, o, l}
			if err := os.Remove(path[i]); err != nil {
				t.Fatalf("Remove: %v", err)
			}
			if err := os.WriteFile(path[i], nil, 0o600); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			b, err := v.OpenBucket("b")
			if err == nil {
				_ = b.Close()
				t.Fatal("OpenBucket succeeded with a file in place of a bucket dir")
			}
			if errors.Is(err, ErrBucketNotFound) {
				t.Fatalf("OpenBucket = %v, want an I/O error, not ErrBucketNotFound", err)
			}
		})
	}
}

// TestFileModes covers bug 9: directories must not be group-writable or
// world-accessible, and metadata, data and lock files must be private to the
// owner.
func TestFileModes(t *testing.T) {
	skipIfNoPermissions(t)
	v, dir := newTestVFS(t)
	b := newTestBucket(t, v)
	putObject(t, b, "k", "x", "")
	// Keep the lock file around by holding the lock.
	if err := b.lock.Lock(t.Context(), hash("k")); err != nil {
		t.Fatalf("Lock: %v", err)
	}
	defer func() { _ = b.lock.Unlock(hash("k")) }()

	m, o, l := bucketPaths(dir)
	dirs := []string{
		filepath.Join(dir, ".gas"),
		filepath.Join(dir, objectsPath),
		filepath.Join(dir, locksPath),
		m, o, l,
	}
	for _, p := range dirs {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatalf("Stat(%s): %v", p, err)
		}
		if perm := fi.Mode().Perm(); perm&0o027 != 0 {
			t.Errorf("dir %s mode = %o, want no bits outside 0o750", p, perm)
		}
	}

	files := []string{
		filepath.Join(m, hash("k")+metaFileExt),
		filepath.Join(o, dataFile(t, b, "k")),
		filepath.Join(l, hash("k")),
	}
	for _, p := range files {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatalf("Stat(%s): %v", p, err)
		}
		if perm := fi.Mode().Perm(); perm&0o077 != 0 {
			t.Errorf("file %s mode = %o, want no bits outside 0o600", p, perm)
		}
	}
}
