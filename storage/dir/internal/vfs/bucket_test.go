package vfs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestCreateObject_RoundTrip checks that bodies, content types and metadata
// read back as written.
func TestCreateObject_RoundTrip(t *testing.T) {
	large := bytes.Repeat([]byte("0123456789abcdef"), 1024) // 16 KiB
	png := append([]byte("\x89PNG\x0d\x0a\x1a\x0a"), make([]byte, 600)...)
	head := large[:fileHeadSize]
	html := []byte("<html><body>x</body></html>")
	const plain, htmlType = "text/plain; charset=utf-8", "text/html; charset=utf-8"

	tests := []struct {
		body        io.Reader
		contentType *string
		name        string
		wantType    string
		wantBody    []byte
	}{
		{bytes.NewReader(html), new("a/b"), "explicit type", "a/b", html},
		{bytes.NewReader(html), new(""), "empty explicit type sniffs", htmlType, html},
		{strings.NewReader("hello"), nil, "sniff text", plain, []byte("hello")},
		{bytes.NewReader(html), nil, "sniff html", htmlType, html},
		{bytes.NewReader(png), nil, "sniff png larger than head", "image/png", png},
		{bytes.NewReader(large), nil, "large body", plain, large},
		{bytes.NewReader(large), new("a/b"), "large body explicit type", "a/b", large},
		{bytes.NewReader(head), nil, "exactly head size", plain, head},
		{strings.NewReader(""), nil, "empty body", plain, []byte{}},
		// Bug 7: a nil body is an empty object, as in the s3 provider.
		{nil, nil, "nil body", plain, []byte{}},
		{nil, new("text/csv"), "nil body explicit type", "text/csv", []byte{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, _ := newTestVFS(t)
			b := newTestBucket(t, v)
			md := map[string]string{"a": "1"}

			noPanic(t, func() {
				out, err := b.CreateObject(t.Context(), &CreateObjectInput{
					Key: "dir/key.txt", Body: tt.body, ContentType: tt.contentType, Metadata: md,
				})
				if err != nil {
					t.Fatalf("CreateObject: %v", err)
				}
				if out.ContentType != tt.wantType || out.Metadata["a"] != "1" {
					t.Fatalf("CreateObject = %+v, want type %q, metadata a=1", out, tt.wantType)
				}
			})

			body, obj := getObject(t, b, "dir/key.txt")
			if !bytes.Equal([]byte(body), tt.wantBody) {
				t.Fatalf("body = %d bytes, want %d", len(body), len(tt.wantBody))
			}
			if obj.ContentType != tt.wantType || obj.Metadata["a"] != "1" {
				t.Fatalf("OpenObject = %+v, want type %q", obj.ObjectMetadata, tt.wantType)
			}
		})
	}
}

// TestCreateObject_Overwrite checks that an overwrite leaves one metadata and
// one data file.
func TestCreateObject_Overwrite(t *testing.T) {
	v, dir := newTestVFS(t)
	b := newTestBucket(t, v)
	putObject(t, b, "k", "first", "a/a")
	putObject(t, b, "k", "second", "b/b")

	body, obj := getObject(t, b, "k")
	if body != "second" || obj.ContentType != "b/b" {
		t.Fatalf("got %q (%s), want second (b/b)", body, obj.ContentType)
	}
	m, o, _ := bucketPaths(dir)
	if got := listDir(t, o); len(got) != 1 {
		t.Fatalf("object dir = %v, want one file", got)
	}
	if got := listDir(t, m); len(got) != 1 {
		t.Fatalf("meta dir = %v, want one file", got)
	}
}

// TestOpenObject_MissingKey checks that a missing key is ErrObjectNotFound
// and leaves no lock file or slot.
func TestOpenObject_MissingKey(t *testing.T) {
	v, dir := newTestVFS(t)
	b := newTestBucket(t, v)

	for i := range 10 {
		_, err := b.OpenObject(t.Context(), &OpenObjectInput{Key: fmt.Sprintf("missing-%d", i)})
		if !errors.Is(err, ErrObjectNotFound) || !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("OpenObject error = %v, want ErrObjectNotFound wrapping ErrNotExist", err)
		}
	}
	_, _, l := bucketPaths(dir)
	if got := listDir(t, l); len(got) != 0 {
		t.Fatalf("lock dir = %v after lookups of missing keys, want empty", got)
	}
	if n := slotCount(b.lock); n != 0 {
		t.Fatalf("%d slots left, want 0", n)
	}
}

