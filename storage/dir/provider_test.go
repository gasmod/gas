package dir

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
	"sync"
	"testing"
	"testing/iotest"
	"time"

	"github.com/gasmod/gas/storage"
)

// checkRoundTrip puts, heads, gets and deletes an object of size bytes and
// checks each result.
func checkRoundTrip(t *testing.T, size int) {
	t.Helper()
	p, dir := newTestProvider(t)
	body := randomBytes(t, size)
	key := fmt.Sprintf("obj-%d.bin", size)

	info := put(t, p, key, body)
	if info == nil {
		t.Fatal("PutObject() info = nil")
	}

	hInfo, err := head(p, key)
	if err != nil {
		t.Fatalf("HeadObject() error: %v", err)
	}
	if hInfo.Size != int64(size) {
		t.Errorf("Head Size = %d, want %d", hInfo.Size, size)
	}

	obj, data := get(t, p, key)
	if !bytes.Equal(data, body) {
		t.Errorf("Get body mismatch: got %d bytes, want %d", len(data), len(body))
	}
	if obj.Size != int64(size) {
		t.Errorf("Get Size = %d, want %d", obj.Size, size)
	}
	if obj.ContentType != hInfo.ContentType || !obj.LastModified.Equal(hInfo.LastModified) {
		t.Errorf("Get = %q %v, Head = %q %v", obj.ContentType, obj.LastModified,
			hInfo.ContentType, hInfo.LastModified)
	}

	dInfo, err := del(p, key)
	if err != nil || dInfo == nil {
		t.Fatalf("DeleteObject() = %v, %v; want info and nil", dInfo, err)
	}
	if _, err := head(p, key); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("Head after delete error = %v, want ErrNotFound", err)
	}
	assertEmptyBucket(t, dir)
	assertNoTempFiles(t, dir)
}

// TestProvider_RoundTripSizes runs checkRoundTrip across sizes around the
// sniffing head.
func TestProvider_RoundTripSizes(t *testing.T) {
	t.Parallel()

	sizes := []int{0, 1, 511, 512, 513, 4096, 3 << 20}
	for _, size := range sizes {
		t.Run(fmt.Sprintf("%d bytes", size), func(t *testing.T) {
			t.Parallel()
			checkRoundTrip(t, size)
		})
	}
}

// TestProvider_PutNilBody checks that a nil body is an empty object.
func TestProvider_PutNilBody(t *testing.T) {
	t.Parallel()
	p, dir := newTestProvider(t)

	_, err := p.PutObject(
		context.Background(),
		&storage.PutObjectInput{BaseObjectInput: base("empty")},
	)
	if err != nil {
		t.Fatalf("PutObject(nil body) error: %v", err)
	}
	obj, data := get(t, p, "empty")
	if len(data) != 0 || obj.Size != 0 {
		t.Errorf("nil body object: size=%d len=%d, want 0", obj.Size, len(data))
	}
	if obj.ContentType != "text/plain; charset=utf-8" {
		t.Errorf("ContentType = %q, want the type sniffed from no bytes", obj.ContentType)
	}
	assertNoTempFiles(t, dir)
}

// TestProvider_ContentType checks explicit and sniffed content types.
func TestProvider_ContentType(t *testing.T) {
	t.Parallel()

	html := []byte("<!DOCTYPE html><html><body>hi</body></html>")
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	const plain = "text/plain; charset=utf-8"

	tests := []struct {
		ct   *string
		name string
		want string
		body []byte
	}{
		{
			name: "explicit wins over sniffing",
			body: html,
			ct:   new("application/x-custom"),
			want: "application/x-custom",
		},
		{name: "explicit on empty body", body: nil, ct: new("text/plain"), want: "text/plain"},
		{name: "sniffed html", body: html, want: "text/html; charset=utf-8"},
		{name: "sniffed png", body: png, want: "image/png"},
		{name: "sniffed plain text", body: []byte("hello world"), want: plain},
		{
			name: "empty explicit falls back to sniffing",
			body: html,
			ct:   new(""),
			want: "text/html; charset=utf-8",
		},
		{name: "empty body", body: nil, want: plain},
		{name: "empty body empty explicit", body: nil, ct: new(""), want: plain},
		{
			name: "binary sniffed",
			body: []byte{0x00, 0x01, 0x02, 0xff},
			want: "application/octet-stream",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p, _ := newTestProvider(t)
			if _, err := putWith(p, "obj", bytes.NewReader(tt.body), tt.ct, nil); err != nil {
				t.Fatalf("PutObject() error: %v", err)
			}
			h, err := head(p, "obj")
			if err != nil {
				t.Fatalf("HeadObject() error: %v", err)
			}
			if h.ContentType != tt.want {
				t.Errorf("Head ContentType = %q, want %q", h.ContentType, tt.want)
			}
			obj, data := get(t, p, "obj")
			if obj.ContentType != tt.want {
				t.Errorf("Get ContentType = %q, want %q", obj.ContentType, tt.want)
			}
			if !bytes.Equal(data, tt.body) {
				t.Errorf("Get body = %q, want %q", data, tt.body)
			}
		})
	}
}

// TestProvider_Metadata runs the metadata cases.
func TestProvider_Metadata(t *testing.T) {
	t.Parallel()

	t.Run("round trip", testProviderMetadataRoundTrip)

	t.Run("input map is not mutated", testProviderMetadataInputMapIsNotMutated)

	t.Run("nil metadata reads back as nil", testProviderMetadataNilReadsBackNil)

	t.Run(
		"keys named like internal fields are plain metadata",
		testProviderMetadataInternalFieldNames,
	)

	t.Run("overwrite replaces metadata", testProviderMetadataOverwriteReplacesMetadata)
}

// testProviderMetadataRoundTrip checks that metadata reads back from Head and
// Get.
func testProviderMetadataRoundTrip(t *testing.T) {
	t.Parallel()
	p, _ := newTestProvider(t)
	meta := map[string]string{"owner": "ahmed", "x-trace": "abc", "unicode": "日本"}
	if _, err := putWith(p, "m", strings.NewReader("x"), nil, meta); err != nil {
		t.Fatalf("PutObject() error: %v", err)
	}
	h, err := head(p, "m")
	if err != nil {
		t.Fatalf("HeadObject() error: %v", err)
	}
	if !maps.Equal(h.Metadata, meta) {
		t.Errorf("Head Metadata = %v, want %v", h.Metadata, meta)
	}
	obj, _ := get(t, p, "m")
	if !maps.Equal(obj.Metadata, meta) {
		t.Errorf("Get Metadata = %v, want %v", obj.Metadata, meta)
	}
}

