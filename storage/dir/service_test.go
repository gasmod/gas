package dir_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/storage"
	"github.com/gasmod/gas/storage/dir"
)

const testBucket = "test-bucket"

// pngData is a PNG signature followed by enough bytes to exceed the 512-byte
// sniffing window, so round-trips cover both the sniffed head and the rest.
var pngData = append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte("z"), 2000)...)

// newTestService returns an initialized *dir.Service rooted at a fresh temp
// directory, and that directory's path.
func newTestService(t *testing.T, opts ...dir.Option) (*dir.Service, string) {
	t.Helper()

	path := t.TempDir()
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Logf("close root: %v", closeErr)
		}
	})

	svc := dir.New(append([]dir.Option{dir.WithRoot(root)}, opts...)...)(nil, gas.NewNopLogger()())
	if err := svc.Init(); err != nil {
		t.Fatalf("init service: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := svc.Close(); closeErr != nil {
			t.Logf("close service: %v", closeErr)
		}
	})

	return svc, path
}

func upload(t *testing.T, svc *dir.Service, key string, data []byte, opts ...gas.StorageOption) {
	t.Helper()
	opts = append([]gas.StorageOption{gas.InBucket(testBucket)}, opts...)
	if err := svc.Upload(context.Background(), key, bytes.NewReader(data), opts...); err != nil {
		t.Fatalf("upload %q: %v", key, err)
	}
}