// TestOpenObject_MissingDataFile checks that metadata naming a missing data
// file is ErrCorrupt and gives the lock back.
func TestOpenObject_MissingDataFile(t *testing.T) {
	v, dir := newTestVFS(t)
	b := newTestBucket(t, v)
	putObject(t, b, "k", "x", "")
	_, o, _ := bucketPaths(dir)
	if err := os.Remove(filepath.Join(o, dataFile(t, b, "k"))); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	// Metadata that names a missing data file is corruption, not a missing
	// object.
	_, err := b.OpenObject(t.Context(), &OpenObjectInput{Key: "k"})
	if !errors.Is(err, ErrCorrupt) || errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("OpenObject error = %v, want ErrCorrupt and not ErrObjectNotFound", err)
	}
	// The lock must have been given back.
	if err := b.lock.Lock(t.Context(), hash("k")); err != nil {
		t.Fatalf("Lock after failed OpenObject: %v", err)
	}
	if err := b.lock.Unlock(hash("k")); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
}

// corruptMetadata lists metadata file contents that must be rejected as
// corrupt. A data file name must be the key hash, "_" and a canonical UUID,
// so metadata cannot point at another object's data or outside the bucket.
func corruptMetadata() []string {
	h := hash("k")
	id := "123e4567-e89b-12d3-a456-426614174000"
	return []string{
		"null", "", "{not json", "  null  ", `{"Key":"k"}`, `["k"]`,
		`{"Metadata":{"a":1},"DataFile":"` + h + "_" + id + `"}`,
		`{"DataFile":"../` + h + "_" + id + `"}`,
		`{"DataFile":"` + h + "_" + id + `/x"}`,
		`{"DataFile":"` + hash("other") + "_" + id + `"}`,
		`{"DataFile":"` + h + `_not-a-uuid"}`,
		`{"DataFile":"` + h + "_" + strings.ToUpper(id) + `"}`,
		`{"DataFile":"` + h + "_{" + id + `}"}`,
		`{"DataFile":"` + h + `"}`,
	}
}

// TestCorruptMetadata covers bug 7: corrupt metadata, including JSON null, is
// an error, never a panic and never "not found".
func TestCorruptMetadata(t *testing.T) {
	for _, content := range corruptMetadata() {
		t.Run(fmt.Sprintf("%q", content), func(t *testing.T) {
			v, dir := newTestVFS(t)
			b := newTestBucket(t, v)
			writeMeta(t, dir, "k", content)

			noPanic(t, func() {
				_, err := b.OpenObject(t.Context(), &OpenObjectInput{Key: "k"})
				if !errors.Is(err, ErrCorrupt) || errors.Is(err, ErrObjectNotFound) {
					t.Fatalf(
						"OpenObject error = %v, want ErrCorrupt and not ErrObjectNotFound",
						err,
					)
				}
			})
			noPanic(t, func() {
				err := b.DeleteObject(t.Context(), &DeleteObjectInput{Key: "k"})
				if !errors.Is(err, ErrCorrupt) || errors.Is(err, ErrObjectNotFound) {
					t.Fatalf(
						"DeleteObject error = %v, want ErrCorrupt and not ErrObjectNotFound",
						err,
					)
				}
			})
		})
	}
}

// TestCorruptMetadata_Directory checks that a directory where the metadata
// file belongs is corruption, not a missing object and not a plain read
// error.
func TestCorruptMetadata_Directory(t *testing.T) {
	v, dir := newTestVFS(t)
	b := newTestBucket(t, v)
	m, _, _ := bucketPaths(dir)
	if err := os.Mkdir(filepath.Join(m, hash("k")+metaFileExt), 0o750); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	_, err := b.OpenObject(t.Context(), &OpenObjectInput{Key: "k"})
	if !errors.Is(err, ErrCorrupt) || errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("OpenObject error = %v, want ErrCorrupt and not ErrObjectNotFound", err)
	}
}