// testProviderMetadataInputMapIsNotMutated checks that Put leaves the
// caller's map alone.
func testProviderMetadataInputMapIsNotMutated(t *testing.T) {
	t.Parallel()
	p, _ := newTestProvider(t)
	meta := map[string]string{"a": "1"}
	if _, err := putWith(p, "m", nil, nil, meta); err != nil {
		t.Fatalf("PutObject() error: %v", err)
	}
	if len(meta) != 1 || meta["a"] != "1" {
		t.Errorf("caller metadata mutated: %v", meta)
	}
}

// testProviderMetadataNilReadsBackNil checks that metadata not given is
// stored as JSON null and reads back as a nil map, as the s3 provider returns
// it.
func testProviderMetadataNilReadsBackNil(t *testing.T) {
	t.Parallel()
	p, _ := newTestProvider(t)
	put(t, p, "m", []byte("x"))
	h, err := head(p, "m")
	if err != nil {
		t.Fatalf("HeadObject() error: %v", err)
	}
	if h.Metadata != nil {
		t.Errorf("Head Metadata = %#v, want nil", h.Metadata)
	}
}

// testProviderMetadataInternalFieldNames checks that user metadata is stored
// under its own JSON field, so keys that match the names of the internal
// fields cannot override them.
func testProviderMetadataInternalFieldNames(t *testing.T) {
	t.Parallel()
	p, _ := newTestProvider(t)
	meta := map[string]string{
		"ContentType": "text/x-fake",
		"DataFile":    "../../escape",
		"Key":         "other",
		"keep":        "me",
	}
	if _, err := putWith(p, "m", strings.NewReader("x"), new("text/x-real"), meta); err != nil {
		t.Fatalf("PutObject() error: %v", err)
	}
	obj, data := get(t, p, "m")
	if obj.ContentType != "text/x-real" || string(data) != "x" {
		t.Errorf("Get = %q %q, want text/x-real x", obj.ContentType, data)
	}
	if !maps.Equal(obj.Metadata, meta) {
		t.Errorf("Metadata = %v, want %v", obj.Metadata, meta)
	}
}

// testProviderMetadataOverwriteReplacesMetadata checks that an overwrite
// replaces metadata instead of merging it.
func testProviderMetadataOverwriteReplacesMetadata(t *testing.T) {
	t.Parallel()
	p, _ := newTestProvider(t)
	for _, m := range []map[string]string{{"a": "1"}, {"b": "2"}} {
		if _, err := putWith(p, "m", nil, nil, m); err != nil {
			t.Fatalf("PutObject() error: %v", err)
		}
	}
	h, err := head(p, "m")
	if err != nil {
		t.Fatalf("HeadObject() error: %v", err)
	}
	if !maps.Equal(h.Metadata, map[string]string{"b": "2"}) {
		t.Errorf("Metadata = %v, want only b=2", h.Metadata)
	}
}

// TestProvider_LastModified checks that LastModified is the write time and
// agrees between Head and Get.
func TestProvider_LastModified(t *testing.T) {
	t.Parallel()
	p, _ := newTestProvider(t)

	before := time.Now().Add(-2 * time.Second)
	put(t, p, "t", []byte("x"))
	after := time.Now().Add(2 * time.Second)

	h, err := head(p, "t")
	if err != nil {
		t.Fatalf("HeadObject() error: %v", err)
	}
	if h.LastModified.Before(before) || h.LastModified.After(after) {
		t.Errorf("LastModified = %v, want between %v and %v", h.LastModified, before, after)
	}
	obj, _ := get(t, p, "t")
	if !obj.LastModified.Equal(h.LastModified) {
		t.Errorf("Get LastModified = %v, Head = %v", obj.LastModified, h.LastModified)
	}
}

// TestProvider_Keys checks that every key that passes validation is stored
// under its hash, so keys that would be dangerous or impossible as paths
// round trip like any other.
func TestProvider_Keys(t *testing.T) {
	t.Parallel()

	valid := map[string]string{
		"single":           "a",
		"nested":           "a/b/c",
		"spaces":           "with spaces/and more spaces.txt",
		"unicode":          "unicode/résumé-日本語.pdf",
		"dotfile":          ".hidden",
		"triple dot":       "...",
		"backslash":        "back\\slash",
		"percent":          "percent%20encoded",
		"parent escape":    "../escape",
		"dot dot":          "..",
		"deep escape":      "a/../../b",
		"absolute":         "/abs",
		"root slash":       "/",
		"windows absolute": `C:\Windows\x`,
		"internal dir":     ".gas/objects/x",
		"internal meta":    "x.gas.json",
		"nul byte":         "a\x00b",
		"control bytes":    "\x01\x1f\x7f\n\r\t",
		"1024 bytes":       strings.Repeat("k", 1024),
		"511 levels":       strings.Repeat("d/", 511) + "ff",
		"256 byte segment": strings.Repeat("s", 256),
	}
	for name, key := range valid {
		t.Run("valid "+name, func(t *testing.T) {
			t.Parallel()
			p, dir := newTestProvider(t)
			outside := filepath.Dir(dir)
			before := listDir(t, outside)

			put(t, p, key, []byte(key))
			_, data := get(t, p, key)
			if string(data) != key {
				t.Errorf("body = %q, want %q", data, key)
			}
			if _, err := os.Stat(metaFile(dir, key)); err != nil {
				t.Errorf("metadata not at the key's hash: %v", err)
			}
			if _, err := del(p, key); err != nil {
				t.Errorf("DeleteObject() error: %v", err)
			}
			assertEmptyBucket(t, dir)
			if after := listDir(t, outside); !slicesEqual(before, after) {
				t.Errorf("entries next to the root changed: %v -> %v", before, after)
			}
		})
	}

	invalid := []struct {
		name string
		key  string
	}{
		{name: "empty", key: ""},
		{name: "dot", key: "."},
		{name: "a/..", key: "a/.."},
		{name: "x/../", key: "x/../"},
		{name: "too long", key: strings.Repeat("a", 1025)},
		{name: "invalid utf-8", key: "bad\xff"},
		{name: "truncated utf-8", key: "a\xc3"},
	}
	for _, tt := range invalid {
		t.Run("invalid "+tt.name, func(t *testing.T) {
			t.Parallel()
			p, dir := newTestProvider(t)
			for op, err := range allOps(context.Background(), p, base(tt.key)) {
				wantErrIs(t, op, err, storage.ErrInvalidKey)
			}
			assertEmptyBucket(t, dir)
		})
	}
}

