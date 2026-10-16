package dir

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/gasmod/gas/storage"
)

// TestProvider_ContentLength checks that PutObject holds Body to a set
// ContentLength: a body that is shorter or longer fails with
// ErrContentLengthMismatch, stores nothing, keeps the previous object and
// leaves no data file behind, while a matching or nil length writes as usual.
func TestProvider_ContentLength(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		body     io.Reader
		length   *int64
		want     string
		mismatch bool
	}{
		{name: "exact", body: strings.NewReader("hello"), length: new(int64(5)), want: "hello"},
		{name: "nil length", body: strings.NewReader("hello"), length: nil, want: "hello"},
		{name: "zero with an empty body", body: strings.NewReader(""), length: new(int64(0)), want: ""},
		{name: "zero with a nil body", body: nil, length: new(int64(0)), want: ""},
		{name: "short", body: strings.NewReader("hel"), length: new(int64(5)), mismatch: true},
		{name: "long", body: strings.NewReader("hello world"), length: new(int64(5)), mismatch: true},
		{name: "long past the sniffed head", body: bytes.NewReader(make([]byte, 9000)), length: new(int64(600)), mismatch: true},
		{name: "short past the sniffed head", body: bytes.NewReader(make([]byte, 600)), length: new(int64(9000)), mismatch: true},
		{name: "nil body with a length", body: nil, length: new(int64(3)), mismatch: true},
		{name: "negative", body: strings.NewReader("hello"), length: new(int64(-1)), mismatch: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p, dir := newTestProvider(t)
			put(t, p, "k", []byte("previous"))
			dataBefore := listDir(t, objDir(dir, testBucket))
			metaBefore := readFile(t, metaFile(dir, "k"))

			info, err := p.PutObject(context.Background(), &storage.PutObjectInput{
				BaseObjectInput: base("k"),
				Body:            tc.body,
				ContentLength:   tc.length,
			})
			if tc.mismatch {
				if !errors.Is(err, storage.ErrContentLengthMismatch) || info != nil {
					t.Fatalf("PutObject() = %+v, %v; want nil, ErrContentLengthMismatch", info, err)
				}
				if _, data := get(t, p, "k"); string(data) != "previous" {
					t.Errorf("object after a rejected put = %q, want previous", data)
				}
				if after := listDir(t, objDir(dir, testBucket)); !slices.Equal(after, dataBefore) {
					t.Errorf("data files = %v after a rejected put, want %v", after, dataBefore)
				}
				if after := readFile(t, metaFile(dir, "k")); !bytes.Equal(after, metaBefore) {
					t.Errorf("metadata changed by a rejected put: %q, was %q", after, metaBefore)
				}
				assertNoTempFiles(t, dir)
				return
			}
			if err != nil {
				t.Fatalf("PutObject() error: %v", err)
			}
			if _, data := get(t, p, "k"); string(data) != tc.want {
				t.Errorf("object = %q, want %q", data, tc.want)
			}
		})
	}
}

// TestProvider_ContentLengthFreshKey checks that a mismatched PutObject of a
// key that does not exist yet leaves no trace: no metadata, data, temporary
// or lock file, and the key reads as not found.
func TestProvider_ContentLengthFreshKey(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		body   []byte
		length int64
	}{
		{name: "short", body: []byte("abc"), length: 5},
		{name: "long", body: []byte("abcdefg"), length: 5},
		{name: "long past the sniffed head", body: make([]byte, 9000), length: 600},
		{name: "negative", body: []byte("abc"), length: -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p, dir := newTestProvider(t)
			_, err := p.PutObject(context.Background(), &storage.PutObjectInput{
				BaseObjectInput: base("fresh"),
				Body:            bytes.NewReader(tc.body),
				ContentLength:   &tc.length,
			})
			if !errors.Is(err, storage.ErrContentLengthMismatch) {
				t.Fatalf("PutObject() error = %v, want ErrContentLengthMismatch", err)
			}
			// Checked before any read: reads of a missing key release its
			// lock file themselves.
			if got := listDir(t, lockDir(dir, testBucket)); len(got) != 0 {
				t.Errorf("lock files = %v after a rejected put, want none", got)
			}
			if _, err := head(p, "fresh"); !errors.Is(err, storage.ErrNotFound) {
				t.Errorf("HeadObject() after a rejected put error = %v, want ErrNotFound", err)
			}
			if _, err := os.Stat(metaFile(dir, "fresh")); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("metadata file after a rejected put: %v, want it absent", err)
			}
			if got := listDir(t, objDir(dir, testBucket)); len(got) != 0 {
				t.Errorf("data files = %v after a rejected put, want none", got)
			}
			assertNoTempFiles(t, dir)
		})
	}
}

// readFile returns the contents of path, failing the test on error.
func readFile(t *testing.T, path string) []byte {
	t.Helper()
	//nolint:gosec // path is under this test's own temp dir
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