// TestCorruptMetadata_ErrorOmitsContents checks that metadata is data read
// back from storage: an error about it must not quote the file's contents,
// which hold the user's key and metadata.
func TestCorruptMetadata_ErrorOmitsContents(t *testing.T) {
	const secret = "secret-metadata-key"
	contents := []string{
		`{"Key":"` + secret + `","Metadata":{"` + secret + `":1}}`,
		`{"Key":"` + secret + `","Metadata":{"` + secret + `":"v"}`,
		`{"Key":"` + secret + `"} trailing ` + secret,
		`{"Key":"` + secret + `","DataFile":"` + secret + `"}`,
	}
	for _, content := range contents {
		v, dir := newTestVFS(t)
		b := newTestBucket(t, v)
		writeMeta(t, dir, "k", content)

		_, err := b.OpenObject(t.Context(), &OpenObjectInput{Key: "k"})
		if !errors.Is(err, ErrCorrupt) {
			t.Fatalf("OpenObject error = %v, want ErrCorrupt", err)
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("OpenObject error %q quotes the metadata file", err)
		}
	}
}

// TestOpenObject_DataFileNotRegular checks that a data file that is not a
// regular file is corruption; its descriptor is not leaked and the lock is
// given back.
func TestOpenObject_DataFileNotRegular(t *testing.T) {
	v, dir := newTestVFS(t)
	b := newTestBucket(t, v)
	putObject(t, b, "k", "x", "")
	_, o, _ := bucketPaths(dir)
	data := filepath.Join(o, dataFile(t, b, "k"))
	if err := os.Remove(data); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := os.Mkdir(data, 0o750); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	_, err := b.OpenObject(t.Context(), &OpenObjectInput{Key: "k"})
	if !errors.Is(err, ErrCorrupt) || errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("OpenObject error = %v, want ErrCorrupt and not ErrObjectNotFound", err)
	}
	if err := b.lock.Lock(t.Context(), hash("k")); err != nil {
		t.Fatalf("Lock after failed OpenObject: %v", err)
	}
	if err := b.lock.Unlock(hash("k")); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
}

// TestOpenObject_Info checks that OpenObject reports the data file's size and
// modification time.
func TestOpenObject_Info(t *testing.T) {
	v, dir := newTestVFS(t)
	b := newTestBucket(t, v)
	putObject(t, b, "k", "hello", "")
	_, o, _ := bucketPaths(dir)
	fi, err := os.Stat(filepath.Join(o, dataFile(t, b, "k")))
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	_, obj := getObject(t, b, "k")
	if obj.Size != 5 || !obj.LastModified.Equal(fi.ModTime()) {
		t.Fatalf("OpenObject = size %d modified %v, want 5 and %v", obj.Size, obj.LastModified,
			fi.ModTime())
	}
}

// TestCreateObject_FailingBody covers bug 4: a failed Put leaves no temp
// files and the previous object intact.
func TestCreateObject_FailingBody(t *testing.T) {
	boom := errors.New("boom")
	twoHeads := make([]byte, 2*fileHeadSize)
	tests := []struct {
		body        func() io.Reader
		contentType *string
		name        string
	}{
		{func() io.Reader {
			return io.MultiReader(strings.NewReader("partial"), &errReader{err: boom})
		}, new("text/plain"), "fails in copy"},
		{func() io.Reader {
			return io.MultiReader(bytes.NewReader(twoHeads), &errReader{err: boom})
		}, nil, "fails after head"},
		{func() io.Reader {
			return &errReader{data: []byte("abc"), err: boom}
		}, nil, "fails in head"},
		// Bug 8: the error arrives with the bytes that complete the head.
		{func() io.Reader {
			return &errReader{data: bytes.Repeat([]byte("a"), fileHeadSize), err: boom}
		}, nil, "fails with final head bytes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, dir := newTestVFS(t)
			b := newTestBucket(t, v)
			putObject(t, b, "k", "old", "old/type")
			oldData := dataFile(t, b, "k")

			for range 3 {
				_, err := b.CreateObject(t.Context(), &CreateObjectInput{
					Key: "k", Body: tt.body(), ContentType: tt.contentType,
				})
				if !errors.Is(err, boom) {
					t.Fatalf("CreateObject error = %v, want boom", err)
				}
			}

			m, o, l := bucketPaths(dir)
			if got := listDir(t, o); len(got) != 1 || got[0] != oldData {
				t.Fatalf("object dir = %v, want only %s", got, oldData)
			}
			if got := listDir(t, m); len(got) != 1 || got[0] != hash("k")+metaFileExt {
				t.Fatalf("meta dir = %v, want only the meta file", got)
			}
			if got := listDir(t, l); len(got) != 1 {
				t.Fatalf("lock dir = %v, want one lock file", got)
			}
			if body, obj := getObject(t, b, "k"); body != "old" || obj.ContentType != "old/type" {
				t.Fatalf("got %q (%s), want old (old/type)", body, obj.ContentType)
			}
		})
	}
}