// slicesEqual reports whether a and b hold the same strings in order.
func slicesEqual(a, b []string) bool {
	return strings.Join(a, "\x00") == strings.Join(b, "\x00")
}

// TestProvider_KeysAreDistinct checks that keys are opaque strings, as in S3:
// keys that a filesystem would resolve to one path (normalization, case
// folding, Unicode normalization) are distinct objects on every filesystem.
func TestProvider_KeysAreDistinct(t *testing.T) {
	t.Parallel()

	pairs := [][2]string{
		{"a/b", "a/b/"},
		{"a/b", "a//b"},
		{"a/b", "./a/b"},
		{"y", "x/../y"},
		{"Report.txt", "report.txt"},
		{"caf\u00e9", "cafe\u0301"},
		{"ſ", "s"},
	}
	for _, pair := range pairs {
		t.Run(fmt.Sprintf("%q vs %q", pair[0], pair[1]), func(t *testing.T) {
			t.Parallel()
			p, _ := newTestProvider(t)
			put(t, p, pair[0], []byte("first"))
			put(t, p, pair[1], []byte("second"))
			if _, data := get(t, p, pair[0]); string(data) != "first" {
				t.Errorf("Get(%q) = %q, want first", pair[0], data)
			}
			if _, err := del(p, pair[1]); err != nil {
				t.Fatalf("DeleteObject(%q) error: %v", pair[1], err)
			}
			if _, data := get(t, p, pair[0]); string(data) != "first" {
				t.Errorf("Get(%q) after deleting %q = %q, want first", pair[0], pair[1], data)
			}
		})
	}
}

// TestProvider_PrefixKeys checks that a key that is a prefix of another key
// is an independent object; there are no directories to collide with.
func TestProvider_PrefixKeys(t *testing.T) {
	t.Parallel()
	p, _ := newTestProvider(t)
	put(t, p, "p/c", []byte("child"))
	if _, err := head(p, "p"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("Head(p) = %v, want ErrNotFound", err)
	}
	put(t, p, "p", []byte("parent"))
	put(t, p, "p/c/d", []byte("grandchild"))
	if _, err := del(p, "p"); err != nil {
		t.Fatalf("DeleteObject(p) error: %v", err)
	}
	for key, want := range map[string]string{"p/c": "child", "p/c/d": "grandchild"} {
		if _, data := get(t, p, key); string(data) != want {
			t.Errorf("Get(%q) = %q, want %q", key, data, want)
		}
	}
}

// TestProvider_Bucket checks bucket selection, validation, missing buckets
// and isolation.
func TestProvider_Bucket(t *testing.T) {
	t.Parallel()

	t.Run("explicit bucket overrides config", func(t *testing.T) {
		t.Parallel()
		p, dir := newProvider(t, "other-bucket")
		createBucket(t, p, testBucket)
		_, err := p.PutObject(context.Background(), &storage.PutObjectInput{
			BaseObjectInput: inBucket(testBucket),
			Body:            strings.NewReader("x"),
		})
		if err != nil {
			t.Fatalf("PutObject() error: %v", err)
		}
		if _, err := os.Stat(metaFile(dir, "k")); err != nil {
			t.Errorf("object not in explicit bucket: %v", err)
		}
	})

	t.Run("nil bucket uses config", func(t *testing.T) {
		t.Parallel()
		p, dir := newTestProvider(t)
		put(t, p, "k", []byte("x"))
		if _, err := os.Stat(metaFile(dir, "k")); err != nil {
			t.Errorf("object not in configured bucket: %v", err)
		}
	})

	invalid := []struct {
		bucket *string
		cfg    string
		name   string
	}{
		{name: "pointer to empty does not fall back", bucket: new(""), cfg: testBucket},
		{name: "uppercase", bucket: new("Test-Bucket"), cfg: testBucket},
		{name: "underscore", bucket: new("test_bucket"), cfg: testBucket},
		{name: "too short", bucket: new("ab"), cfg: testBucket},
		{name: "too long", bucket: new(strings.Repeat("a", 64)), cfg: testBucket},
		{name: "ip-like", bucket: new("10.0.0.1"), cfg: testBucket},
		{name: "leading dash", bucket: new("-bucket"), cfg: testBucket},
		{name: "trailing dash", bucket: new("bucket-"), cfg: testBucket},
		{name: "double dot", bucket: new("a..b"), cfg: testBucket},
		{name: "dot dot", bucket: new(".."), cfg: testBucket},
		{name: "slash", bucket: new("a/b"), cfg: testBucket},
		{name: "nul byte", bucket: new("buc\x00ket"), cfg: testBucket},
		{name: "reserved prefix", bucket: new("xn--abc"), cfg: testBucket},
		{name: "reserved suffix", bucket: new("abc--x-s3"), cfg: testBucket},
		{name: "no bucket anywhere", bucket: nil, cfg: ""},
	}
	for _, tt := range invalid {
		t.Run("invalid "+tt.name, func(t *testing.T) {
			t.Parallel()
			p, _ := newProvider(t, tt.cfg)
			in := storage.BaseObjectInput{Bucket: tt.bucket, Key: "k"}
			for op, err := range allOps(context.Background(), p, in) {
				wantErrIs(t, op, err, storage.ErrInvalidBucket)
			}
		})
	}

	t.Run("missing bucket", func(t *testing.T) {
		t.Parallel()
		p, dir := newProvider(t, "missing-bucket")
		for op, err := range allOps(context.Background(), p, base("k")) {
			wantErrIs(t, op, err, storage.ErrBucketNotFound)
			for _, other := range []error{storage.ErrNotFound, storage.ErrKeyNotFound} {
				if errors.Is(err, other) {
					t.Errorf("%s error = %v, must not also match %v", op, err, other)
				}
			}
		}
		if got := listDir(t, dir); len(got) != 1 || got[0] != ".gas" {
			t.Errorf("root = %v after ops on a missing bucket, want only .gas", got)
		}
	})

	t.Run("buckets are isolated", func(t *testing.T) {
		t.Parallel()
		p, _ := newTestProvider(t)
		createBucket(t, p, "second-bucket")
		_, err := p.PutObject(context.Background(), &storage.PutObjectInput{
			BaseObjectInput: inBucket("second-bucket"),
			Body:            strings.NewReader("second"),
		})
		if err != nil {
			t.Fatalf("PutObject() error: %v", err)
		}
		if _, err := head(p, "k"); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("Head in default bucket error = %v, want ErrNotFound", err)
		}
	})
}

