package storage_test

import (
	"context"
	"crypto/md5" //nolint:gosec // S3 ETags are MD5; this checks one, it protects nothing
	"encoding/hex"
	"strings"
	"testing"

	"github.com/gasmod/gas/storage"
)

// TestContract_Headers lists the header and ETag behavior both backends must
// share.
func TestContract_Headers(t *testing.T) {
	t.Parallel()

	cases := map[string]func(t *testing.T, p storage.Provider){
		"headers round trip":             contractHeadersRoundTrip,
		"headers not given":              contractHeadersNotGiven,
		"etag is the md5 of the body":    contractETag,
		"etag is listed":                 contractETagListed,
		"overwrite replaces the headers": contractHeadersOverwrite,
		"create bucket names the bucket": contractCreateBucketName,
	}
	for name, check := range cases {
		for _, be := range backends {
			t.Run(name+"/"+be.name, func(t *testing.T) {
				t.Parallel()
				check(t, be.mk(t).p)
			})
		}
	}
}

// expires is an HTTP-date, the one form of Expires both backends accept.
const expires = "Wed, 21 Oct 2026 07:28:00 GMT"

// allHeaders returns headers that set every field of storage.ObjectHeaders.
func allHeaders() storage.ObjectHeaders {
	return storage.ObjectHeaders{
		Metadata:           map[string]string{"owner": "contract"},
		CacheControl:       new("max-age=60"),
		ContentDisposition: new(`attachment; filename="a.txt"`),
		ContentEncoding:    new("identity"),
		ContentLanguage:    new("en-GB"),
		ContentType:        new("text/plain"),
		Expires:            new(expires),
	}
}

// headerFields renders the header fields of info, so a mismatch names the
// field.
func headerFields(info *storage.ObjectInfo) map[string]string {
	return map[string]string{
		"CacheControl":       info.CacheControl,
		"ContentDisposition": info.ContentDisposition,
		"ContentEncoding":    info.ContentEncoding,
		"ContentLanguage":    info.ContentLanguage,
		"ContentType":        info.ContentType,
		"Expires":            info.Expires,
		"Metadata":           metaString(info.Metadata),
	}
}

// wantHeaders checks that info carries every header allHeaders sets.
func wantHeaders(t *testing.T, op string, info *storage.ObjectInfo) {
	t.Helper()
	want := map[string]string{
		"CacheControl":       "max-age=60",
		"ContentDisposition": `attachment; filename="a.txt"`,
		"ContentEncoding":    "identity",
		"ContentLanguage":    "en-GB",
		"ContentType":        "text/plain",
		"Expires":            expires,
		"Metadata":           "{owner=contract}",
	}
	got := headerFields(info)
	for field, w := range want {
		if got[field] != w {
			t.Errorf("%s %s = %q, want %q", op, field, got[field], w)
		}
	}
}

// md5Hex returns the hex MD5 of s, unquoted, which is the ETag of an object
// written in one part.
func md5Hex(s string) string {
	sum := md5.Sum([]byte(s)) //nolint:gosec // see the import
	return hex.EncodeToString(sum[:])
}

// putHeaders puts body under k with h.
func putHeaders(
	p storage.Provider,
	k, body string,
	h storage.ObjectHeaders,
) (*storage.ObjectInfo, error) {
	return p.PutObject(context.Background(), &storage.PutObjectInput{
		BaseObjectInput: key(k),
		Body:            strings.NewReader(body),
		ObjectHeaders:   h,
	})
}

// contractHeadersRoundTrip writes an object with every header set and reads
// each back through HeadObject and GetObject.
func contractHeadersRoundTrip(t *testing.T, p storage.Provider) {
	if _, err := putHeaders(p, "headers", "body", allHeaders()); err != nil {
		t.Fatalf("PutObject() error: %v", err)
	}
	info, err := chead(p, key("headers"))
	if err != nil {
		t.Fatalf("HeadObject() error: %v", err)
	}
	wantHeaders(t, "HeadObject", info)
	obj, _, err := cget(p, storage.HeadObjectInput{BaseObjectInput: key("headers")})
	if err != nil {
		t.Fatalf("GetObject() error: %v", err)
	}
	wantHeaders(t, "GetObject", &obj.ObjectInfo)
}

