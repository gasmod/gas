package dir

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gasmod/gas/storage"
)

// listKeys lists every object of the configured bucket in one page and
// returns their keys.
func listKeys(t *testing.T, p *Provider) []string {
	t.Helper()
	out, err := p.ListObjects(context.Background(), &storage.ListObjectsInput{})
	if err != nil {
		t.Fatalf("ListObjects() error: %v", err)
	}
	keys := make([]string, 0, len(out.Objects))
	for _, o := range out.Objects {
		keys = append(keys, o.Key)
	}
	return keys
}

// bucketNames lists every bucket in one page and returns their names.
func bucketNames(t *testing.T, p *Provider) []string {
	t.Helper()
	out, err := p.ListBuckets(context.Background(), &storage.ListBucketsInput{})
	if err != nil {
		t.Fatalf("ListBuckets() error: %v", err)
	}
	names := make([]string, 0, len(out.Buckets))
	for _, b := range out.Buckets {
		names = append(names, b.Name)
	}
	return names
}

// nameRecord is the absolute path of bucket's name record under dir.
func nameRecord(dir, bucket string) string {
	return filepath.Join(dir, ".gas", "buckets", sha(bucket))
}

// TestListObjects_SizeAndTime checks that an object's summary carries the
// size and modification time of its data file.
func TestListObjects_SizeAndTime(t *testing.T) {
	t.Parallel()
	p, dir := newTestProvider(t)
	put(t, p, "k", []byte("hello"))

	when := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)
	if err := os.Chtimes(dataFile(t, dir, "k"), when, when); err != nil {
		t.Fatal(err)
	}

	out, err := p.ListObjects(context.Background(), &storage.ListObjectsInput{})
	if err != nil {
		t.Fatalf("ListObjects() error: %v", err)
	}
	want := storage.ObjectSummary{Key: "k", Size: 5, LastModified: when}
	if len(out.Objects) != 1 || out.Objects[0].Key != want.Key ||
		out.Objects[0].Size != want.Size || !out.Objects[0].LastModified.Equal(when) {
		t.Errorf("ListObjects() = %+v, want [%+v]", out.Objects, want)
	}
}

// TestListObjects_SkipsForeignFiles checks that metadata temp files and
// files that are not metadata files are not listed and do not fail the call.
func TestListObjects_SkipsForeignFiles(t *testing.T) {
	t.Parallel()
	p, dir := newTestProvider(t)
	put(t, p, "k", []byte("v"))

	md := metaDir(dir, testBucket)
	writeFile(t, filepath.Join(md, sha("tmp")+".gas.json0b1c"), []byte("{"))
	writeFile(t, filepath.Join(md, "README"), []byte("x"))
	writeFile(t, filepath.Join(md, "short.gas.json"), []byte("{"))
	if err := os.Mkdir(filepath.Join(md, "sub"), 0o750); err != nil {
		t.Fatal(err)
	}

	if got := listKeys(t, p); !slices.Equal(got, []string{"k"}) {
		t.Errorf("ListObjects() = %q, want [k]", got)
	}
}

// TestListObjects_InvalidToken checks that a token this backend did not
// issue is ErrInvalidContinuationToken, and that one it did resumes.
func TestListObjects_InvalidToken(t *testing.T) {
	t.Parallel()
	p, _ := newTestProvider(t)
	put(t, p, "a", []byte("v"))
	put(t, p, "b", []byte("v"))
	ctx := context.Background()

	for _, tok := range []string{"!!!", "a=", "Zm9v="} {
		_, err := p.ListObjects(ctx, &storage.ListObjectsInput{ContinuationToken: new(tok)})
		wantErrIs(t, "ListObjects(token "+tok+")", err, storage.ErrInvalidContinuationToken)
		_, err = p.ListBuckets(ctx, &storage.ListBucketsInput{ContinuationToken: new(tok)})
		wantErrIs(t, "ListBuckets(token "+tok+")", err, storage.ErrInvalidContinuationToken)
	}

	first, err := p.ListObjects(ctx, &storage.ListObjectsInput{MaxKeys: new(int32(1))})
	if err != nil || !first.IsTruncated {
		t.Fatalf("ListObjects(MaxKeys 1) = %+v, %v; want a truncated page", first, err)
	}
	next, err := p.ListObjects(ctx, &storage.ListObjectsInput{
		ContinuationToken: new(first.NextContinuationToken),
	})
	if err != nil || len(next.Objects) != 1 || next.Objects[0].Key != "b" || next.IsTruncated {
		t.Errorf("ListObjects(next) = %+v, %v; want the last page [b]", next, err)
	}
}