// TestProvider_CreateBucket runs the CreateBucket cases.
func TestProvider_CreateBucket(t *testing.T) {
	t.Parallel()

	t.Run("creates a usable bucket", testProviderCreateBucketUsable)

	t.Run("existing bucket", testProviderCreateBucketExisting)

	for _, bk := range []string{"", "ab", "UPPER", "a_b", "../x", "a/b", "10.0.0.1", "buc\x00ket"} {
		t.Run(fmt.Sprintf("invalid %q", bk), func(t *testing.T) {
			t.Parallel()
			testProviderCreateBucketInvalid(t, bk)
		})
	}

	t.Run("bucket dir in the way", testProviderCreateBucketDirInTheWay)
}

// testProviderCreateBucketUsable checks that CreateBucket makes the bucket's
// dirs and the bucket accepts objects.
func testProviderCreateBucketUsable(t *testing.T) {
	t.Parallel()
	p, dir := newProvider(t, "")
	info, err := p.CreateBucket(context.Background(),
		&storage.CreateBucketInput{Bucket: "new-bucket"})
	if err != nil {
		t.Fatalf("CreateBucket() error: %v", err)
	}
	if info.Name != "new-bucket" || info.CreationDate.IsZero() || info.ID != "" ||
		info.Region != "" {
		t.Errorf("CreateBucket() = %+v, want Name new-bucket and a CreationDate only", info)
	}
	again, err := p.CreateBucket(context.Background(),
		&storage.CreateBucketInput{Bucket: "new-bucket"})
	if err != nil || !again.CreationDate.Equal(info.CreationDate) {
		t.Errorf("CreateBucket() again = %+v, %v; want the first CreationDate %v",
			again, err, info.CreationDate)
	}
	for _, d := range []string{
		metaDir(dir, "new-bucket"),
		objDir(dir, "new-bucket"),
		lockDir(dir, "new-bucket"),
	} {
		if fi, sErr := os.Stat(d); sErr != nil || !fi.IsDir() {
			t.Errorf("bucket dir %s: %v", d, sErr)
		}
	}
	_, err = p.PutObject(context.Background(), &storage.PutObjectInput{
		BaseObjectInput: storage.BaseObjectInput{Bucket: new("new-bucket"), Key: "k"},
	})
	if err != nil {
		t.Errorf("PutObject() into the new bucket error: %v", err)
	}
}

// testProviderCreateBucketExisting checks that CreateBucket of an existing
// bucket succeeds, as for a bucket the caller owns on S3, and keeps its
// objects. Every bucket under the root belongs to the caller.
func testProviderCreateBucketExisting(t *testing.T) {
	t.Parallel()
	p, _ := newTestProvider(t)
	put(t, p, "k", []byte("kept"))
	for range 2 {
		info, err := p.CreateBucket(context.Background(),
			&storage.CreateBucketInput{Bucket: testBucket})
		if err != nil {
			t.Fatalf("CreateBucket() of an existing bucket error = %v, want nil", err)
		}
		if info == nil || info.Name != testBucket {
			t.Fatalf("CreateBucket() of an existing bucket = %+v, want Name %q",
				info, testBucket)
		}
	}
	if _, data := get(t, p, "k"); string(data) != "kept" {
		t.Errorf("object after CreateBucket of its bucket = %q, want kept", data)
	}
}

// testProviderCreateBucketInvalid checks that CreateBucket rejects bk with
// ErrInvalidBucket before touching disk.
func testProviderCreateBucketInvalid(t *testing.T, bk string) {
	p, dir := newProvider(t, "")
	info, err := p.CreateBucket(context.Background(), &storage.CreateBucketInput{Bucket: bk})
	if !errors.Is(err, storage.ErrInvalidBucket) || info != nil {
		t.Fatalf("CreateBucket(%q) = %+v, %v; want nil, ErrInvalidBucket", bk, info, err)
	}
	if got := listDir(t, dir); len(got) != 1 || got[0] != ".gas" {
		t.Errorf("root = %v after rejected CreateBucket, want only .gas", got)
	}
}

// testProviderCreateBucketDirInTheWay checks that something other than a
// directory at the bucket's metadata path is an I/O error, not "exists".
func testProviderCreateBucketDirInTheWay(t *testing.T) {
	t.Parallel()
	p, dir := newProvider(t, "")
	writeFile(t, metaDir(dir, "blocked"), []byte("not a dir"))
	_, err := p.CreateBucket(context.Background(), &storage.CreateBucketInput{Bucket: "blocked"})
	wantPresentError(t, "CreateBucket", err)
	if errors.Is(err, storage.ErrBucketExists) {
		t.Errorf("CreateBucket() error = %v, must not be ErrBucketExists", err)
	}
}

// TestProvider_NotFound checks the not-found sentinel of each operation on a
// missing key.
func TestProvider_NotFound(t *testing.T) {
	t.Parallel()

	for _, key := range []string{"nope", "no/such/dir/key"} {
		t.Run(key, func(t *testing.T) {
			t.Parallel()
			p, dir := newTestProvider(t)
			put(t, p, "other", []byte("x"))

			// Head matches s3's HEAD: ErrNotFound only.
			_, err := head(p, key)
			wantErrIs(t, "HeadObject", err, storage.ErrNotFound)
			if errors.Is(err, storage.ErrKeyNotFound) || errors.Is(err, storage.ErrBucketNotFound) {
				t.Errorf("HeadObject error = %v, want only ErrNotFound", err)
			}

			err = getErr(p, key)
			wantErrIs(t, "GetObject", err, storage.ErrKeyNotFound)
			if errors.Is(err, storage.ErrNotFound) || errors.Is(err, storage.ErrBucketNotFound) {
				t.Errorf("GetObject error = %v, want only ErrKeyNotFound", err)
			}

			info, err := del(p, key)
			if err != nil || info == nil {
				t.Errorf("DeleteObject() = %v, %v; want info and nil", info, err)
			}
			// Lookups of missing keys leave no lock files behind.
			if got := listDir(t, lockDir(dir, testBucket)); len(got) > 1 {
				t.Errorf("lock dir = %v, want at most other's lock file", got)
			}
		})
	}
}

