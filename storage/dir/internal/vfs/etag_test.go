package vfs

import (
	"crypto/md5" //nolint:gosec // the ETag is an MD5 by definition; it protects nothing
	"encoding/hex"
	"strings"
	"testing"
)

// TestCreateObject_ETagStatAndHeaders checks that CreateObject records the
// hex MD5 of the body and the headers it was given, and reports the size and
// modification time OpenObject then reads.
func TestCreateObject_ETagStatAndHeaders(t *testing.T) {
	v, _ := newTestVFS(t)
	b := newTestBucket(t, v)

	const body = "the body"
	out, err := b.CreateObject(t.Context(), &CreateObjectInput{
		Key:  "k",
		Body: strings.NewReader(body),
		Headers: Headers{
			CacheControl:       "no-cache",
			ContentDisposition: "inline",
			ContentEncoding:    "identity",
			ContentLanguage:    "de",
			Expires:            "not a date",
		},
	})
	if err != nil {
		t.Fatalf("CreateObject: %v", err)
	}
	sum := md5.Sum([]byte(body)) //nolint:gosec // see the import
	if want := hex.EncodeToString(sum[:]); out.ETag != want {
		t.Errorf("CreateObject ETag = %q, want %q", out.ETag, want)
	}

	_, obj := getObject(t, b, "k")
	if obj.ETag != out.ETag {
		t.Errorf("OpenObject ETag = %q, want %q", obj.ETag, out.ETag)
	}
	if out.Size != int64(len(body)) || out.Size != obj.Size {
		t.Errorf(
			"CreateObject Size = %d, OpenObject Size = %d, want %d",
			out.Size,
			obj.Size,
			len(body),
		)
	}
	if out.LastModified.IsZero() || !out.LastModified.Equal(obj.LastModified) {
		t.Errorf("CreateObject LastModified = %v, OpenObject = %v; want equal, non-zero",
			out.LastModified, obj.LastModified)
	}
	if obj.Headers != out.Headers || obj.Expires != "not a date" || obj.ContentLanguage != "de" {
		t.Errorf("OpenObject Headers = %+v, want %+v", obj.Headers, out.Headers)
	}
}

// TestOpenObject_LegacyMetaHasNoETag checks that metadata written before
// ETags were recorded reads back with an empty ETag and no headers.
func TestOpenObject_LegacyMetaHasNoETag(t *testing.T) {
	v, _ := newTestVFS(t)
	b := newTestBucket(t, v)
	putObject(t, b, "k", "old", "text/plain")

	meta, err := b.meta(hash("k"))
	if err != nil {
		t.Fatal(err)
	}
	legacy := `{"Key":"k","ContentType":"text/plain","Metadata":null,"DataFile":"` + meta.DataFile + `"}`
	if err := b.metaRoot.WriteFile(hash("k")+metaFileExt, []byte(legacy), filePerm); err != nil {
		t.Fatal(err)
	}

	body, obj := getObject(t, b, "k")
	if body != "old" || obj.ETag != "" || obj.Headers != (Headers{}) {
		t.Errorf("legacy object = %q, ETag %q, Headers %+v; want old, empty, none",
			body, obj.ETag, obj.Headers)
	}
}

// TestCreateObject_ReturnsWhatOpenReads checks that CreateObject returns the
// metadata as it was stored, not as it was given: invalid UTF-8 comes back
// as U+FFFD, as OpenObject reads it, and the returned map is not the
// caller's.
func TestCreateObject_ReturnsWhatOpenReads(t *testing.T) {
	v, _ := newTestVFS(t)
	b := newTestBucket(t, v)

	md := map[string]string{"k": "bad\xffvalue"}
	out, err := b.CreateObject(t.Context(), &CreateObjectInput{
		Key:      "k",
		Body:     strings.NewReader("body"),
		Metadata: md,
		Headers:  Headers{ContentLanguage: "bad\xfflang"},
	})
	if err != nil {
		t.Fatalf("CreateObject: %v", err)
	}
	_, obj := getObject(t, b, "k")
	if out.Metadata["k"] != obj.Metadata["k"] || out.ContentLanguage != obj.ContentLanguage {
		t.Errorf("CreateObject = %q, %q; OpenObject = %q, %q; want equal",
			out.Metadata["k"], out.ContentLanguage, obj.Metadata["k"], obj.ContentLanguage)
	}
	if want := "bad�value"; out.Metadata["k"] != want {
		t.Errorf("CreateObject metadata = %q, want %q", out.Metadata["k"], want)
	}
	md["k"] = "changed"
	if out.Metadata["k"] == "changed" {
		t.Error("CreateObject returned the caller's metadata map")
	}
}