// TestCreateObject_MetaRenameFails covers bug 4: a failed metadata rename
// removes the temp metadata file.
func TestCreateObject_MetaRenameFails(t *testing.T) {
	v, dir := newTestVFS(t)
	b := newTestBucket(t, v)
	m, o, _ := bucketPaths(dir)
	blocker := filepath.Join(m, hash("k")+metaFileExt)
	if err := os.MkdirAll(filepath.Join(blocker, "x"), 0o750); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	for range 3 {
		in := &CreateObjectInput{Key: "k", Body: strings.NewReader("x")}
		out, err := b.CreateObject(t.Context(), in)
		var le *os.LinkError
		if !errors.As(err, &le) || out != nil {
			t.Fatalf("CreateObject = %+v, %v; want nil and the rename's *os.LinkError", out, err)
		}
	}
	if got := listDir(t, m); len(got) != 1 || got[0] != hash("k")+metaFileExt {
		t.Fatalf("meta dir = %v, want only the blocking dir", got)
	}
	// The uncommitted data files are discarded with the metadata.
	if got := listDir(t, o); len(got) != 0 {
		t.Fatalf("object dir = %v, want empty", got)
	}
}

// TestCreateObject_MetaWriteFails checks that an unwritable metadata dir
// fails the Put and leaves no files.
func TestCreateObject_MetaWriteFails(t *testing.T) {
	skipIfNoPermissions(t)
	v, dir := newTestVFS(t)
	b := newTestBucket(t, v)
	m, o, _ := bucketPaths(dir)
	readOnlyDir(t, m)

	in := &CreateObjectInput{Key: "k", Body: strings.NewReader("x")}
	_, err := b.CreateObject(t.Context(), in)
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("CreateObject error = %v, want ErrPermission", err)
	}
	if got := listDir(t, m); len(got) != 0 {
		t.Fatalf("meta dir = %v, want empty", got)
	}
	if got := listDir(t, o); len(got) != 0 {
		t.Fatalf("object dir = %v, want empty", got)
	}
}

// TestCreateObject_DataDirNotWritable checks that an unwritable objects dir
// fails the Put with ErrPermission.
func TestCreateObject_DataDirNotWritable(t *testing.T) {
	skipIfNoPermissions(t)
	v, dir := newTestVFS(t)
	b := newTestBucket(t, v)
	_, o, _ := bucketPaths(dir)
	readOnlyDir(t, o)

	in := &CreateObjectInput{Key: "k", Body: strings.NewReader("x")}
	_, err := b.CreateObject(t.Context(), in)
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("CreateObject error = %v, want ErrPermission", err)
	}
}

// TestDeleteObject checks Delete of an existing and of a missing key.
func TestDeleteObject(t *testing.T) {
	t.Run("existing", func(t *testing.T) {
		v, dir := newTestVFS(t)
		b := newTestBucket(t, v)
		putObject(t, b, "k", "x", "")
		putObject(t, b, "other", "y", "")
		if err := b.DeleteObject(t.Context(), &DeleteObjectInput{Key: "k"}); err != nil {
			t.Fatalf("DeleteObject: %v", err)
		}
		_, err := b.OpenObject(t.Context(), &OpenObjectInput{Key: "k"})
		if !errors.Is(err, ErrObjectNotFound) {
			t.Fatalf("OpenObject after delete = %v, want ErrObjectNotFound", err)
		}
		m, o, l := bucketPaths(dir)
		if got := listDir(t, m); len(got) != 1 {
			t.Fatalf("meta dir = %v, want only other", got)
		}
		if got := listDir(t, o); len(got) != 1 || got[0] != dataFile(t, b, "other") {
			t.Fatalf("object dir = %v, want only other's data file", got)
		}
		// Put leaves its lock file; Delete removes the deleted key's.
		if got := listDir(t, l); len(got) != 1 || got[0] != hash("other") {
			t.Fatalf("lock dir = %v, want only other's lock file", got)
		}
		if body, _ := getObject(t, b, "other"); body != "y" {
			t.Fatalf("other = %q, want y", body)
		}
	})

	// Bug 5: deleting a missing key leaves no lock file behind.
	t.Run("missing", func(t *testing.T) {
		v, dir := newTestVFS(t)
		b := newTestBucket(t, v)
		for i := range 5 {
			err := b.DeleteObject(t.Context(), &DeleteObjectInput{Key: fmt.Sprint(i)})
			if err != nil {
				t.Fatalf("DeleteObject: %v", err)
			}
		}
		_, _, l := bucketPaths(dir)
		if got := listDir(t, l); len(got) != 0 {
			t.Fatalf("lock dir = %v after deleting missing keys, want empty", got)
		}
	})
}