// contractHeadersNotGiven writes an object with no headers: every header
// field but ContentType reads back empty.
func contractHeadersNotGiven(t *testing.T, p storage.Provider) {
	if _, err := putHeaders(p, "bare", "body", storage.ObjectHeaders{
		ContentType: new("text/plain"),
	}); err != nil {
		t.Fatalf("PutObject() error: %v", err)
	}
	info, err := chead(p, key("bare"))
	if err != nil {
		t.Fatalf("HeadObject() error: %v", err)
	}
	for field, v := range headerFields(info) {
		switch field {
		case "ContentType", "Metadata":
		default:
			if v != "" {
				t.Errorf("HeadObject %s = %q, want empty", field, v)
			}
		}
	}
}

// contractETag checks that PutObject, HeadObject and GetObject report the
// same ETag, the unquoted hex MD5 of the body.
func contractETag(t *testing.T, p storage.Provider) {
	const body = "etag body"
	want := md5Hex(body)
	put, err := putHeaders(p, "etag", body, storage.ObjectHeaders{})
	if err != nil {
		t.Fatalf("PutObject() error: %v", err)
	}
	if put.ETag != want {
		t.Errorf("PutObject ETag = %q, want %q", put.ETag, want)
	}
	info, err := chead(p, key("etag"))
	if err != nil {
		t.Fatalf("HeadObject() error: %v", err)
	}
	if info.ETag != want {
		t.Errorf("HeadObject ETag = %q, want %q", info.ETag, want)
	}
	obj, _, err := cget(p, storage.HeadObjectInput{BaseObjectInput: key("etag")})
	if err != nil {
		t.Fatalf("GetObject() error: %v", err)
	}
	if obj.ETag != want {
		t.Errorf("GetObject ETag = %q, want %q", obj.ETag, want)
	}
}

// contractETagListed checks that ListObjects reports each object's ETag.
func contractETagListed(t *testing.T, p storage.Provider) {
	if _, err := putHeaders(p, "listed", "listed body", storage.ObjectHeaders{}); err != nil {
		t.Fatalf("PutObject() error: %v", err)
	}
	out, err := p.ListObjects(context.Background(), &storage.ListObjectsInput{})
	if err != nil {
		t.Fatalf("ListObjects() error: %v", err)
	}
	if len(out.Objects) != 1 {
		t.Fatalf("ListObjects() = %d objects, want 1", len(out.Objects))
	}
	if got, want := out.Objects[0].ETag, md5Hex("listed body"); got != want {
		t.Errorf("ListObjects ETag = %q, want %q", got, want)
	}
}

// contractHeadersOverwrite checks that a second Put with no headers leaves
// none of the first Put's headers behind, and changes the ETag.
func contractHeadersOverwrite(t *testing.T, p storage.Provider) {
	if _, err := putHeaders(p, "over", "first", allHeaders()); err != nil {
		t.Fatalf("PutObject(first) error: %v", err)
	}
	if _, err := putHeaders(p, "over", "second", storage.ObjectHeaders{
		ContentType: new("text/plain"),
	}); err != nil {
		t.Fatalf("PutObject(second) error: %v", err)
	}
	info, err := chead(p, key("over"))
	if err != nil {
		t.Fatalf("HeadObject() error: %v", err)
	}
	if info.CacheControl != "" || info.Expires != "" || info.ContentLanguage != "" {
		t.Errorf("HeadObject after overwrite kept headers: %v", headerFields(info))
	}
	if info.ETag != md5Hex("second") {
		t.Errorf("HeadObject ETag = %q, want the second body's", info.ETag)
	}
}

// contractCreateBucketName checks that CreateBucket reports the bucket's
// name, for a new bucket and for one the caller already owns.
func contractCreateBucketName(t *testing.T, p storage.Provider) {
	ctx := context.Background()
	const bk = "contract-named-bucket"
	for _, when := range []string{"new", "owned"} {
		b, err := p.CreateBucket(ctx, &storage.CreateBucketInput{Bucket: bk})
		if err != nil {
			t.Fatalf("CreateBucket(%s) error: %v", when, err)
		}
		if b.Name != bk {
			t.Errorf("CreateBucket(%s) Name = %q, want %q", when, b.Name, bk)
		}
	}
}