func download(t *testing.T, svc *dir.Service, key string) (*gas.StorageObject, []byte) {
	t.Helper()
	obj, err := svc.Download(context.Background(), key, gas.InBucket(testBucket))
	if err != nil {
		t.Fatalf("download %q: %v", key, err)
	}
	defer func() {
		if closeErr := obj.Body.Close(); closeErr != nil {
			t.Errorf("close body: %v", closeErr)
		}
	}()
	body, err := io.ReadAll(obj.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return obj, body
}

func listDir(t *testing.T, path string) []string {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatalf("read dir %q: %v", path, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

type errReader struct {
	err  error
	data []byte
}

// Read returns data once, then err.
func (r *errReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, r.err
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}

type fakePresigner struct {
	err         error
	bucket, key string
	ttl         time.Duration
}

func (p *fakePresigner) PresignGetObject(bucket, key string, ttl time.Duration) (string, error) {
	p.bucket, p.key, p.ttl = bucket, key, ttl
	return "get://" + bucket + "/" + key, p.err
}

func (p *fakePresigner) PresignPutObject(bucket, key string, ttl time.Duration) (string, error) {
	p.bucket, p.key, p.ttl = bucket, key, ttl
	return "put://" + bucket + "/" + key, p.err
}

func TestName(t *testing.T) {
	svc, _ := newTestService(t)
	if got := svc.Name(); got != "gas/storage/dir" {
		t.Errorf("Name = %q, want %q", got, "gas/storage/dir")
	}
}

func TestInit_NoRoot(t *testing.T) {
	svc := dir.New()(nil, gas.NewNopLogger()())
	if err := svc.Init(); !errors.Is(err, storage.ErrNotInitialized) {
		t.Errorf("Init = %v, want ErrNotInitialized", err)
	}
	if err := svc.CheckReady(context.Background()); !errors.Is(err, storage.ErrNotInitialized) {
		t.Errorf("CheckReady = %v, want ErrNotInitialized", err)
	}
}

func TestClose_RejectsOperations(t *testing.T) {
	svc, _ := newTestService(t)
	if err := svc.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	ctx := context.Background()
	b := gas.InBucket(testBucket)
	if err := svc.CheckReady(ctx); !errors.Is(err, storage.ErrClosed) {
		t.Errorf("CheckReady = %v, want ErrClosed", err)
	}
	if err := svc.Upload(ctx, "k", strings.NewReader("x"), b); !errors.Is(err, storage.ErrClosed) {
		t.Errorf("Upload = %v, want ErrClosed", err)
	}
	if _, err := svc.Download(ctx, "k", b); !errors.Is(err, storage.ErrClosed) {
		t.Errorf("Download = %v, want ErrClosed", err)
	}
	if _, err := svc.Head(ctx, "k", b); !errors.Is(err, storage.ErrClosed) {
		t.Errorf("Head = %v, want ErrClosed", err)
	}
	if err := svc.Delete(ctx, "k", b); !errors.Is(err, storage.ErrClosed) {
		t.Errorf("Delete = %v, want ErrClosed", err)
	}
	if _, err := svc.PresignDownloadURL(
		ctx,
		"k",
		time.Minute,
		b,
	); !errors.Is(
		err,
		storage.ErrClosed,
	) {
		t.Errorf("PresignDownloadURL = %v, want ErrClosed", err)
	}
	if _, err := svc.PresignUploadURL(
		ctx,
		"k",
		time.Minute,
		b,
	); !errors.Is(
		err,
		storage.ErrClosed,
	) {
		t.Errorf("PresignUploadURL = %v, want ErrClosed", err)
	}
}

func TestInit_AfterClose(t *testing.T) {
	svc, _ := newTestService(t)
	if err := svc.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := svc.Init(); err != nil {
		t.Fatalf("re-init: %v", err)
	}
	if err := svc.CheckReady(context.Background()); err != nil {
		t.Errorf("CheckReady after re-init = %v, want nil", err)
	}
}

func TestUploadDownload_RoundTrip(t *testing.T) {
	svc, _ := newTestService(t)
	upload(t, svc, "image.png", pngData)

	obj, body := download(t, svc, "image.png")
	if !bytes.Equal(body, pngData) {
		t.Errorf("body mismatch: got %d bytes, want %d", len(body), len(pngData))
	}
	if obj.Size != int64(len(pngData)) {
		t.Errorf("Size = %d, want %d", obj.Size, len(pngData))
	}
	if obj.ContentType != "image/png" {
		t.Errorf("ContentType = %q, want %q", obj.ContentType, "image/png")
	}
}

func TestUpload_SmallBody(t *testing.T) {
	svc, _ := newTestService(t)
	upload(t, svc, "small.txt", []byte("hello"))

	obj, body := download(t, svc, "small.txt")
	if string(body) != "hello" {
		t.Errorf("body = %q, want %q", body, "hello")
	}
	if obj.ContentType != "text/plain; charset=utf-8" {
		t.Errorf("ContentType = %q, want %q", obj.ContentType, "text/plain; charset=utf-8")
	}
}

func TestUpload_EmptyBody(t *testing.T) {
	svc, _ := newTestService(t)
	upload(t, svc, "empty", nil)

	obj, body := download(t, svc, "empty")
	if len(body) != 0 || obj.Size != 0 {
		t.Errorf("got %d bytes, Size = %d, want empty", len(body), obj.Size)
	}
	if obj.ContentType != "application/octet-stream" {
		t.Errorf("ContentType = %q, want %q", obj.ContentType, "application/octet-stream")
	}
}

func TestUpload_ExplicitContentTypeWins(t *testing.T) {
	svc, _ := newTestService(t)
	upload(t, svc, "image.png", pngData, gas.WithContentType("application/x-custom"))

	obj, _ := download(t, svc, "image.png")
	if obj.ContentType != "application/x-custom" {
		t.Errorf("ContentType = %q, want %q", obj.ContentType, "application/x-custom")
	}
}

func TestUpload_Metadata(t *testing.T) {
	svc, _ := newTestService(t)
	meta := map[string]string{"owner": "alice", "Content-Type": "user-value"}
	want := maps.Clone(meta)

	upload(t, svc, "k", []byte("x"), gas.WithMetadata(meta), gas.WithContentType("text/plain"))

	if !maps.Equal(meta, want) {
		t.Errorf("caller metadata mutated: %v, want %v", meta, want)
	}

	obj, _ := download(t, svc, "k")
	if !maps.Equal(obj.Metadata, want) {
		t.Errorf("Download Metadata = %v, want %v", obj.Metadata, want)
	}
	if obj.ContentType != "text/plain" {
		t.Errorf("ContentType = %q, want %q", obj.ContentType, "text/plain")
	}

	info, err := svc.Head(context.Background(), "k", gas.InBucket(testBucket))
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	if !maps.Equal(info.Metadata, want) {
		t.Errorf("Head Metadata = %v, want %v", info.Metadata, want)
	}
}

func TestUpload_Overwrite(t *testing.T) {
	svc, _ := newTestService(t)
	upload(t, svc, "k", pngData, gas.WithMetadata(map[string]string{"v": "1"}))

	html := []byte("<html><body>x</body></html>")
	upload(t, svc, "k", html, gas.WithMetadata(map[string]string{"v": "2"}))

	obj, body := download(t, svc, "k")
	if !bytes.Equal(body, html) {
		t.Errorf("body = %q, want %q", body, html)
	}
	if obj.ContentType != "text/html; charset=utf-8" {
		t.Errorf("ContentType = %q, want %q", obj.ContentType, "text/html; charset=utf-8")
	}
	if obj.Metadata["v"] != "2" {
		t.Errorf("Metadata[v] = %q, want %q", obj.Metadata["v"], "2")
	}
}

func TestUpload_NestedKey(t *testing.T) {
	svc, _ := newTestService(t)
	upload(t, svc, "a/b/c.txt", []byte("nested"))

	_, body := download(t, svc, "a/b/c.txt")
	if string(body) != "nested" {
		t.Errorf("body = %q, want %q", body, "nested")
	}
}

func TestUpload_ReaderErrorLeavesNoFiles(t *testing.T) {
	for _, size := range []int{0, 100, 600} {
		t.Run(fmt.Sprintf("%d bytes", size), func(t *testing.T) {
			svc, path := newTestService(t)
			readErr := errors.New("boom")

			err := svc.Upload(context.Background(), "k",
				&errReader{data: bytes.Repeat([]byte("x"), size), err: readErr},
				gas.InBucket(testBucket))
			if !errors.Is(err, readErr) {
				t.Fatalf("Upload = %v, want %v", err, readErr)
			}

			if names := listDir(t, filepath.Join(path, testBucket)); len(names) != 0 {
				t.Errorf("leftover files: %v", names)
			}
			if _, err := svc.Head(
				context.Background(),
				"k",
				gas.InBucket(testBucket),
			); !errors.Is(
				err,
				storage.ErrKeyNotFound,
			) {
				t.Errorf("Head = %v, want ErrKeyNotFound", err)
			}
		})
	}
}

func TestUpload_ReaderErrorKeepsPreviousObject(t *testing.T) {
	svc, _ := newTestService(t)
	upload(t, svc, "k", []byte("original"))

	readErr := errors.New("boom")
	err := svc.Upload(context.Background(), "k",
		&errReader{data: []byte("partial"), err: readErr}, gas.InBucket(testBucket))
	if !errors.Is(err, readErr) {
		t.Fatalf("Upload = %v, want %v", err, readErr)
	}

	_, body := download(t, svc, "k")
	if string(body) != "original" {
		t.Errorf("body = %q, want %q", body, "original")
	}
}

func TestBucketIsolation(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	if err := svc.Upload(ctx, "k", strings.NewReader("one"), gas.InBucket("one")); err != nil {
		t.Fatalf("upload one: %v", err)
	}
	if err := svc.Upload(ctx, "k", strings.NewReader("two"), gas.InBucket("two")); err != nil {
		t.Fatalf("upload two: %v", err)
	}

	for _, bucket := range []string{"one", "two"} {
		obj, err := svc.Download(ctx, "k", gas.InBucket(bucket))
		if err != nil {
			t.Fatalf("download %s: %v", bucket, err)
		}
		body, err := io.ReadAll(obj.Body)
		if closeErr := obj.Body.Close(); closeErr != nil {
			t.Errorf("close body: %v", closeErr)
		}
		if err != nil {
			t.Fatalf("read %s: %v", bucket, err)
		}
		if string(body) != bucket {
			t.Errorf("bucket %s body = %q, want %q", bucket, body, bucket)
		}
	}
}

func TestBucketRequired(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	if err := svc.Upload(
		ctx,
		"k",
		strings.NewReader("x"),
	); !errors.Is(
		err,
		storage.ErrBucketRequired,
	) {
		t.Errorf("Upload = %v, want ErrBucketRequired", err)
	}
	if _, err := svc.Download(ctx, "k"); !errors.Is(err, storage.ErrBucketRequired) {
		t.Errorf("Download = %v, want ErrBucketRequired", err)
	}
	if _, err := svc.Head(ctx, "k"); !errors.Is(err, storage.ErrBucketRequired) {
		t.Errorf("Head = %v, want ErrBucketRequired", err)
	}
	if err := svc.Delete(ctx, "k"); !errors.Is(err, storage.ErrBucketRequired) {
		t.Errorf("Delete = %v, want ErrBucketRequired", err)
	}
}

func TestInvalidPath(t *testing.T) {
	tests := []struct {
		name, bucket, key string
	}{
		{"key escapes bucket", testBucket, "../other/k"},
		{"key escapes root", testBucket, "../../k"},
		{"absolute key", testBucket, "/etc/passwd"},
		{"empty key", testBucket, ""},
		{"metadata file key", testBucket, ".__GAS_FILE_METADATA__k.json"},
		{"nested metadata file key", testBucket, "a/.__GAS_FILE_METADATA__k.json"},
		{"temp file key", testBucket, ".__GAS_FILE_TMP__abc"},
		{"bucket with slash", "a/b", "k"},
		{"bucket with backslash", `a\b`, "k"},
		{"bucket parent", "..", "k"},
		{"bucket absolute", "/tmp", "k"},
	}

	svc, _ := newTestService(t, dir.WithPresigner(&fakePresigner{}))
	ctx := context.Background()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := gas.InBucket(tt.bucket)
			if err := svc.Upload(
				ctx,
				tt.key,
				strings.NewReader("x"),
				b,
			); !errors.Is(
				err,
				dir.ErrInvalidPath,
			) {
				t.Errorf("Upload = %v, want ErrInvalidPath", err)
			}
			if _, err := svc.Download(ctx, tt.key, b); !errors.Is(err, dir.ErrInvalidPath) {
				t.Errorf("Download = %v, want ErrInvalidPath", err)
			}
			if _, err := svc.Head(ctx, tt.key, b); !errors.Is(err, dir.ErrInvalidPath) {
				t.Errorf("Head = %v, want ErrInvalidPath", err)
			}
			if err := svc.Delete(ctx, tt.key, b); !errors.Is(err, dir.ErrInvalidPath) {
				t.Errorf("Delete = %v, want ErrInvalidPath", err)
			}
			if _, err := svc.PresignDownloadURL(
				ctx,
				tt.key,
				time.Minute,
				b,
			); !errors.Is(
				err,
				dir.ErrInvalidPath,
			) {
				t.Errorf("PresignDownloadURL = %v, want ErrInvalidPath", err)
			}
			if _, err := svc.PresignUploadURL(
				ctx,
				tt.key,
				time.Minute,
				b,
			); !errors.Is(
				err,
				dir.ErrInvalidPath,
			) {
				t.Errorf("PresignUploadURL = %v, want ErrInvalidPath", err)
			}
		})
	}
}