// TestDeleteObject_MetaRemovalFails checks that removing the metadata is the
// delete's commit point: when it fails, the data file is left alone, so the
// object stays whole instead of turning into metadata that names a missing
// data file.
func TestDeleteObject_MetaRemovalFails(t *testing.T) {
	skipIfNoPermissions(t)
	v, dir := newTestVFS(t)
	b := newTestBucket(t, v)
	putObject(t, b, "k", "x", "")
	m, o, _ := bucketPaths(dir)
	data := dataFile(t, b, "k")
	readOnlyDir(t, m)

	err := b.DeleteObject(t.Context(), &DeleteObjectInput{Key: "k"})
	var pe *os.PathError
	if !errors.Is(err, os.ErrPermission) || !errors.As(err, &pe) ||
		pe.Path != hash("k")+metaFileExt {
		t.Fatalf("DeleteObject error = %v, want ErrPermission removing the meta file", err)
	}
	if got := listDir(t, o); len(got) != 1 || got[0] != data {
		t.Fatalf("object dir = %v, want only %s", got, data)
	}
	if body, _ := getObject(t, b, "k"); body != "x" {
		t.Fatalf("body after failed delete = %q, want x", body)
	}
}

// TestDeleteObject_DataRemovalFails checks that when the metadata is gone but
// the data file cannot be removed, the object is deleted and the error names
// the data file.
func TestDeleteObject_DataRemovalFails(t *testing.T) {
	v, dir := newTestVFS(t)
	b := newTestBucket(t, v)
	putObject(t, b, "k", "x", "")
	_, o, _ := bucketPaths(dir)
	data := dataFile(t, b, "k")
	// A non-empty directory at the data file's name cannot be removed.
	if err := os.Remove(filepath.Join(o, data)); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(o, data, "x"), 0o750); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	err := b.DeleteObject(t.Context(), &DeleteObjectInput{Key: "k"})
	var pe *os.PathError
	if !errors.As(err, &pe) || pe.Path != data {
		t.Fatalf("DeleteObject error = %v, want a *os.PathError for %s", err, data)
	}
	if _, err := b.OpenObject(
		t.Context(),
		&OpenObjectInput{Key: "k"},
	); !errors.Is(
		err,
		ErrObjectNotFound,
	) {
		t.Fatalf("OpenObject after delete = %v, want ErrObjectNotFound", err)
	}
}

// assertAllOps checks that Put, Get and Delete of "k" all fail with want.
func assertAllOps(t *testing.T, ctx context.Context, b *Bucket, want error) {
	t.Helper()
	_, cErr := b.CreateObject(ctx, &CreateObjectInput{Key: "k"})
	_, oErr := b.OpenObject(ctx, &OpenObjectInput{Key: "k"})
	dErr := b.DeleteObject(ctx, &DeleteObjectInput{Key: "k"})
	errs := map[string]error{"CreateObject": cErr, "OpenObject": oErr, "DeleteObject": dErr}
	for op, err := range errs {
		if !errors.Is(err, want) {
			t.Errorf("%s = %v, want %v", op, err, want)
		}
	}
}

// TestBucket_ClosedOperations checks that every operation on a closed Bucket
// fails with os.ErrClosed.
func TestBucket_ClosedOperations(t *testing.T) {
	v, _ := newTestVFS(t)
	if _, err := v.CreateBucket(t.Context(), "b"); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	b, err := v.OpenBucket("b")
	if err != nil {
		t.Fatalf("OpenBucket: %v", err)
	}
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	assertAllOps(t, t.Context(), b, os.ErrClosed)
}

// TestBucket_ContextCanceled checks that every operation waiting on a held
// lock fails with the context's error.
func TestBucket_ContextCanceled(t *testing.T) {
	v, _ := newTestVFS(t)
	holder := newTestBucket(t, v)
	if err := holder.lock.Lock(t.Context(), hash("k")); err != nil {
		t.Fatalf("Lock: %v", err)
	}
	defer func() { _ = holder.lock.Unlock(hash("k")) }()

	b := newOpenBucket(t, v)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	assertAllOps(t, ctx, b, context.DeadlineExceeded)
}