// TestProvider_Delete checks Delete edge cases.
func TestProvider_Delete(t *testing.T) {
	t.Parallel()

	t.Run("twice", func(t *testing.T) {
		t.Parallel()
		p, dir := newTestProvider(t)
		put(t, p, "k", []byte("x"))
		for i := range 2 {
			if _, err := del(p, "k"); err != nil {
				t.Fatalf("DeleteObject() #%d error: %v", i, err)
			}
		}
		assertEmptyBucket(t, dir)
		if got := listDir(t, lockDir(dir, testBucket)); len(got) != 0 {
			t.Errorf("lock dir = %v after delete, want empty", got)
		}
	})

	t.Run("version is not supported", func(t *testing.T) {
		t.Parallel()
		p, _ := newTestProvider(t)
		put(t, p, "k", []byte("x"))
		info, err := p.DeleteObject(context.Background(), &storage.DeleteObjectInput{
			BaseObjectInput: base("k"),
			Version:         new("some-other-version"),
		})
		if !errors.Is(err, storage.ErrNotSupported) || info != nil {
			t.Fatalf("DeleteObject() with Version = %+v, %v; want nil, ErrNotSupported", info, err)
		}
		if _, data := get(t, p, "k"); string(data) != "x" {
			t.Errorf("object after a rejected versioned delete = %q, want x", data)
		}
	})

	t.Run("missing data file", func(t *testing.T) {
		t.Parallel()
		p, dir := newTestProvider(t)
		put(t, p, "k", []byte("x"))
		if err := os.Remove(dataFile(t, dir, "k")); err != nil {
			t.Fatal(err)
		}
		if _, err := del(p, "k"); err != nil {
			t.Fatalf("DeleteObject() error: %v", err)
		}
		assertEmptyBucket(t, dir)
	})

	t.Run("data file cannot be removed", func(t *testing.T) {
		t.Parallel()
		p, dir := newTestProvider(t)
		put(t, p, "k", []byte("x"))
		data := dataFile(t, dir, "k")
		if err := os.Remove(data); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(data, "x"), 0o750); err != nil {
			t.Fatal(err)
		}
		info, err := del(p, "k")
		var pe *os.PathError
		if !errors.As(err, &pe) || info != nil {
			t.Fatalf("DeleteObject() = %v, %v; want nil and a *os.PathError", info, err)
		}
		wantPresentError(t, "DeleteObject", err)
		// The metadata was removed first, so the object is gone.
		if _, err := head(p, "k"); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("Head after delete = %v, want ErrNotFound", err)
		}
	})
}

// corruptContents are metadata file contents the provider must treat as a
// corrupt object, never as a missing one.
var corruptContents = map[string]string{
	"invalid json":       "{not json",
	"wrong value type":   `{"Metadata":{"a":1}}`,
	"array":              `["a"]`,
	"null":               "null",
	"empty":              "",
	"no data file":       `{"Key":"k"}`,
	"escaping data file": `{"DataFile":"../x"}`,
}

// TestProvider_Corrupt checks that corrupt metadata and data files are errors
// that never read as not-found.
func TestProvider_Corrupt(t *testing.T) {
	t.Parallel()

	for name, content := range corruptContents {
		t.Run("metadata "+name, func(t *testing.T) {
			t.Parallel()
			p, dir := newTestProvider(t)
			put(t, p, "k", []byte("x"))
			writeFile(t, metaFile(dir, "k"), []byte(content))

			_, err := head(p, "k")
			wantPresentError(t, "HeadObject", err)
			wantPresentError(t, "GetObject", getErr(p, "k"))
			_, err = del(p, "k")
			wantPresentError(t, "DeleteObject", err)

			// A Put repairs the key.
			put(t, p, "k", []byte("fixed"))
			if _, data := get(t, p, "k"); string(data) != "fixed" {
				t.Errorf("body after repair = %q, want fixed", data)
			}
		})
	}

	t.Run("metadata is a directory", func(t *testing.T) {
		t.Parallel()
		p, dir := newTestProvider(t)
		if err := os.MkdirAll(filepath.Join(metaFile(dir, "k"), "x"), 0o750); err != nil {
			t.Fatal(err)
		}
		_, err := head(p, "k")
		wantPresentError(t, "HeadObject", err)
		wantPresentError(t, "GetObject", getErr(p, "k"))
		_, err = del(p, "k")
		wantPresentError(t, "DeleteObject", err)
		info, err := putWith(p, "k", strings.NewReader("x"), nil, nil)
		wantPresentError(t, "PutObject", err)
		if info != nil {
			t.Errorf("PutObject() info = %+v, want nil", info)
		}
		assertNoTempFiles(t, dir)
		if got := listDir(t, objDir(dir, testBucket)); len(got) != 0 {
			t.Errorf("objects dir = %v after failed put, want empty", got)
		}
	})

	t.Run("data file missing", func(t *testing.T) {
		t.Parallel()
		p, dir := newTestProvider(t)
		put(t, p, "k", []byte("x"))
		if err := os.Remove(dataFile(t, dir, "k")); err != nil {
			t.Fatal(err)
		}
		_, err := head(p, "k")
		wantPresentError(t, "HeadObject", err)
		wantPresentError(t, "GetObject", getErr(p, "k"))
	})

	t.Run("data file is a directory", func(t *testing.T) {
		t.Parallel()
		p, dir := newTestProvider(t)
		put(t, p, "k", []byte("x"))
		data := dataFile(t, dir, "k")
		if err := os.Remove(data); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(data, 0o750); err != nil {
			t.Fatal(err)
		}
		_, err := head(p, "k")
		wantPresentError(t, "HeadObject", err)
		wantPresentError(t, "GetObject", getErr(p, "k"))
	})
}

// TestProvider_StaleData checks that a Put whose commit succeeded but whose
// previous data file could not be removed returns the object info together
// with ErrStaleData.
func TestProvider_StaleData(t *testing.T) {
	t.Parallel()
	p, dir := newTestProvider(t)
	put(t, p, "k", []byte("old"))
	old := dataFile(t, dir, "k")
	// A non-empty directory at the old data file's name cannot be removed.
	if err := os.Remove(old); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(old, "x"), 0o750); err != nil {
		t.Fatal(err)
	}

	info, err := putWith(p, "k", strings.NewReader("new"), new("text/x-new"), nil)
	wantErrIs(t, "PutObject", err, ErrStaleData)
	wantPresentError(t, "PutObject", err)
	if info == nil {
		t.Fatal("PutObject() info = nil, want the written object's info")
	}
	if !strings.HasPrefix(err.Error(), "gas/storage/dir/provider: put: ") {
		t.Errorf("error = %q, want the put prefix", err)
	}
	obj, data := get(t, p, "k")
	if string(data) != "new" || obj.ContentType != "text/x-new" {
		t.Errorf("Get = %q %q, want new text/x-new", data, obj.ContentType)
	}
}