func TestKeyNotFound(t *testing.T) {
	svc, _ := newTestService(t)
	upload(t, svc, "folder/k", []byte("x"))

	ctx := context.Background()
	b := gas.InBucket(testBucket)

	for _, key := range []string{"missing", "folder"} {
		if _, err := svc.Download(ctx, key, b); !errors.Is(err, storage.ErrKeyNotFound) {
			t.Errorf("Download(%q) = %v, want ErrKeyNotFound", key, err)
		}
		if _, err := svc.Head(ctx, key, b); !errors.Is(err, storage.ErrKeyNotFound) {
			t.Errorf("Head(%q) = %v, want ErrKeyNotFound", key, err)
		}
	}
}

func TestHead(t *testing.T) {
	svc, _ := newTestService(t)
	before := time.Now().Add(-time.Second)
	upload(t, svc, "image.png", pngData, gas.WithMetadata(map[string]string{"k": "v"}))

	info, err := svc.Head(context.Background(), "image.png", gas.InBucket(testBucket))
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	if info.Size != int64(len(pngData)) {
		t.Errorf("Size = %d, want %d", info.Size, len(pngData))
	}
	if info.ContentType != "image/png" {
		t.Errorf("ContentType = %q, want %q", info.ContentType, "image/png")
	}
	if info.Metadata["k"] != "v" || len(info.Metadata) != 1 {
		t.Errorf("Metadata = %v, want map[k:v]", info.Metadata)
	}
	if info.LastModified.Before(before) {
		t.Errorf("LastModified = %v, want after %v", info.LastModified, before)
	}
}