// TestListObjects_Canceled checks that a canceled context stops the call.
func TestListObjects_Canceled(t *testing.T) {
	t.Parallel()
	p, _ := newTestProvider(t)
	put(t, p, "k", []byte("v"))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := p.ListObjects(ctx, &storage.ListObjectsInput{})
	wantErrIs(t, "ListObjects(canceled)", err, context.Canceled)
	_, err = p.ListBuckets(ctx, &storage.ListBucketsInput{})
	wantErrIs(t, "ListBuckets(canceled)", err, context.Canceled)
}

// TestListBuckets_NameRecord checks that CreateBucket writes the name record
// once, that a bucket without one is left out of the listing until
// CreateBucket runs again, and that a half-created bucket is not listed.
func TestListBuckets_NameRecord(t *testing.T) {
	t.Parallel()
	p, dir := newTestProvider(t)
	createBucket(t, p, "other-bucket")

	if got := bucketNames(t, p); !slices.Equal(got, []string{"other-bucket", testBucket}) {
		t.Fatalf("ListBuckets() = %q, want [other-bucket %s]", got, testBucket)
	}

	before, err := os.Stat(nameRecord(dir, testBucket))
	if err != nil {
		t.Fatal(err)
	}
	old := before.ModTime().Add(-time.Hour)
	if err = os.Chtimes(nameRecord(dir, testBucket), old, old); err != nil {
		t.Fatal(err)
	}
	createBucket(t, p, testBucket)
	out, err := p.ListBuckets(context.Background(), &storage.ListBucketsInput{
		Prefix: new(testBucket),
	})
	if err != nil || len(out.Buckets) != 1 || !out.Buckets[0].CreationDate.Equal(old) {
		t.Errorf("ListBuckets() after a second CreateBucket = %+v, %v; want CreationDate %v",
			out, err, old)
	}

	// A bucket whose name record is gone cannot be named, so it is left out
	// rather than failing the whole listing: one damaged bucket must not
	// make every other bucket unlistable.
	if err = os.Remove(nameRecord(dir, testBucket)); err != nil {
		t.Fatal(err)
	}
	if got := bucketNames(t, p); !slices.Equal(got, []string{"other-bucket"}) {
		t.Errorf("ListBuckets() without a name record = %q, want [other-bucket]", got)
	}
	createBucket(t, p, testBucket)
	if got := bucketNames(t, p); !slices.Equal(got, []string{"other-bucket", testBucket}) {
		t.Errorf("ListBuckets() after repair = %q, want [other-bucket %s]", got, testBucket)
	}

	// A name record whose metadata directory was never made is a bucket
	// CreateBucket did not finish: it is not listed.
	writeFile(t, nameRecord(dir, "half-bucket"), []byte("half-bucket"))
	if got := bucketNames(t, p); slices.Contains(got, "half-bucket") {
		t.Errorf("ListBuckets() = %q, lists a half-created bucket", got)
	}
}

// TestListBuckets_RecordMismatch checks that a name record holding a name
// that does not hash to its bucket leaves the bucket out of the listing
// instead of failing the call.
func TestListBuckets_RecordMismatch(t *testing.T) {
	t.Parallel()
	p, dir := newTestProvider(t)
	createBucket(t, p, "other-bucket")
	writeFile(t, nameRecord(dir, testBucket), []byte("another-bucket"))

	if got := bucketNames(t, p); !slices.Equal(got, []string{"other-bucket"}) {
		t.Errorf("ListBuckets() with a mismatched record = %q, want [other-bucket]", got)
	}

	// CreateBucket replaces a record that does not hold the bucket's name,
	// such as one a crash left empty.
	for _, bad := range []string{"another-bucket", ""} {
		writeFile(t, nameRecord(dir, testBucket), []byte(bad))
		createBucket(t, p, testBucket)
		if got := bucketNames(t, p); !slices.Equal(got, []string{"other-bucket", testBucket}) {
			t.Errorf("ListBuckets() after repairing a %q record = %q, want [other-bucket %s]",
				bad, got, testBucket)
		}
	}
}