// TestProvider_ReaderErrors checks that a failing body fails the Put and
// keeps the previous object.
func TestProvider_ReaderErrors(t *testing.T) {
	t.Parallel()

	errBoom := errors.New("boom")
	tests := []struct {
		reader func() io.Reader
		want   error
		name   string
	}{
		{
			name:   "error on first read",
			reader: func() io.Reader { return iotest.ErrReader(errBoom) },
			want:   errBoom,
		},
		{
			name: "error after head",
			reader: func() io.Reader {
				return io.MultiReader(bytes.NewReader(make([]byte, 600)), iotest.ErrReader(errBoom))
			},
			want: errBoom,
		},
		{
			name: "error inside head",
			reader: func() io.Reader {
				return io.MultiReader(bytes.NewReader(make([]byte, 100)), iotest.ErrReader(errBoom))
			},
			want: errBoom,
		},
		{
			name: "timeout reader mid-stream",
			reader: func() io.Reader {
				return iotest.TimeoutReader(bytes.NewReader(make([]byte, 2000)))
			},
			want: iotest.ErrTimeout,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p, dir := newTestProvider(t)
			put(t, p, "k", []byte("original"))
			oldData := dataFile(t, dir, "k")

			info, err := putWith(p, "k", tt.reader(), nil, nil)
			wantErrIs(t, "PutObject", err, tt.want)
			if info != nil {
				t.Errorf("PutObject() info = %+v, want nil", info)
			}
			assertNoTempFiles(t, dir)
			if got := dataFile(t, dir, "k"); got != oldData {
				t.Errorf("data file = %s, want the original %s", got, oldData)
			}

			if _, data := get(t, p, "k"); string(data) != "original" {
				t.Errorf("failed put clobbered object: %q", data)
			}
		})
	}

	t.Run("failed put of new key leaves nothing", func(t *testing.T) {
		t.Parallel()
		p, dir := newTestProvider(t)
		_, err := putWith(p, "new/nested/key", iotest.ErrReader(errBoom), nil, nil)
		wantErrIs(t, "PutObject", err, errBoom)
		assertEmptyBucket(t, dir)
		if _, err := head(p, "new/nested/key"); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("Head after failed put error = %v, want ErrNotFound", err)
		}
	})
}

// TestProvider_ReaderShapes checks Put with readers that return data in odd
// shapes.
func TestProvider_ReaderShapes(t *testing.T) {
	t.Parallel()

	body := randomBytes(t, 3000)
	tests := []struct {
		reader func() io.Reader
		name   string
	}{
		{
			name:   "one byte reader",
			reader: func() io.Reader { return iotest.OneByteReader(bytes.NewReader(body)) },
		},
		{
			name:   "half reader",
			reader: func() io.Reader { return iotest.HalfReader(bytes.NewReader(body)) },
		},
		{
			name:   "data with EOF",
			reader: func() io.Reader { return iotest.DataErrReader(bytes.NewReader(body)) },
		},
		{
			name: "data with EOF one byte",
			reader: func() io.Reader {
				return iotest.DataErrReader(iotest.OneByteReader(bytes.NewReader(body)))
			},
		},
		{name: "pipe", reader: func() io.Reader {
			pr, pw := io.Pipe()
			go func() {
				for i := 0; i < len(body); i += 100 {
					_, _ = pw.Write(body[i:min(i+100, len(body))])
				}
				_ = pw.Close()
			}()
			return pr
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p, dir := newTestProvider(t)
			if _, err := putWith(p, "k", tt.reader(), nil, nil); err != nil {
				t.Fatalf("PutObject() error: %v", err)
			}
			_, data := get(t, p, "k")
			if !bytes.Equal(data, body) {
				t.Errorf("body mismatch: got %d bytes, want %d", len(data), len(body))
			}
			assertNoTempFiles(t, dir)
		})
	}

	t.Run("short data with EOF", func(t *testing.T) {
		t.Parallel()
		p, _ := newTestProvider(t)
		_, err := putWith(p, "k", iotest.DataErrReader(strings.NewReader("tiny")), nil, nil)
		if err != nil {
			t.Fatalf("PutObject() error: %v", err)
		}
		_, data := get(t, p, "k")
		if string(data) != "tiny" {
			t.Errorf("body = %q, want tiny", data)
		}
	})
}

// TestProvider_GetBodyIsReadable checks the Get body with iotest.TestReader.
func TestProvider_GetBodyIsReadable(t *testing.T) {
	t.Parallel()
	p, _ := newTestProvider(t)
	body := randomBytes(t, 10000)
	put(t, p, "k", body)

	obj, err := p.GetObject(context.Background(), &storage.GetObjectInput{
		HeadObjectInput: storage.HeadObjectInput{BaseObjectInput: base("k")},
	})
	if err != nil {
		t.Fatalf("GetObject() error: %v", err)
	}
	if err := iotest.TestReader(obj.Body, body); err != nil {
		t.Errorf("iotest.TestReader: %v", err)
	}
	if err := obj.Body.Close(); err != nil {
		t.Errorf("Close() error: %v", err)
	}
}

// TestProvider_VersionNotSupported checks that every operation taking a
// Version rejects one with ErrNotSupported, since dir does not version
// objects, and that the rejection reads and changes nothing. Even an empty
// version string is rejected: the field is set.
func TestProvider_VersionNotSupported(t *testing.T) {
	t.Parallel()
	for _, version := range []string{"v1", ""} {
		p, _ := newTestProvider(t)
		put(t, p, "k", []byte("kept"))
		ctx := context.Background()
		in := storage.HeadObjectInput{BaseObjectInput: base("k"), Version: new(version)}

		h, err := p.HeadObject(ctx, &in)
		if !errors.Is(err, storage.ErrNotSupported) || h != nil {
			t.Errorf("HeadObject(Version %q) = %+v, %v; want nil, ErrNotSupported", version, h, err)
		}
		obj, err := p.GetObject(ctx, &storage.GetObjectInput{HeadObjectInput: in})
		if !errors.Is(err, storage.ErrNotSupported) || obj != nil {
			t.Errorf(
				"GetObject(Version %q) = %+v, %v; want nil, ErrNotSupported",
				version,
				obj,
				err,
			)
		}
		d, err := p.DeleteObject(
			ctx,
			&storage.DeleteObjectInput{BaseObjectInput: base("k"), Version: new(version)},
		)
		if !errors.Is(err, storage.ErrNotSupported) || d != nil {
			t.Errorf(
				"DeleteObject(Version %q) = %+v, %v; want nil, ErrNotSupported",
				version,
				d,
				err,
			)
		}
		if _, data := get(t, p, "k"); string(data) != "kept" {
			t.Errorf("object after rejected versioned calls = %q, want kept", data)
		}
	}
}

