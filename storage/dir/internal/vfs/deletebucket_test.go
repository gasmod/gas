package vfs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// assertBucketGone checks that DeleteBucket left nothing of bucket "b": no
// metadata, objects or locks dir, and no name record.
func assertBucketGone(t *testing.T, v *VFS, dir string) {
	t.Helper()
	meta, obj, locks := bucketPaths(dir)
	for _, p := range []string{meta, obj, locks, filepath.Join(dir, bucketNameFile("b"))} {
		if _, err := os.Lstat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s after DeleteBucket: %v, want it removed", p, err)
		}
	}
	if _, err := v.OpenBucket("b"); !errors.Is(err, ErrBucketNotFound) {
		t.Errorf("OpenBucket after DeleteBucket = %v, want ErrBucketNotFound", err)
	}
}

// writeDebris writes name, with content, into the metadata dir of bucket
// "b".
func writeDebris(t *testing.T, dir, name, content string) {
	t.Helper()
	meta, _, _ := bucketPaths(dir)
	if err := os.WriteFile(filepath.Join(meta, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestDeleteBucket_Empty deletes an empty bucket and everything it kept on
// disk.
func TestDeleteBucket_Empty(t *testing.T) {
	v, dir := newTestVFS(t)
	if _, err := v.CreateBucket(t.Context(), "b"); err != nil {
		t.Fatal(err)
	}
	if err := v.DeleteBucket(t.Context(), "b", nil); err != nil {
		t.Fatalf("DeleteBucket: %v", err)
	}
	assertBucketGone(t, v, dir)
}

// TestDeleteBucket_Missing reports a bucket that does not exist.
func TestDeleteBucket_Missing(t *testing.T) {
	v, _ := newTestVFS(t)
	if err := v.DeleteBucket(t.Context(), "b", nil); !errors.Is(err, ErrBucketNotFound) {
		t.Errorf("DeleteBucket(missing) = %v, want ErrBucketNotFound", err)
	}
}

// TestDeleteBucket_NotEmpty refuses a bucket that holds an object and
// leaves the object readable.
func TestDeleteBucket_NotEmpty(t *testing.T) {
	v, _ := newTestVFS(t)
	b := newTestBucket(t, v)
	putObject(t, b, "k", "body", "text/plain")

	if err := v.DeleteBucket(t.Context(), "b", nil); !errors.Is(err, ErrBucketNotEmpty) {
		t.Fatalf("DeleteBucket(not empty) = %v, want ErrBucketNotEmpty", err)
	}
	if body, _ := getObject(t, newOpenBucket(t, v), "k"); body != "body" {
		t.Errorf("object after DeleteBucket(not empty) = %q, want body", body)
	}
}

// TestDeleteBucket_Emptied deletes a bucket whose objects were all deleted.
func TestDeleteBucket_Emptied(t *testing.T) {
	v, dir := newTestVFS(t)
	b := newTestBucket(t, v)
	putObject(t, b, "k", "body", "")
	if err := b.DeleteObject(t.Context(), &DeleteObjectInput{Key: "k"}); err != nil {
		t.Fatal(err)
	}
	if err := v.DeleteBucket(t.Context(), "b", nil); err != nil {
		t.Fatalf("DeleteBucket: %v", err)
	}
	assertBucketGone(t, v, dir)
}

// TestDeleteBucket_Debris checks that entries no listing can return do not
// keep a bucket from being deleted: metadata whose key cannot be decoded,
// metadata whose data file is gone, and a temp file a crashed write left.
// Each would otherwise make the bucket undeletable, since no caller can
// name or delete them.
func TestDeleteBucket_Debris(t *testing.T) {
	tests := []struct {
		setup func(t *testing.T, b *Bucket, dir string)
		name  string
	}{
		{
			name: "metadata that does not decode",
			setup: func(t *testing.T, _ *Bucket, dir string) {
				writeDebris(t, dir, hash("k")+metaFileExt, "{")
			},
		},
		{
			name: "metadata whose data file is gone",
			setup: func(t *testing.T, b *Bucket, dir string) {
				putObject(t, b, "k", "body", "")
				_, obj, _ := bucketPaths(dir)
				if err := os.Remove(filepath.Join(obj, dataFile(t, b, "k"))); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "temp metadata a crashed write left",
			setup: func(t *testing.T, _ *Bucket, dir string) {
				writeDebris(
					t,
					dir,
					hash("k")+metaFileExt+"0f1e2d3c-4b5a-4968-8778-695a4b3c2d1e",
					"{}",
				)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, dir := newTestVFS(t)
			tt.setup(t, newTestBucket(t, v), dir)
			if err := v.DeleteBucket(t.Context(), "b", nil); err != nil {
				t.Fatalf("DeleteBucket: %v", err)
			}
			assertBucketGone(t, v, dir)
		})
	}
}

// TestDeleteBucket_ForeignFile refuses a bucket whose metadata dir holds a
// file this package did not write, and removes nothing.
func TestDeleteBucket_ForeignFile(t *testing.T) {
	v, dir := newTestVFS(t)
	b := newTestBucket(t, v)
	putObject(t, b, "k", "body", "")
	if err := b.DeleteObject(t.Context(), &DeleteObjectInput{Key: "k"}); err != nil {
		t.Fatal(err)
	}
	writeDebris(t, dir, "notes.txt", "mine")

	err := v.DeleteBucket(t.Context(), "b", nil)
	if err == nil || errors.Is(err, ErrBucketNotEmpty) || errors.Is(err, ErrBucketNotFound) {
		t.Fatalf("DeleteBucket(foreign file) = %v, want a plain error", err)
	}
	if strings.Contains(err.Error(), "notes.txt") {
		t.Errorf("error %q quotes the file name", err)
	}
	if _, oErr := v.OpenBucket("b"); oErr != nil {
		t.Errorf("OpenBucket after a refused DeleteBucket: %v", oErr)
	}
	meta, _, _ := bucketPaths(dir)
	if got := listDir(t, meta); len(got) != 1 || got[0] != "notes.txt" {
		t.Errorf("meta dir = %q, want only notes.txt", got)
	}
}

// TestDeleteBucket_InFlightWrite checks that the sweep does not remove the
// temp metadata file of a write in progress. A writer holds the key's lock
// from before it writes the temp file until after it renames it, so the
// sweep waits for it; the write then lands and the bucket is not empty.
// Nothing is reported removed: the temp file the sweep listed was renamed
// away by the write, not removed by the sweep.
func TestDeleteBucket_InFlightWrite(t *testing.T) {
	v, dir := newTestVFS(t)
	if _, err := v.CreateBucket(t.Context(), "b"); err != nil {
		t.Fatal(err)
	}
	metaDir, objDir, locksDir := bucketPaths(dir)

	// The writer: another holder of the key's lock, with its data file and
	// temp metadata written but not yet committed.
	locksRoot, err := os.OpenRoot(locksDir)
	if err != nil {
		t.Fatal(err)
	}
	writer := NewLock(locksRoot)
	t.Cleanup(func() { _ = writer.Close() })
	name := hash("k")
	if lErr := writer.Lock(t.Context(), name); lErr != nil {
		t.Fatal(lErr)
	}
	meta := ObjectMetadata{Key: "k", DataFile: newDataFileName(name)}
	if wErr := os.WriteFile(
		filepath.Join(objDir, meta.DataFile),
		[]byte("body"),
		0o600,
	); wErr != nil {
		t.Fatal(wErr)
	}
	data, _, err := encodeMeta(&meta)
	if err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(metaDir, name+metaFileExt+"0f1e2d3c-4b5a-4968-8778-695a4b3c2d1e")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	var reports []Skipped
	go func() {
		done <- v.DeleteBucket(t.Context(), "b", func(s Skipped) { reports = append(reports, s) })
	}()

	select {
	case err := <-done:
		t.Fatalf("DeleteBucket returned %v while the writer held the key", err)
	case <-time.After(200 * time.Millisecond):
	}
	if _, err := os.Stat(tmp); err != nil {
		t.Fatalf("temp metadata of the write in progress: %v, want it kept", err)
	}

	// The write commits and gives up the lock.
	if err := os.Rename(tmp, filepath.Join(metaDir, name+metaFileExt)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Unlock(name); err != nil {
		t.Fatal(err)
	}

	if err := <-done; !errors.Is(err, ErrBucketNotEmpty) {
		t.Fatalf("DeleteBucket after the write = %v, want ErrBucketNotEmpty", err)
	}
	for _, s := range reports {
		t.Errorf("DeleteBucket reported %s: %v; want nothing reported", s.Path, s.Err)
	}
	if body, _ := getObject(t, newOpenBucket(t, v), "k"); body != "body" {
		t.Errorf("object written during DeleteBucket = %q, want body", body)
	}
}

// TestDeleteBucket_KeepsHeldLockFile checks that cleanup never unlinks a
// lock file another holder has locked: the bucket is deleted, and that lock
// file and its dir stay.
func TestDeleteBucket_KeepsHeldLockFile(t *testing.T) {
	v, dir := newTestVFS(t)
	if _, err := v.CreateBucket(t.Context(), "b"); err != nil {
		t.Fatal(err)
	}
	_, _, locksDir := bucketPaths(dir)
	locksRoot, err := os.OpenRoot(locksDir)
	if err != nil {
		t.Fatal(err)
	}
	holder := NewLock(locksRoot)
	t.Cleanup(func() { _ = holder.Close() })
	if err := holder.Lock(t.Context(), "held"); err != nil {
		t.Fatal(err)
	}

	if err := v.DeleteBucket(t.Context(), "b", nil); err != nil {
		t.Fatalf("DeleteBucket: %v", err)
	}
	if _, err := os.Stat(filepath.Join(locksDir, "held")); err != nil {
		t.Errorf("held lock file after DeleteBucket: %v, want it kept", err)
	}
	if _, err := v.OpenBucket("b"); !errors.Is(err, ErrBucketNotFound) {
		t.Errorf("OpenBucket after DeleteBucket = %v, want ErrBucketNotFound", err)
	}
}

// TestDeleteBucket_ThenCreate checks that a bucket created again after a
// delete gets a new name record, so a stale CreationDate cannot survive.
func TestDeleteBucket_ThenCreate(t *testing.T) {
	v, dir := newTestVFS(t)
	if _, err := v.CreateBucket(t.Context(), "b"); err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(dir, bucketNameFile("b"))
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(record, old, old); err != nil {
		t.Fatal(err)
	}
	if err := v.DeleteBucket(t.Context(), "b", nil); err != nil {
		t.Fatal(err)
	}
	entry, err := v.CreateBucket(t.Context(), "b")
	if err != nil || !entry.Created.After(old.Add(time.Minute)) {
		t.Errorf("CreateBucket after a delete = %+v, %v; want a new CreationDate", entry, err)
	}
}

// TestHeadBucket checks HeadBucket of an existing, a missing and an unnamed
// bucket.
func TestHeadBucket(t *testing.T) {
	v, dir := newTestVFS(t)
	if _, err := v.HeadBucket("b"); !errors.Is(err, ErrBucketNotFound) {
		t.Errorf("HeadBucket(missing) = %v, want ErrBucketNotFound", err)
	}
	if _, err := v.CreateBucket(t.Context(), "b"); err != nil {
		t.Fatal(err)
	}
	entry, err := v.HeadBucket("b")
	if err != nil || entry.Name != "b" || entry.Created.IsZero() {
		t.Errorf("HeadBucket = %+v, %v; want b with a CreationDate", entry, err)
	}
	if rErr := os.Remove(filepath.Join(dir, bucketNameFile("b"))); rErr != nil {
		t.Fatal(rErr)
	}
	entry, err = v.HeadBucket("b")
	if err != nil || entry.Name != "b" || !entry.Created.IsZero() {
		t.Errorf("HeadBucket(unnamed) = %+v, %v; want b with no CreationDate", entry, err)
	}
}

// TestBucketLifecycleLock checks that CreateBucket and DeleteBucket wait for
// the bucket's lifecycle lock, so neither can run while the other holds it:
// a bucket created while it is being deleted could lose its name record or
// a data file to the delete's cleanup.
func TestBucketLifecycleLock(t *testing.T) {
	for name, op := range map[string]func(v *VFS) error{
		"CreateBucket": func(v *VFS) error { _, err := v.CreateBucket(t.Context(), "b"); return err },
		"DeleteBucket": func(v *VFS) error { return v.DeleteBucket(t.Context(), "b", nil) },
	} {
		t.Run(name, func(t *testing.T) {
			v, dir := newTestVFS(t)
			if _, err := v.CreateBucket(t.Context(), "b"); err != nil {
				t.Fatal(err)
			}
			locksRoot, err := os.OpenRoot(filepath.Join(dir, locksPath))
			if err != nil {
				t.Fatal(err)
			}
			other := NewLock(locksRoot)
			t.Cleanup(func() { _ = other.Close() })
			if lErr := other.Lock(t.Context(), bucketLockName("b")); lErr != nil {
				t.Fatal(lErr)
			}

			done := make(chan error, 1)
			go func() { done <- op(v) }()
			select {
			case err := <-done:
				t.Fatalf("%s returned %v while another holder had the bucket's lock", name, err)
			case <-time.After(200 * time.Millisecond):
			}
			if rErr := other.Release(bucketLockName("b")); rErr != nil {
				t.Fatal(rErr)
			}
			if err := <-done; err != nil && !errors.Is(err, ErrBucketExists) {
				t.Errorf("%s after the lock was released: %v", name, err)
			}
		})
	}
}

// TestDeleteBucket_RefusedDeletesNothing checks that a DeleteBucket refused
// for an object removes no debris either: the corrupt metadata below sorts
// before the object, and must still be there afterwards with its data file.
func TestDeleteBucket_RefusedDeletesNothing(t *testing.T) {
	v, dir := newTestVFS(t)
	b := newTestBucket(t, v)
	putObject(t, b, "k", "body", "")

	debris := strings.Repeat("0", hashLen)
	_, objDir, _ := bucketPaths(dir)
	debrisData := filepath.Join(objDir, newDataFileName(debris))
	if err := os.WriteFile(debrisData, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeDebris(t, dir, debris+metaFileExt, "{")

	if err := v.DeleteBucket(t.Context(), "b", nil); !errors.Is(err, ErrBucketNotEmpty) {
		t.Fatalf("DeleteBucket = %v, want ErrBucketNotEmpty", err)
	}
	metaDir, _, _ := bucketPaths(dir)
	for _, p := range []string{filepath.Join(metaDir, debris+metaFileExt), debrisData} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s after a refused DeleteBucket: %v, want it kept", p, err)
		}
	}
}

// TestDeleteBucket_KeepsDataFileOfWriteInProgress checks that the cleanup
// leaves the data file of a write still in progress: one whose writer holds
// the key's lock and has not written metadata yet, so the sweep never saw
// it.
func TestDeleteBucket_KeepsDataFileOfWriteInProgress(t *testing.T) {
	v, dir := newTestVFS(t)
	if _, err := v.CreateBucket(t.Context(), "b"); err != nil {
		t.Fatal(err)
	}
	_, objDir, locksDir := bucketPaths(dir)
	locksRoot, err := os.OpenRoot(locksDir)
	if err != nil {
		t.Fatal(err)
	}
	writer := NewLock(locksRoot)
	t.Cleanup(func() { _ = writer.Close() })
	name := hash("k")
	if lErr := writer.Lock(t.Context(), name); lErr != nil {
		t.Fatal(lErr)
	}
	data := filepath.Join(objDir, newDataFileName(name))
	if wErr := os.WriteFile(data, []byte("partial"), 0o600); wErr != nil {
		t.Fatal(wErr)
	}

	if dErr := v.DeleteBucket(t.Context(), "b", nil); dErr != nil {
		t.Fatalf("DeleteBucket: %v", dErr)
	}
	if _, sErr := os.Stat(data); sErr != nil {
		t.Errorf("data file of the write in progress: %v, want it kept", sErr)
	}
}