// TestListBuckets_UnusableRecord checks that a name record that is not a
// regular file, as opposed to one that is missing or holds the wrong name,
// still fails the call: it is an I/O fault, not a bucket awaiting repair.
// The error must point at the record on disk without quoting any bucket
// name. TestListBuckets_UnreadableRecord covers the record that cannot be
// read.
func TestListBuckets_UnusableRecord(t *testing.T) {
	t.Parallel()
	p, dir := newTestProvider(t)

	// A directory where the record belongs is not a regular file.
	record := nameRecord(dir, testBucket)
	if err := os.Remove(record); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(record, 0o750); err != nil {
		t.Fatal(err)
	}

	_, err := p.ListBuckets(context.Background(), &storage.ListBucketsInput{})
	wantPresentError(t, "ListBuckets() with a non-regular record", err)
	if strings.Contains(err.Error(), testBucket) {
		t.Errorf("ListBuckets() error = %q, quotes the bucket name", err)
	}
	if !strings.Contains(err.Error(), filepath.Join(".gas", "buckets", sha(testBucket))) {
		t.Errorf("ListBuckets() error = %q, does not name the record path", err)
	}
}

// TestListBuckets_UnreadableRecord checks that a name record the process is
// not allowed to read fails the call rather than being skipped: unlike a
// missing or wrong-name record, it is no evidence that the bucket is merely
// awaiting repair, so skipping it would hide an I/O fault. The error must
// point at the record on disk without quoting any bucket name.
func TestListBuckets_UnreadableRecord(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("a file mode does not deny reads on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root reads the record whatever its mode")
	}
	p, dir := newTestProvider(t)

	// Lstat needs no permission on the record itself, so this reaches the
	// read rather than the mode check.
	record := nameRecord(dir, testBucket)
	if err := os.Chmod(record, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(record, 0o600) })

	_, err := p.ListBuckets(context.Background(), &storage.ListBucketsInput{})
	wantPresentError(t, "ListBuckets() with an unreadable record", err)
	if strings.Contains(err.Error(), testBucket) {
		t.Errorf("ListBuckets() error = %q, quotes the bucket name", err)
	}
	if !strings.Contains(err.Error(), filepath.Join(".gas", "buckets", sha(testBucket))) {
		t.Errorf("ListBuckets() error = %q, does not name the record path", err)
	}
}

// TestListBuckets_SkipDoesNotBreakPaging checks that paging still reaches
// every readable bucket when one of them is unnameable. The unnameable
// bucket is dropped in the VFS, before the provider pages what it returns,
// so this cannot observe it consuming a page slot or disturbing a token;
// what it checks is that several single-bucket pages still yield every
// readable bucket, in order.
func TestListBuckets_SkipDoesNotBreakPaging(t *testing.T) {
	t.Parallel()
	p, dir := newTestProvider(t)
	for _, b := range []string{"bucket-a", "bucket-b", "bucket-c"} {
		createBucket(t, p, b)
	}
	// testBucket sorts last of the four and is the one made unnameable.
	if err := os.Remove(nameRecord(dir, testBucket)); err != nil {
		t.Fatal(err)
	}

	var got []string
	var token *string
	for range 10 {
		out, err := p.ListBuckets(context.Background(), &storage.ListBucketsInput{
			MaxBuckets:        new(int32(1)),
			ContinuationToken: token,
		})
		if err != nil {
			t.Fatalf("ListBuckets() error: %v", err)
		}
		for _, b := range out.Buckets {
			got = append(got, b.Name)
		}
		if out.NextContinuationToken == "" {
			break
		}
		token = &out.NextContinuationToken
	}

	want := []string{"bucket-a", "bucket-b", "bucket-c"}
	if !slices.Equal(got, want) {
		t.Errorf("paged ListBuckets() = %q, want %q", got, want)
	}
}

// TestListObjects_MissingBucket checks that listing a bucket that was never
// created is ErrBucketNotFound, and that ErrBucketNotFound is the only
// not-found it matches.
func TestListObjects_MissingBucket(t *testing.T) {
	t.Parallel()
	p, _ := newTestProvider(t)

	_, err := p.ListObjects(context.Background(), &storage.ListObjectsInput{
		Bucket: new("missing-bucket"),
	})
	wantErrIs(t, "ListObjects(missing bucket)", err, storage.ErrBucketNotFound)
	for _, s := range []error{storage.ErrNotFound, storage.ErrKeyNotFound} {
		if errors.Is(err, s) {
			t.Errorf("ListObjects(missing bucket) error = %v, must not match %v", err, s)
		}
	}
}