// TestProvider_PartNumberNotSupported checks that Head and Get reject a set
// PartNumber, including a pointer to 0, with ErrNotSupported, since dir does
// not store objects in parts, and that the rejection reads nothing. An
// invalid key is still reported as such.
func TestProvider_PartNumberNotSupported(t *testing.T) {
	t.Parallel()
	p, _ := newTestProvider(t)
	put(t, p, "k", []byte("kept"))
	ctx := context.Background()

	for _, part := range []int32{0, 1, 2, -1} {
		in := storage.HeadObjectInput{BaseObjectInput: base("k"), PartNumber: new(part)}
		h, err := p.HeadObject(ctx, &in)
		if !errors.Is(err, storage.ErrNotSupported) || h != nil {
			t.Errorf("HeadObject(PartNumber %d) = %+v, %v; want nil, ErrNotSupported", part, h, err)
		}
		obj, err := p.GetObject(ctx, &storage.GetObjectInput{HeadObjectInput: in})
		if !errors.Is(err, storage.ErrNotSupported) || obj != nil {
			t.Errorf(
				"GetObject(PartNumber %d) = %+v, %v; want nil, ErrNotSupported",
				part,
				obj,
				err,
			)
		}
	}

	bad := storage.HeadObjectInput{BaseObjectInput: base(""), PartNumber: new(int32(1))}
	if _, err := p.HeadObject(ctx, &bad); !errors.Is(err, storage.ErrInvalidKey) {
		t.Errorf("HeadObject(invalid key, PartNumber) error = %v, want ErrInvalidKey", err)
	}
	if _, data := get(t, p, "k"); string(data) != "kept" {
		t.Errorf("object after rejected calls = %q, want kept", data)
	}
}

// TestProvider_IgnoresReadOptions documents that dir ignores the S3-only
// Response* read options rather than erroring on them.
func TestProvider_IgnoresReadOptions(t *testing.T) {
	t.Parallel()
	p, _ := newTestProvider(t)
	put(t, p, "k", []byte("0123456789"))

	in := storage.HeadObjectInput{
		BaseObjectInput:     base("k"),
		ResponseContentType: new("text/x-override"),
	}
	h, err := p.HeadObject(context.Background(), &in)
	if err != nil {
		t.Fatalf("HeadObject() error: %v", err)
	}
	if h.Size != 10 || h.ContentType == "text/x-override" {
		t.Errorf("Head = size %d type %q, want 10 and the stored type", h.Size, h.ContentType)
	}
	obj, err := p.GetObject(context.Background(), &storage.GetObjectInput{HeadObjectInput: in})
	if err != nil {
		t.Fatalf("GetObject() error: %v", err)
	}
	defer func() { _ = obj.Body.Close() }()
	data, err := io.ReadAll(obj.Body)
	if err != nil || string(data) != "0123456789" {
		t.Errorf("body = %q, %v; want full object", data, err)
	}
}

// TestProvider_StoresAnyExpires checks that dir stores Expires as given, even
// text that is not an HTTP-date, where s3 returns storage.ErrInvalidHeader.
func TestProvider_StoresAnyExpires(t *testing.T) {
	t.Parallel()
	p, _ := newTestProvider(t)
	_, err := p.PutObject(context.Background(), &storage.PutObjectInput{
		BaseObjectInput: base("k"),
		Body:            strings.NewReader("0123456789"),
		ObjectHeaders:   storage.ObjectHeaders{Expires: new("0")},
	})
	if err != nil {
		t.Fatalf("PutObject() error: %v", err)
	}
	info, err := head(p, "k")
	if err != nil || info.Expires != "0" {
		t.Errorf("HeadObject() Expires = %v, %v; want 0", info, err)
	}
}

// TestProvider_PutReturnsTheObject checks that PutObject reports what
// HeadObject then reads: the ETag, size, modification time, content type and
// metadata, with invalid UTF-8 replaced as it is when stored.
func TestProvider_PutReturnsTheObject(t *testing.T) {
	t.Parallel()
	p, _ := newTestProvider(t)
	put, err := putWith(p, "k", strings.NewReader("body"), new("text/x-put"),
		map[string]string{"a": "1\xff"})
	if err != nil {
		t.Fatalf("PutObject() error: %v", err)
	}
	info, err := head(p, "k")
	if err != nil {
		t.Fatalf("HeadObject() error: %v", err)
	}
	if put.ETag == "" || put.Size != 4 || put.LastModified.IsZero() {
		t.Errorf("PutObject() = %+v, want an ETag, size 4 and a modification time", put)
	}
	if put.ETag != info.ETag || put.Size != info.Size ||
		!put.LastModified.Equal(info.LastModified) ||
		put.ContentType != info.ContentType ||
		put.Metadata["a"] != info.Metadata["a"] {
		t.Errorf("PutObject() = %+v, HeadObject() = %+v; want the same object", put, info)
	}
}

// TestProvider_ErrorPrefixes checks that every operation's errors carry its
// prefix exactly once.
func TestProvider_ErrorPrefixes(t *testing.T) {
	t.Parallel()
	p, _ := newTestProvider(t)
	ctx := context.Background()

	checks := map[string]error{}
	_, checks["create bucket"] = p.CreateBucket(
		ctx,
		&storage.CreateBucketInput{Bucket: "Bad_Bucket"},
	)
	_, checks["head"] = head(p, "nope")
	checks["get"] = getErr(p, "nope")
	_, checks["put"] = p.PutObject(
		ctx,
		&storage.PutObjectInput{BaseObjectInput: inBucket("nope-bucket")},
	)
	_, checks["delete"] = p.DeleteObject(ctx, &storage.DeleteObjectInput{BaseObjectInput: base("")})
	_, checks["head bucket"] = p.HeadBucket(ctx, &storage.HeadBucketInput{Bucket: "nope-bucket"})
	checks["delete bucket"] = p.DeleteBucket(ctx, &storage.DeleteBucketInput{Bucket: "nope-bucket"})

	for op, err := range checks {
		want := "gas/storage/dir/provider: " + op + ": "
		if err == nil {
			t.Errorf("%s error = nil, want an error", op)
			continue
		}
		if !strings.HasPrefix(err.Error(), want) {
			t.Errorf("%s error = %v, want prefix %q", op, err, want)
		}
		if strings.Count(err.Error(), "gas/storage/dir/provider") != 1 {
			t.Errorf("%s error = %q, want the prefix exactly once", op, err)
		}
	}
}

