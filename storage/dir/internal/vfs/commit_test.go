package vfs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCreateObject_OverwriteReplacesDataFile checks that the metadata rename
// is the commit point: an overwrite swaps body and metadata together and
// leaves only the new data file behind.
func TestCreateObject_OverwriteReplacesDataFile(t *testing.T) {
	v, dir := newTestVFS(t)
	b := newTestBucket(t, v)
	_, o, _ := bucketPaths(dir)

	putObject(t, b, "k", "first", "first/type")
	first := dataFile(t, b, "k")
	putObject(t, b, "k", "second", "second/type")
	second := dataFile(t, b, "k")

	if first == second {
		t.Fatalf("overwrite reused data file %s", first)
	}
	if got := listDir(t, o); len(got) != 1 || got[0] != second {
		t.Fatalf("object dir = %v, want only %s", got, second)
	}
	if body, obj := getObject(t, b, "k"); body != "second" || obj.ContentType != "second/type" {
		t.Fatalf("got %q (%s), want second (second/type)", body, obj.ContentType)
	}
}

// TestCreateObject_OverwriteKeepsOpenReader checks that a reader that opened
// the previous body keeps reading it after an overwrite.
func TestCreateObject_OverwriteKeepsOpenReader(t *testing.T) {
	v, _ := newTestVFS(t)
	b := newTestBucket(t, v)

	putObject(t, b, "k", "first", "first/type")
	obj, err := b.OpenObject(t.Context(), &OpenObjectInput{Key: "k"})
	if err != nil {
		t.Fatalf("OpenObject: %v", err)
	}
	defer func() { _ = obj.File.Close() }()

	putObject(t, b, "k", "second", "second/type")

	buf := make([]byte, 16)
	n, err := obj.File.Read(buf)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got := string(buf[:n]); got != "first" || obj.ContentType != "first/type" {
		t.Fatalf("open reader got %q (%s), want first (first/type)", got, obj.ContentType)
	}
}

// TestCreateObject_CommitFailsKeepsPrevious checks that when the metadata
// commit fails on an overwrite, the previous body and metadata are still
// served together and the new data file is discarded.
func TestCreateObject_CommitFailsKeepsPrevious(t *testing.T) {
	skipIfNoPermissions(t)
	v, dir := newTestVFS(t)
	b := newTestBucket(t, v)
	m, o, _ := bucketPaths(dir)

	putObject(t, b, "k", "old", "old/type")
	oldData := dataFile(t, b, "k")
	readOnlyDir(t, m)

	ct := "new/type"
	_, err := b.CreateObject(t.Context(), &CreateObjectInput{
		Key: "k", Body: strings.NewReader("new"), ContentType: &ct,
	})
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("CreateObject error = %v, want ErrPermission", err)
	}

	if got := listDir(t, o); len(got) != 1 || got[0] != oldData {
		t.Fatalf("object dir = %v, want only %s", got, oldData)
	}
	if body, obj := getObject(t, b, "k"); body != "old" || obj.ContentType != "old/type" {
		t.Fatalf("got %q (%s), want old (old/type)", body, obj.ContentType)
	}
}

// TestCreateObject_PreviousDataRemovalFails checks that failing to remove the
// previous data file after the commit is reported, but the new object is
// already in place.
func TestCreateObject_PreviousDataRemovalFails(t *testing.T) {
	v, dir := newTestVFS(t)
	b := newTestBucket(t, v)
	_, o, _ := bucketPaths(dir)

	putObject(t, b, "k", "old", "old/type")
	oldData := filepath.Join(o, dataFile(t, b, "k"))
	// A non-empty directory at the old data file's name cannot be removed.
	if err := os.Remove(oldData); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(oldData, "x"), 0o750); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	ct := "new/type"
	out, err := b.CreateObject(t.Context(), &CreateObjectInput{
		Key: "k", Body: strings.NewReader("new"), ContentType: &ct,
	})
	var pe *os.PathError
	if !errors.Is(err, ErrStaleData) || !errors.As(err, &pe) || pe.Path != filepath.Base(oldData) {
		t.Fatalf("CreateObject error = %v, want ErrStaleData for the old data file", err)
	}
	if out == nil || out.DataFile != dataFile(t, b, "k") {
		t.Fatalf("CreateObject out = %+v, want the committed metadata", out)
	}
	if body, obj := getObject(t, b, "k"); body != "new" || obj.ContentType != "new/type" {
		t.Fatalf("got %q (%s), want new (new/type)", body, obj.ContentType)
	}
}

// TestCreateObject_RepairsCorruptMetadata checks that put over corrupt
// metadata replaces it, so a corrupt key can be repaired.
func TestCreateObject_RepairsCorruptMetadata(t *testing.T) {
	for _, content := range corruptMetadata() {
		t.Run(content, func(t *testing.T) {
			v, dir := newTestVFS(t)
			b := newTestBucket(t, v)
			writeMeta(t, dir, "k", content)

			putObject(t, b, "k", "fixed", "text/plain")
			body, obj := getObject(t, b, "k")
			if body != "fixed" || obj.ContentType != "text/plain" {
				t.Fatalf("got %q (%s), want fixed (text/plain)", body, obj.ContentType)
			}
		})
	}
}

// TestDeleteObject_RemovesCommittedDataFile checks that delete removes the
// data file the metadata points to, not one derived from the key.
func TestDeleteObject_RemovesCommittedDataFile(t *testing.T) {
	v, dir := newTestVFS(t)
	b := newTestBucket(t, v)
	m, o, _ := bucketPaths(dir)

	putObject(t, b, "k", "first", "")
	putObject(t, b, "k", "second", "")
	if err := b.DeleteObject(t.Context(), &DeleteObjectInput{Key: "k"}); err != nil {
		t.Fatalf("DeleteObject: %v", err)
	}
	if got := listDir(t, o); len(got) != 0 {
		t.Fatalf("object dir = %v, want empty", got)
	}
	if got := listDir(t, m); len(got) != 0 {
		t.Fatalf("meta dir = %v, want empty", got)
	}
}

// TestCreateObject_PreviousMetadataUnreadable checks that metadata that
// exists but cannot be read is not overwritten: Put would orphan the data
// file it names. The Put fails and changes nothing.
func TestCreateObject_PreviousMetadataUnreadable(t *testing.T) {
	skipIfNoPermissions(t)
	v, dir := newTestVFS(t)
	b := newTestBucket(t, v)
	m, o, _ := bucketPaths(dir)

	putObject(t, b, "k", "old", "old/type")
	oldData := dataFile(t, b, "k")
	metaPath := filepath.Join(m, hash("k")+metaFileExt)
	if err := os.Chmod(metaPath, 0o000); err != nil {
		t.Fatalf("Chmod: %v", err)
	}

	out, err := b.CreateObject(t.Context(), &CreateObjectInput{
		Key: "k", Body: strings.NewReader("new"),
	})
	if !errors.Is(err, os.ErrPermission) || errors.Is(err, ErrCorrupt) || out != nil {
		t.Fatalf("CreateObject = %+v, %v; want nil and ErrPermission", out, err)
	}
	if got := listDir(t, o); len(got) != 1 || got[0] != oldData {
		t.Fatalf("object dir = %v, want only %s", got, oldData)
	}

	if err := os.Chmod(metaPath, 0o600); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	if body, obj := getObject(t, b, "k"); body != "old" || obj.ContentType != "old/type" {
		t.Fatalf("got %q (%s), want old (old/type)", body, obj.ContentType)
	}
}
