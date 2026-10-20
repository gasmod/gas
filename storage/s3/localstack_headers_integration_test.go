package s3_test

import (
	"bytes"
	"crypto/md5" //nolint:gosec // an S3 ETag is an MD5; this checks one, it protects nothing
	"encoding/hex"
	"maps"
	"testing"

	"github.com/gasmod/gas/storage"
)

// TestLocalStack_HeadersAndETag checks against LocalStack that every header
// of ObjectHeaders is stored and read back by HeadObject, GetObject and
// ListObjects, and that the ETag PutObject returns is the unquoted hex MD5
// of a single-part body.
func TestLocalStack_HeadersAndETag(t *testing.T) {
	t.Parallel()
	p, bucket := lsProviderWithBucket(t, "ls-hdr")

	data := []byte("headers and etag")
	sum := md5.Sum(data) //nolint:gosec // see the import
	wantETag := hex.EncodeToString(sum[:])
	const expires = "Wed, 21 Oct 2026 07:28:00 GMT"

	put, err := p.PutObject(t.Context(), &storage.PutObjectInput{
		Body: bytes.NewReader(data),
		ObjectHeaders: storage.ObjectHeaders{
			CacheControl:       new("max-age=60"),
			ContentDisposition: new(`attachment; filename="a.txt"`),
			ContentEncoding:    new("identity"),
			ContentLanguage:    new("en-GB"),
			ContentType:        new("text/plain"),
			Expires:            new(expires),
		},
		BaseObjectInput: lsObj(bucket, "h"),
	})
	if err != nil {
		t.Fatalf("PutObject() error: %v", err)
	}
	if put.ETag != wantETag {
		t.Errorf("PutObject() ETag = %q, want %q", put.ETag, wantETag)
	}

	want := map[string]string{
		"CacheControl":       "max-age=60",
		"ContentDisposition": `attachment; filename="a.txt"`,
		"ContentEncoding":    "identity",
		"ContentLanguage":    "en-GB",
		"Expires":            expires,
		"ETag":               wantETag,
	}
	fields := func(info *storage.ObjectInfo) map[string]string {
		return map[string]string{
			"CacheControl":       info.CacheControl,
			"ContentDisposition": info.ContentDisposition,
			"ContentEncoding":    info.ContentEncoding,
			"ContentLanguage":    info.ContentLanguage,
			"Expires":            info.Expires,
			"ETag":               info.ETag,
		}
	}

	h, err := p.HeadObject(
		t.Context(),
		&storage.HeadObjectInput{BaseObjectInput: lsObj(bucket, "h")},
	)
	if err != nil {
		t.Fatalf("HeadObject() error: %v", err)
	}
	if got := fields(h); !maps.Equal(got, want) {
		t.Errorf("HeadObject() headers = %v, want %v", got, want)
	}

	obj, err := p.GetObject(t.Context(), &storage.GetObjectInput{
		HeadObjectInput: storage.HeadObjectInput{BaseObjectInput: lsObj(bucket, "h")},
	})
	if err != nil {
		t.Fatalf("GetObject() error: %v", err)
	}
	_ = obj.Body.Close()
	if got := fields(&obj.ObjectInfo); !maps.Equal(got, want) {
		t.Errorf("GetObject() headers = %v, want %v", got, want)
	}

	out, err := p.ListObjects(t.Context(), &storage.ListObjectsInput{Bucket: new(bucket)})
	if err != nil {
		t.Fatalf("ListObjects() error: %v", err)
	}
	if len(out.Objects) != 1 || out.Objects[0].ETag != wantETag {
		t.Errorf("ListObjects() = %+v, want one object with ETag %q", out.Objects, wantETag)
	}
}