// TestBucket_ConcurrentSameKey covers bug 1, end to end: concurrent Puts,
// Deletes and Gets on one key, each call on its own Bucket as the provider
// does. Every successful Get must return a body that matches its own
// metadata.
func TestBucket_ConcurrentSameKey(t *testing.T) {
	v, _ := newTestVFS(t)
	newTestBucket(t, v)
	deadline := time.Now().Add(2 * time.Second)

	var mu sync.Mutex
	var errs []error
	var gets int

	var wg sync.WaitGroup
	for w := range 12 {
		wg.Go(func() {
			for i := 0; time.Now().Before(deadline); i++ {
				got, err := sameKeyOp(v, w, i)
				mu.Lock()
				if err != nil {
					errs = append(errs, err)
				}
				if got {
					gets++
				}
				mu.Unlock()
			}
		})
	}
	wg.Wait()

	if len(errs) > 0 {
		t.Fatalf("%d operations failed; first: %v", len(errs), errs[0])
	}
	if gets == 0 {
		t.Fatal("no successful gets; the test exercised nothing")
	}
}

// sameKeyOp runs one Put, Delete or Get of key "k" on a fresh Bucket. got
// reports a successful Get.
func sameKeyOp(v *VFS, w, i int) (got bool, err error) {
	b, err := v.OpenBucket("b")
	if err != nil {
		return false, err
	}
	defer func() {
		if cErr := b.Close(); cErr != nil {
			err = errors.Join(err, fmt.Errorf("close: %w", cErr))
		}
	}()

	switch (w + i) % 4 {
	case 0:
		if dErr := b.DeleteObject(context.Background(), &DeleteObjectInput{Key: "k"}); dErr != nil {
			return false, fmt.Errorf("delete: %w", dErr)
		}
		return false, nil
	case 1:
		token := fmt.Sprintf("w%d-i%d", w, i)
		body := token + strings.Repeat("x", (w*i)%4096)
		if _, cErr := b.CreateObject(context.Background(), &CreateObjectInput{
			Key: "k", ContentType: &token, Body: strings.NewReader(body),
		}); cErr != nil {
			return false, fmt.Errorf("put: %w", cErr)
		}
		return false, nil
	}

	obj, err := b.OpenObject(context.Background(), &OpenObjectInput{Key: "k"})
	if errors.Is(err, ErrObjectNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get: %w", err)
	}
	body, rErr := io.ReadAll(obj.File)
	if cErr := obj.File.Close(); cErr != nil {
		return false, fmt.Errorf("close object: %w", cErr)
	}
	if rErr != nil {
		return false, fmt.Errorf("read: %w", rErr)
	}
	if string(body) != obj.ContentType && !strings.HasPrefix(string(body), obj.ContentType+"x") {
		return false, fmt.Errorf("body %.20q... served with content type %q", body, obj.ContentType)
	}
	return true, nil
}

// TestBucket_ReleaseFails checks that a lock file that cannot be removed
// after a lookup of a missing key is reported along with the miss, and
// DeleteObject of a missing key fails with it instead of succeeding.
func TestBucket_ReleaseFails(t *testing.T) {
	skipIfNoPermissions(t)
	v, dir := newTestVFS(t)
	b := newTestBucket(t, v)
	_, _, l := bucketPaths(dir)
	// An existing lock file can still be opened and locked in a read-only
	// dir, but not removed.
	if err := os.WriteFile(filepath.Join(l, hash("k")), nil, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	readOnlyDir(t, l)

	_, err := b.OpenObject(t.Context(), &OpenObjectInput{Key: "k"})
	if !errors.Is(err, ErrObjectNotFound) || !errors.Is(err, os.ErrPermission) {
		t.Fatalf("OpenObject = %v, want ErrObjectNotFound joined with ErrPermission", err)
	}
	err = b.DeleteObject(t.Context(), &DeleteObjectInput{Key: "k"})
	if !errors.Is(err, os.ErrPermission) || errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("DeleteObject = %v, want ErrPermission only", err)
	}
	// The lock was given back despite the failed removal.
	if err := b.lock.Lock(t.Context(), hash("k")); err != nil {
		t.Fatalf("Lock after failed release: %v", err)
	}
	if err := b.lock.Unlock(hash("k")); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
}