func TestDelete(t *testing.T) {
	svc, path := newTestService(t)
	upload(t, svc, "k", []byte("x"))

	ctx := context.Background()
	b := gas.InBucket(testBucket)
	if err := svc.Delete(ctx, "k", b); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := svc.Head(ctx, "k", b); !errors.Is(err, storage.ErrKeyNotFound) {
		t.Errorf("Head after delete = %v, want ErrKeyNotFound", err)
	}
	if names := listDir(t, filepath.Join(path, testBucket)); len(names) != 0 {
		t.Errorf("leftover files after delete: %v", names)
	}
}

func TestDelete_Missing(t *testing.T) {
	svc, _ := newTestService(t)
	if err := svc.Delete(context.Background(), "missing", gas.InBucket(testBucket)); err != nil {
		t.Errorf("Delete missing = %v, want nil", err)
	}
}

func TestPresign_NoPresigner(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	b := gas.InBucket(testBucket)

	if _, err := svc.PresignDownloadURL(
		ctx,
		"k",
		time.Minute,
		b,
	); !errors.Is(
		err,
		dir.ErrNoPresigner,
	) {
		t.Errorf("PresignDownloadURL = %v, want ErrNoPresigner", err)
	}
	if _, err := svc.PresignUploadURL(
		ctx,
		"k",
		time.Minute,
		b,
	); !errors.Is(
		err,
		dir.ErrNoPresigner,
	) {
		t.Errorf("PresignUploadURL = %v, want ErrNoPresigner", err)
	}
}