// TestProvider_ErrorsOmitUserData checks that keys, bucket names and metadata
// are user data: no error may quote them.
func TestProvider_ErrorsOmitUserData(t *testing.T) {
	t.Parallel()
	const secret = "secret-user-data"
	p, dir := newTestProvider(t)
	ctx := context.Background()
	key := "users/" + secret + "/avatar.png"
	bucket := secret + "-bucket"

	var errs []error
	collect := func(err error) {
		if err != nil {
			errs = append(errs, err)
		}
	}

	// Missing key and missing bucket.
	_, err := head(p, key)
	collect(err)
	collect(getErr(p, key))
	for _, err := range allOps(ctx, p, storage.BaseObjectInput{Bucket: new(bucket), Key: key}) {
		collect(err)
	}
	// Existing bucket.
	createBucket(t, p, bucket)
	_, err = p.CreateBucket(ctx, &storage.CreateBucketInput{Bucket: bucket})
	collect(err)
	// Corrupt metadata holding the key and metadata.
	if _, pErr := putWith(p, key, strings.NewReader("x"), nil,
		map[string]string{secret: secret}); pErr != nil {
		t.Fatal(pErr)
	}
	writeFile(t, metaFile(dir, key), []byte(`{"Key":"`+key+`","Metadata":{"`+secret+`":1}}`))
	_, err = head(p, key)
	collect(err)
	collect(getErr(p, key))
	_, err = del(p, key)
	collect(err)
	// Failing body.
	_, err = putWith(p, secret, iotest.ErrReader(errors.New("boom")), nil, nil)
	collect(err)

	if len(errs) < 10 {
		t.Fatalf("collected %d errors, want every call above to fail", len(errs))
	}
	for _, err := range errs {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("error quotes user data: %q", err)
		}
	}
}

// TestProvider_ClosedRoot checks that closing the root under a live provider
// makes every call fail with an I/O error that is not mistaken for a missing
// object or bucket.
func TestProvider_ClosedRoot(t *testing.T) {
	t.Parallel()
	p, _ := newTestProvider(t)
	put(t, p, "k", []byte("x"))
	if err := p.Root().Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for op, err := range allOps(ctx, p, base("k")) {
		wantErrIs(t, op, err, os.ErrClosed)
		wantPresentError(t, op, err)
	}
	_, err := p.CreateBucket(ctx, &storage.CreateBucketInput{Bucket: "other-bucket"})
	wantErrIs(t, "CreateBucket", err, os.ErrClosed)
}

// TestProvider_Concurrency runs Put, Get and Head of distinct keys
// concurrently.
func TestProvider_Concurrency(t *testing.T) {
	t.Parallel()
	p, dir := newTestProvider(t)

	const n = 32
	var wg sync.WaitGroup
	errs := make(chan error, n*3)
	for i := range n {
		wg.Go(func() {
			key := fmt.Sprintf("shared/dir/key-%d", i)
			body := fmt.Appendf(nil, "body-%d", i)
			if _, err := putWith(p, key, bytes.NewReader(body), nil, nil); err != nil {
				errs <- fmt.Errorf("put %s: %w", key, err)
				return
			}
			obj, err := p.GetObject(context.Background(), &storage.GetObjectInput{
				HeadObjectInput: storage.HeadObjectInput{BaseObjectInput: base(key)},
			})
			if err != nil {
				errs <- fmt.Errorf("get %s: %w", key, err)
				return
			}
			data, _ := io.ReadAll(obj.Body)
			_ = obj.Body.Close()
			if !bytes.Equal(data, body) {
				errs <- fmt.Errorf("get %s: body %q, want %q", key, data, body)
			}
			if _, err := head(p, key); err != nil {
				errs <- fmt.Errorf("head %s: %w", key, err)
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	assertNoTempFiles(t, dir)
}

// TestProvider_ConcurrentSameKey runs concurrent Puts of one key and checks
// one whole body survives.
func TestProvider_ConcurrentSameKey(t *testing.T) {
	t.Parallel()
	p, dir := newTestProvider(t)

	const n = 16
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			_, err := putWith(
				p,
				"same",
				bytes.NewReader(fmt.Appendf(nil, "body-%02d", i)),
				nil,
				nil,
			)
			if err != nil {
				t.Errorf("PutObject #%d error: %v", i, err)
			}
		})
	}
	wg.Wait()

	_, data := get(t, p, "same")
	if len(data) != len("body-00") || !strings.HasPrefix(string(data), "body-") {
		t.Errorf("final body = %q, want one complete body", data)
	}
	// Every overwrite removed the data file it replaced.
	if got := listDir(t, objDir(dir, testBucket)); len(got) != 1 {
		t.Errorf("objects dir = %v, want one data file", got)
	}
	assertNoTempFiles(t, dir)
}

// TestProvider_ConcurrentPutDeleteGet hammers a few keys with mixed
// operations under -race. Every Get either fails with ErrKeyNotFound or
// returns a body that matches its own content type.
func TestProvider_ConcurrentPutDeleteGet(t *testing.T) {
	t.Parallel()
	p, dir := newTestProvider(t)

	const workers, rounds = 8, 40
	var wg sync.WaitGroup
	errs := make(chan error, workers*rounds)
	for w := range workers {
		wg.Go(func() {
			for r := range rounds {
				if err := mixedOp(p, w, r); err != nil {
					errs <- err
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("unexpected error under concurrency: %v", err)
	}
	assertNoTempFiles(t, dir)
}

// mixedOp runs round r of worker w: a Put, Delete or Get of one of three
// keys. A Put stores its own token as both body and content type.
func mixedOp(p *Provider, w, r int) error {
	key := "hot/" + string(rune('a'+r%3))
	switch (w + r) % 3 {
	case 0:
		token := fmt.Sprintf("w%d-r%d", w, r)
		_, err := putWith(p, key, strings.NewReader(token), new("text/"+token), nil)
		return err
	case 1:
		_, err := del(p, key)
		return err
	}
	obj, err := p.GetObject(context.Background(), &storage.GetObjectInput{
		HeadObjectInput: storage.HeadObjectInput{BaseObjectInput: base(key)},
	})
	if errors.Is(err, storage.ErrKeyNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	data, rErr := io.ReadAll(obj.Body)
	if cErr := obj.Body.Close(); cErr != nil {
		return cErr
	}
	if rErr != nil {
		return rErr
	}
	if "text/"+string(data) != obj.ContentType {
		return fmt.Errorf("body %q served with content type %q", data, obj.ContentType)
	}
	return nil
}