func TestPresign(t *testing.T) {
	p := &fakePresigner{}
	svc, _ := newTestService(t, dir.WithPresigner(p))
	ctx := context.Background()
	b := gas.InBucket(testBucket)

	got, err := svc.PresignDownloadURL(ctx, "a/k", time.Minute, b)
	if err != nil {
		t.Fatalf("PresignDownloadURL: %v", err)
	}
	if want := "get://" + testBucket + "/a/k"; got != want {
		t.Errorf("PresignDownloadURL = %q, want %q", got, want)
	}
	if p.bucket != testBucket || p.key != "a/k" || p.ttl != time.Minute {
		t.Errorf(
			"presigner got (%q, %q, %v), want (%q, %q, %v)",
			p.bucket,
			p.key,
			p.ttl,
			testBucket,
			"a/k",
			time.Minute,
		)
	}

	got, err = svc.PresignUploadURL(ctx, "a/k", time.Hour, b)
	if err != nil {
		t.Fatalf("PresignUploadURL: %v", err)
	}
	if want := "put://" + testBucket + "/a/k"; got != want {
		t.Errorf("PresignUploadURL = %q, want %q", got, want)
	}
	if p.ttl != time.Hour {
		t.Errorf("presigner ttl = %v, want %v", p.ttl, time.Hour)
	}
}

func TestPresign_Error(t *testing.T) {
	presignErr := errors.New("presign failed")
	svc, _ := newTestService(t, dir.WithPresigner(&fakePresigner{err: presignErr}))
	ctx := context.Background()
	b := gas.InBucket(testBucket)

	if _, err := svc.PresignDownloadURL(ctx, "k", time.Minute, b); !errors.Is(err, presignErr) {
		t.Errorf("PresignDownloadURL = %v, want %v", err, presignErr)
	}
	if _, err := svc.PresignUploadURL(ctx, "k", time.Minute, b); !errors.Is(err, presignErr) {
		t.Errorf("PresignUploadURL = %v, want %v", err, presignErr)
	}
}
