package guidesfiles_test

import (
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/gasmod/gas/storage"
)

// Docs: guides/files.mdx#operations
// "`BaseObjectInput` carries the `Key` and an optional `Bucket`; a nil
// `Bucket` means the configured default bucket."
func TestNilBucketMeansConfiguredDefault(t *testing.T) {
	t.Parallel()
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()
			if _, err := b.p.PutObject(t.Context(), &storage.PutObjectInput{
				Body:            strings.NewReader("x"),
				BaseObjectInput: storage.BaseObjectInput{Key: "k"},
			}); err != nil {
				t.Fatalf("PutObject(nil Bucket) error: %v", err)
			}
			out, err := b.p.ListObjects(t.Context(), &storage.ListObjectsInput{Bucket: ptr(b.bucket)})
			if err != nil {
				t.Fatalf("ListObjects(%s) error: %v", b.bucket, err)
			}
			if len(out.Objects) != 1 || out.Objects[0].Key != "k" {
				t.Fatalf("objects in configured bucket = %+v, want [k]", out.Objects)
			}
		})
	}
}

// Docs: guides/files.mdx#operations
// "`PutObject(ctx, *PutObjectInput)` | `*ObjectInfo` | Streams the body;
// headers, metadata, length and a write condition are optional. ... Returns
// the ETag"
func TestPutObjectStreamsAndReturnsETag(t *testing.T) {
	t.Parallel()
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()
			pr, pw := io.Pipe()
			go func() {
				_, _ = pw.Write([]byte("streamed "))
				_, _ = pw.Write([]byte("body"))
				_ = pw.Close()
			}()
			info, err := b.p.PutObject(t.Context(), &storage.PutObjectInput{
				Body:            pr, // not seekable, length unknown
				BaseObjectInput: obj("stream"),
			})
			if err != nil {
				t.Fatalf("PutObject() error: %v", err)
			}
			if info == nil || info.ETag == "" {
				t.Fatalf("PutObject() ObjectInfo = %+v, want an ETag", info)
			}
			if got := readObject(t, b.p, "stream"); got != "streamed body" {
				t.Fatalf("body = %q, want %q", got, "streamed body")
			}
			head, err := b.p.HeadObject(t.Context(), &storage.HeadObjectInput{BaseObjectInput: obj("stream")})
			if err != nil {
				t.Fatalf("HeadObject() error: %v", err)
			}
			if head.ETag != info.ETag {
				t.Fatalf("PutObject ETag = %q, HeadObject ETag = %q, want equal", info.ETag, head.ETag)
			}
		})
	}
}

// Docs: guides/files.mdx#operations
// "A set `ContentLength` must match the body."
// Docs: guides/files.mdx#errors-and-testing
// "`ErrContentLengthMismatch` | A `PutObject` whose body does not match its
// `ContentLength`; nothing is stored"
func TestContentLengthMismatch(t *testing.T) {
	t.Parallel()
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()
			mustPut(t, b.p, "existing", "original")
			for _, tc := range []struct {
				name string
				body string
				n    int64
			}{
				{"shorter", "abc", 5},
				{"longer", "abcdefg", 5},
			} {
				for _, key := range []string{"existing", "new-" + tc.name} {
					_, err := b.p.PutObject(t.Context(), &storage.PutObjectInput{
						Body:            strings.NewReader(tc.body),
						ContentLength:   ptr(tc.n),
						BaseObjectInput: obj(key),
					})
					wantErr(t, tc.name+" "+key, err, storage.ErrContentLengthMismatch)
				}
				if got := readObject(t, b.p, "existing"); got != "original" {
					t.Fatalf("%s: existing body = %q, want %q (nothing stored)", tc.name, got, "original")
				}
				_, err := b.p.HeadObject(t.Context(), &storage.HeadObjectInput{BaseObjectInput: obj("new-" + tc.name)})
				wantErr(t, tc.name+": HeadObject of new key", err, storage.ErrNotFound)
			}
			if _, err := b.p.PutObject(t.Context(), &storage.PutObjectInput{
				Body:            strings.NewReader("abcde"),
				ContentLength:   ptr(int64(5)),
				BaseObjectInput: obj("exact"),
			}); err != nil {
				t.Fatalf("PutObject with matching ContentLength error: %v", err)
			}
		})
	}
}

// Docs: guides/files.mdx#operations
// "`GetObject(ctx, *GetObjectInput)` | `*Object` | Body plus `ObjectInfo`"
func TestGetObjectBodyPlusInfo(t *testing.T) {
	t.Parallel()
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()
			if _, err := b.p.PutObject(t.Context(), &storage.PutObjectInput{
				Body:            strings.NewReader("hello"),
				ObjectHeaders:   storage.ObjectHeaders{ContentType: ptr("text/plain")},
				BaseObjectInput: obj("g"),
			}); err != nil {
				t.Fatalf("PutObject() error: %v", err)
			}
			o, err := b.p.GetObject(t.Context(), &storage.GetObjectInput{HeadObjectInput: storage.HeadObjectInput{BaseObjectInput: obj("g")}})
			if err != nil {
				t.Fatalf("GetObject() error: %v", err)
			}
			defer func() { _ = o.Body.Close() }()
			body, _ := io.ReadAll(o.Body)
			if string(body) != "hello" || o.Size != 5 || o.ContentType != "text/plain" || o.ETag == "" {
				t.Fatalf("GetObject() = body %q, info %+v, want hello with Size 5, text/plain, an ETag", body, o.ObjectInfo)
			}
		})
	}
}

// Docs: guides/files.mdx#operations
// "`HeadObject(ctx, *HeadObjectInput)` | `*ObjectInfo` | Size, content type,
// modification time, ETag, version, metadata and stored headers"
func TestHeadObjectFields(t *testing.T) {
	t.Parallel()
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()
			if _, err := b.p.PutObject(t.Context(), &storage.PutObjectInput{
				Body: strings.NewReader("12345678"),
				ObjectHeaders: storage.ObjectHeaders{
					ContentType:  ptr("application/x-test"),
					CacheControl: ptr("no-cache"),
					Metadata:     map[string]string{"owner": "alice"},
				},
				BaseObjectInput: obj("h"),
			}); err != nil {
				t.Fatalf("PutObject() error: %v", err)
			}
			info, err := b.p.HeadObject(t.Context(), &storage.HeadObjectInput{BaseObjectInput: obj("h")})
			if err != nil {
				t.Fatalf("HeadObject() error: %v", err)
			}
			if info.Size != 8 || info.ContentType != "application/x-test" || info.LastModified.IsZero() ||
				info.ETag == "" || info.Metadata["owner"] != "alice" || info.CacheControl != "no-cache" {
				t.Fatalf("HeadObject() = %+v, want Size 8, content type, LastModified, ETag, metadata, Cache-Control", info)
			}
		})
	}
}

// Docs: guides/files.mdx#operations
// "`HeadObject(ctx, *HeadObjectInput)` | `*ObjectInfo` | Size, content type,
// modification time, ETag, version, metadata and stored headers"
// The version part, on a versioned S3 bucket (the dir backend does not version).
func TestHeadObjectVersionS3(t *testing.T) {
	t.Parallel()
	b := s3Backend(t)
	enableVersioning(t, b)
	put := mustPut(t, b.p, "v", "one")
	info, err := b.p.HeadObject(t.Context(), &storage.HeadObjectInput{BaseObjectInput: obj("v")})
	if err != nil {
		t.Fatalf("HeadObject() error: %v", err)
	}
	if info.Version == "" || info.Version != put.Version {
		t.Fatalf("HeadObject().Version = %q, PutObject().Version = %q, want equal and non-empty", info.Version, put.Version)
	}
}

// Docs: guides/files.mdx#operations
// "`DeleteObject(ctx, *DeleteObjectInput)` | `*ObjectInfo` | Deleting a
// missing key succeeds"
func TestDeleteMissingKeySucceeds(t *testing.T) {
	t.Parallel()
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()
			if _, err := b.p.DeleteObject(t.Context(), &storage.DeleteObjectInput{BaseObjectInput: obj("never-written")}); err != nil {
				t.Fatalf("DeleteObject(missing) error = %v, want nil", err)
			}
		})
	}
}

// Docs: guides/files.mdx#operations
// "`GetObject` returns an open body the caller must close, and reports a
// missing key as `storage.ErrKeyNotFound`."
// Docs: guides/files.mdx#errors-and-testing
// "`ErrKeyNotFound` | `GetObject` of a missing key"
func TestGetMissingKey(t *testing.T) {
	t.Parallel()
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()
			_, err := b.p.GetObject(t.Context(), &storage.GetObjectInput{HeadObjectInput: storage.HeadObjectInput{BaseObjectInput: obj("missing")}})
			wantErr(t, "GetObject(missing)", err, storage.ErrKeyNotFound)
		})
	}
}

func getRange(t *testing.T, p storage.Provider, key, rng string) (string, *storage.Object, error) {
	t.Helper()
	o, err := p.GetObject(t.Context(), &storage.GetObjectInput{HeadObjectInput: storage.HeadObjectInput{
		Range: ptr(rng), BaseObjectInput: obj(key),
	}})
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = o.Body.Close() }()
	b, err := io.ReadAll(o.Body)
	if err != nil {
		t.Fatalf("read range %q: %v", rng, err)
	}
	return string(b), o, nil
}

// Docs: guides/files.mdx#operations
// "A `Range` such as `bytes=0-1023` reads part of the object in both backends,
// and `Size` is then the length of that part."
func TestRangeReadsPart(t *testing.T) {
	t.Parallel()
	full := strings.Repeat("0123456789", 300) // 3000 bytes
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()
			mustPut(t, b.p, "r", full)
			got, o, err := getRange(t, b.p, "r", "bytes=0-1023")
			if err != nil {
				t.Fatalf("GetObject(bytes=0-1023) error: %v", err)
			}
			if got != full[:1024] || o.Size != 1024 {
				t.Fatalf("GetObject(bytes=0-1023) = %d bytes, Size %d, want the first 1024 bytes and Size 1024", len(got), o.Size)
			}
		})
	}
}

// Docs: guides/files.mdx#operations
// "The directory backend ignores a header that is not one valid byte range and
// returns `storage.ErrInvalidRange` for one starting past the end."
func TestDirRangeHandling(t *testing.T) {
	t.Parallel()
	const full = "0123456789"
	d := dirBackend(t)
	mustPut(t, d.p, "r", full)
	for _, rng := range []string{"bytes=5-2", "items=0-1", "bytes=0-1,4-5", "bytes=abc", "0-1"} {
		got, o, err := getRange(t, d.p, "r", rng)
		if err != nil {
			t.Errorf("dir GetObject(Range %q) error = %v, want the header ignored", rng, err)
			continue
		}
		if got != full || o.Size != int64(len(full)) {
			t.Errorf("dir GetObject(Range %q) = %q, Size %d, want the whole object %q", rng, got, o.Size, full)
		}
	}
	for _, rng := range []string{"bytes=20-30", "bytes=10-", "bytes=10-12"} {
		if _, _, err := getRange(t, d.p, "r", rng); !errors.Is(err, storage.ErrInvalidRange) {
			t.Errorf("dir GetObject(Range %q) error = %v, want ErrInvalidRange", rng, err)
		}
	}
}

// Docs: guides/files.mdx#operations
// "The S3 backend sends the header as given, so the result is S3's, and S3's
// `416` comes back as `storage.ErrInvalidRange`."
func TestS3RangeSentAsGiven(t *testing.T) {
	t.Parallel()
	t.Run("header as given", func(t *testing.T) {
		t.Parallel()
		var mu sync.Mutex
		var sent []string
		f := newFakeS3(t, func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			sent = append(sent, r.Header.Get("Range"))
			mu.Unlock()
			w.Header().Set("Content-Length", "1")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("x"))
		})
		p := f.provider(t, "us-east-1", "b-range")
		given := []string{"bytes=0-1", "bytes=5-2", "items=0-1", "bytes=0-1,4-5", "bytes=abc"}
		for _, rng := range given {
			o, err := p.GetObject(t.Context(), &storage.GetObjectInput{HeadObjectInput: storage.HeadObjectInput{
				Range: ptr(rng), BaseObjectInput: obj("k"),
			}})
			if err == nil {
				_ = o.Body.Close()
			}
		}
		mu.Lock()
		defer mu.Unlock()
		if !slices.Equal(sent, given) {
			t.Fatalf("Range headers sent = %q, want exactly %q", sent, given)
		}
	})
	t.Run("416 is ErrInvalidRange", func(t *testing.T) {
		t.Parallel()
		f := newFakeS3(t, func(w http.ResponseWriter, _ *http.Request) {
			s3Error(w, http.StatusRequestedRangeNotSatisfiable, "InvalidRange")
		})
		p := f.provider(t, "us-east-1", "b-range")
		_, err := p.GetObject(t.Context(), &storage.GetObjectInput{HeadObjectInput: storage.HeadObjectInput{
			Range: ptr("bytes=20-30"), BaseObjectInput: obj("k"),
		}})
		wantErr(t, "s3 GetObject answered 416", err, storage.ErrInvalidRange)
	})
	t.Run("localstack", func(t *testing.T) {
		t.Parallel()
		b := s3Backend(t)
		mustPut(t, b.p, "r", "0123456789")
		_, _, err := getRange(t, b.p, "r", "bytes=20-30")
		wantErr(t, "s3 GetObject(Range bytes=20-30) on LocalStack", err, storage.ErrInvalidRange)
	})
}

// Docs: guides/files.mdx#errors-and-testing
// "`ErrInvalidRange` | A `Range` whose first byte is at or past the end of the
// object, or `bytes=-0`"
func TestUnsatisfiableRange(t *testing.T) {
	t.Parallel()
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()
			mustPut(t, b.p, "r", "0123456789")
			for _, rng := range []string{"bytes=20-30", "bytes=10-", "bytes=10-12", "bytes=-0"} {
				_, _, err := getRange(t, b.p, "r", rng)
				if !errors.Is(err, storage.ErrInvalidRange) {
					t.Errorf("GetObject(Range %q) error = %v, want ErrInvalidRange", rng, err)
				}
			}
		})
	}
}

// Docs: guides/files.mdx#operations
// "every header a write sets reads back on `ObjectInfo`."
func TestHeadersReadBack(t *testing.T) {
	t.Parallel()
	const expires = "Wed, 21 Oct 2026 07:28:00 GMT"
	in := storage.ObjectHeaders{
		ContentType:        ptr("text/csv"),
		CacheControl:       ptr("max-age=60"),
		ContentDisposition: ptr(`attachment; filename="r.csv"`),
		ContentEncoding:    ptr("identity"),
		ContentLanguage:    ptr("en-GB"),
		Expires:            ptr(expires),
		Metadata:           map[string]string{"source": "api"},
	}
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()
			if _, err := b.p.PutObject(t.Context(), &storage.PutObjectInput{
				Body: strings.NewReader("a,b"), ObjectHeaders: in, BaseObjectInput: obj("hdr"),
			}); err != nil {
				t.Fatalf("PutObject() error: %v", err)
			}
			head, err := b.p.HeadObject(t.Context(), &storage.HeadObjectInput{BaseObjectInput: obj("hdr")})
			if err != nil {
				t.Fatalf("HeadObject() error: %v", err)
			}
			o, err := b.p.GetObject(t.Context(), &storage.GetObjectInput{HeadObjectInput: storage.HeadObjectInput{BaseObjectInput: obj("hdr")}})
			if err != nil {
				t.Fatalf("GetObject() error: %v", err)
			}
			_ = o.Body.Close()
			for name, info := range map[string]storage.ObjectInfo{"HeadObject": *head, "GetObject": o.ObjectInfo} {
				if info.ContentType != *in.ContentType || info.CacheControl != *in.CacheControl ||
					info.ContentDisposition != *in.ContentDisposition || info.ContentEncoding != *in.ContentEncoding ||
					info.ContentLanguage != *in.ContentLanguage || info.Expires != expires ||
					info.Metadata["source"] != "api" {
					t.Errorf("%s ObjectInfo = %+v, want every header and metadata as written", name, info)
				}
			}
		})
	}
}

// Docs: guides/files.mdx#operations
// "`Expires` is text; the S3 backend accepts only an IMF-fixdate such as
// `Wed, 21 Oct 2026 07:28:00 GMT`."
// Docs: guides/files.mdx#errors-and-testing
// "`ErrInvalidHeader` | An S3 `PutObject` or presigned upload whose `Expires`
// is not an IMF-fixdate"
func TestExpiresText(t *testing.T) {
	t.Parallel()
	notIMF := []string{
		"tomorrow",
		"Wednesday, 21-Oct-26 07:28:00 GMT", // RFC 850
		"Wed Oct 21 07:28:00 2026",          // asctime
		"2026-10-21T07:28:00Z",
	}
	t.Run("s3", func(t *testing.T) {
		t.Parallel()
		b := s3Backend(t)
		const imf = "Wed, 21 Oct 2026 07:28:00 GMT"
		if _, err := b.p.PutObject(t.Context(), &storage.PutObjectInput{
			Body: strings.NewReader("x"), ObjectHeaders: storage.ObjectHeaders{Expires: ptr(imf)}, BaseObjectInput: obj("e"),
		}); err != nil {
			t.Fatalf("PutObject(Expires %q) error = %v, want accepted", imf, err)
		}
		pre := newS3Presigner(t, "us-east-1", b.bucket)
		for _, v := range notIMF {
			_, err := b.p.PutObject(t.Context(), &storage.PutObjectInput{
				Body: strings.NewReader("x"), ObjectHeaders: storage.ObjectHeaders{Expires: ptr(v)}, BaseObjectInput: obj("bad"),
			})
			wantErr(t, "PutObject(Expires "+v+")", err, storage.ErrInvalidHeader)
			_, err = pre.PresignUploadURL(t.Context(), &storage.PresignUploadURLInput{PutObjectInput: storage.PutObjectInput{
				ObjectHeaders: storage.ObjectHeaders{Expires: ptr(v)}, BaseObjectInput: obj("bad"),
			}})
			wantErr(t, "PresignUploadURL(Expires "+v+")", err, storage.ErrInvalidHeader)
		}
		_, err := b.p.HeadObject(t.Context(), &storage.HeadObjectInput{BaseObjectInput: obj("bad")})
		wantErr(t, "HeadObject after rejected puts", err, storage.ErrNotFound)
	})
	t.Run("dir", func(t *testing.T) {
		t.Parallel()
		b := dirBackend(t)
		for _, v := range notIMF {
			if _, err := b.p.PutObject(t.Context(), &storage.PutObjectInput{
				Body: strings.NewReader("x"), ObjectHeaders: storage.ObjectHeaders{Expires: ptr(v)}, BaseObjectInput: obj("e"),
			}); err != nil {
				t.Fatalf("dir PutObject(Expires %q) error: %v", v, err)
			}
			info, err := b.p.HeadObject(t.Context(), &storage.HeadObjectInput{BaseObjectInput: obj("e")})
			if err != nil {
				t.Fatalf("HeadObject() error: %v", err)
			}
			if info.Expires != v {
				t.Fatalf("dir Expires = %q, want the text %q", info.Expires, v)
			}
		}
	})
}

// Docs: guides/files.mdx#operations
// "Every object reports an `ETag`, unquoted; compare ETags only for equality."
func TestETagUnquoted(t *testing.T) {
	t.Parallel()
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()
			put := mustPut(t, b.p, "et", "body")
			head, err := b.p.HeadObject(t.Context(), &storage.HeadObjectInput{BaseObjectInput: obj("et")})
			if err != nil {
				t.Fatalf("HeadObject() error: %v", err)
			}
			o, err := b.p.GetObject(t.Context(), &storage.GetObjectInput{HeadObjectInput: storage.HeadObjectInput{BaseObjectInput: obj("et")}})
			if err != nil {
				t.Fatalf("GetObject() error: %v", err)
			}
			_ = o.Body.Close()
			list, err := b.p.ListObjects(t.Context(), &storage.ListObjectsInput{})
			if err != nil {
				t.Fatalf("ListObjects() error: %v", err)
			}
			etags := map[string]string{"PutObject": put.ETag, "HeadObject": head.ETag, "GetObject": o.ETag}
			if len(list.Objects) == 1 {
				etags["ListObjects"] = list.Objects[0].ETag
			} else {
				t.Fatalf("ListObjects() = %+v, want one object", list.Objects)
			}
			for where, e := range etags {
				if e == "" || strings.Contains(e, `"`) {
					t.Errorf("%s ETag = %q, want non-empty and unquoted", where, e)
				}
				if e != put.ETag {
					t.Errorf("%s ETag = %q, PutObject ETag = %q, want equal", where, e, put.ETag)
				}
			}
		})
	}
}

// Docs: guides/files.mdx#operations
// "`IfNoneMatch: "*"` writes only a key with no object"
// Docs: guides/files.mdx#errors-and-testing
// "`ErrPreconditionFailed` | A `PutObject` whose `WriteCondition` does not
// hold; nothing is written"
func TestIfNoneMatch(t *testing.T) {
	t.Parallel()
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()
			put := func(body string) error {
				_, err := b.p.PutObject(t.Context(), &storage.PutObjectInput{
					Body:            strings.NewReader(body),
					WriteCondition:  storage.WriteCondition{IfNoneMatch: ptr("*")},
					BaseObjectInput: obj("inm"),
				})
				return err
			}
			if err := put("first"); err != nil {
				t.Fatalf("PutObject(IfNoneMatch *) on a new key error = %v, want nil", err)
			}
			wantErr(t, "PutObject(IfNoneMatch *) over an object", put("second"), storage.ErrPreconditionFailed)
			if got := readObject(t, b.p, "inm"); got != "first" {
				t.Fatalf("body = %q, want %q (nothing written)", got, "first")
			}
		})
	}
}

// Docs: guides/files.mdx#operations
// "`IfMatch` only over the object with that ETag, so an update can be made
// conditional on what was read."
func TestIfMatch(t *testing.T) {
	t.Parallel()
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()
			mustPut(t, b.p, "im", "v1")
			read, err := b.p.HeadObject(t.Context(), &storage.HeadObjectInput{BaseObjectInput: obj("im")})
			if err != nil {
				t.Fatalf("HeadObject() error: %v", err)
			}
			put := func(etag, body string) error {
				_, err := b.p.PutObject(t.Context(), &storage.PutObjectInput{
					Body:            strings.NewReader(body),
					WriteCondition:  storage.WriteCondition{IfMatch: ptr(etag)},
					BaseObjectInput: obj("im"),
				})
				return err
			}
			if err := put(read.ETag, "v2"); err != nil {
				t.Fatalf("PutObject(IfMatch read ETag) error = %v, want nil", err)
			}
			wantErr(t, "PutObject(IfMatch stale ETag)", put(read.ETag, "v3"), storage.ErrPreconditionFailed)
			if got := readObject(t, b.p, "im"); got != "v2" {
				t.Fatalf("body = %q, want %q (stale write not applied)", got, "v2")
			}
		})
	}
}

// Docs: guides/files.mdx#errors-and-testing
// "`ErrInvalidCondition` | A `WriteCondition` that cannot be evaluated, such
// as an `IfNoneMatch` other than `*`"
func TestInvalidCondition(t *testing.T) {
	t.Parallel()
	conds := map[string]storage.WriteCondition{
		"IfNoneMatch etag": {IfNoneMatch: ptr("abc")},
		"IfMatch empty":    {IfMatch: ptr("")},
		"both":             {IfMatch: ptr("abc"), IfNoneMatch: ptr("*")},
	}
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()
			for name, c := range conds {
				_, err := b.p.PutObject(t.Context(), &storage.PutObjectInput{
					Body: strings.NewReader("x"), WriteCondition: c, BaseObjectInput: obj("ic"),
				})
				wantErr(t, "PutObject("+name+")", err, storage.ErrInvalidCondition)
			}
			_, err := b.p.HeadObject(t.Context(), &storage.HeadObjectInput{BaseObjectInput: obj("ic")})
			wantErr(t, "HeadObject after invalid conditions", err, storage.ErrNotFound)
		})
	}
	t.Run("s3 presigner", func(t *testing.T) {
		t.Parallel()
		f := newFakeS3(t, nil)
		pre := f.presigner(t, "us-east-1", "bkt")
		for name, c := range conds {
			_, err := pre.PresignUploadURL(t.Context(), &storage.PresignUploadURLInput{PutObjectInput: storage.PutObjectInput{
				WriteCondition: c, BaseObjectInput: obj("ic"),
			}})
			wantErr(t, "PresignUploadURL("+name+")", err, storage.ErrInvalidCondition)
		}
	})
}

// Docs: guides/files.mdx#errors-and-testing
// "`ErrNotFound` | `HeadObject` of a missing object; on S3 also a missing
// bucket, because HEAD responses carry no error code"
func TestHeadObjectNotFound(t *testing.T) {
	t.Parallel()
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()
			_, err := b.p.HeadObject(t.Context(), &storage.HeadObjectInput{BaseObjectInput: obj("missing")})
			wantErr(t, "HeadObject(missing key)", err, storage.ErrNotFound)
			if errors.Is(err, storage.ErrKeyNotFound) {
				t.Fatalf("HeadObject(missing key) error matches ErrKeyNotFound too: %v", err)
			}
		})
	}
	t.Run("s3 missing bucket", func(t *testing.T) {
		t.Parallel()
		b := s3Backend(t)
		_, err := b.p.HeadObject(t.Context(), &storage.HeadObjectInput{
			BaseObjectInput: storage.BaseObjectInput{Bucket: ptr(uniqueName("nobucket")), Key: "k"},
		})
		wantErr(t, "s3 HeadObject(missing bucket)", err, storage.ErrNotFound)
	})
}

// Docs: guides/files.mdx#errors-and-testing
// "`ErrBucketNotFound` | An object operation, `ListObjects`, `HeadBucket` or
// `DeleteBucket` on a missing bucket (S3 `HeadObject` excepted)"
func TestBucketNotFound(t *testing.T) {
	t.Parallel()
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()
			missing := uniqueName("nobucket")
			in := storage.BaseObjectInput{Bucket: ptr(missing), Key: "k"}
			ctx := t.Context()
			_, err := b.p.PutObject(ctx, &storage.PutObjectInput{Body: strings.NewReader("x"), BaseObjectInput: in})
			wantErr(t, "PutObject", err, storage.ErrBucketNotFound)
			_, err = b.p.GetObject(ctx, &storage.GetObjectInput{HeadObjectInput: storage.HeadObjectInput{BaseObjectInput: in}})
			wantErr(t, "GetObject", err, storage.ErrBucketNotFound)
			_, err = b.p.DeleteObject(ctx, &storage.DeleteObjectInput{BaseObjectInput: in})
			wantErr(t, "DeleteObject", err, storage.ErrBucketNotFound)
			if b.s3 == nil {
				_, err = b.p.HeadObject(ctx, &storage.HeadObjectInput{BaseObjectInput: in})
				wantErr(t, "HeadObject", err, storage.ErrBucketNotFound)
			}
			_, err = b.p.ListObjects(ctx, &storage.ListObjectsInput{Bucket: ptr(missing)})
			wantErr(t, "ListObjects", err, storage.ErrBucketNotFound)
			_, err = b.p.HeadBucket(ctx, &storage.HeadBucketInput{Bucket: missing})
			wantErr(t, "HeadBucket", err, storage.ErrBucketNotFound)
			err = b.p.DeleteBucket(ctx, &storage.DeleteBucketInput{Bucket: missing})
			wantErr(t, "DeleteBucket", err, storage.ErrBucketNotFound)
		})
	}
}

// Docs: guides/files.mdx#errors-and-testing
// "`ErrNotSupported` | A feature the backend does not implement, such as
// `Version` or `PartNumber` on the directory backend"
func TestDirNotSupported(t *testing.T) {
	t.Parallel()
	b := dirBackend(t)
	mustPut(t, b.p, "k", "x")
	ctx := t.Context()
	cases := map[string]storage.HeadObjectInput{
		"Version":    {Version: ptr("v1"), BaseObjectInput: obj("k")},
		"PartNumber": {PartNumber: ptr(int32(1)), BaseObjectInput: obj("k")},
	}
	for name, in := range cases {
		_, err := b.p.HeadObject(ctx, &in)
		wantErr(t, "HeadObject("+name+")", err, storage.ErrNotSupported)
		_, err = b.p.GetObject(ctx, &storage.GetObjectInput{HeadObjectInput: in})
		wantErr(t, "GetObject("+name+")", err, storage.ErrNotSupported)
	}
	_, err := b.p.DeleteObject(ctx, &storage.DeleteObjectInput{Version: ptr("v1"), BaseObjectInput: obj("k")})
	wantErr(t, "DeleteObject(Version)", err, storage.ErrNotSupported)
	if got := readObject(t, b.p, "k"); got != "x" {
		t.Fatalf("body after DeleteObject(Version) = %q, want unchanged", got)
	}
}

// Docs: guides/files.mdx#errors-and-testing
// "`ErrVersionNotFound` | S3 `GetObject` of a version that does not exist. S3
// `HeadObject` reports it as `ErrNotFound`"
func TestVersionNotFoundS3(t *testing.T) {
	t.Parallel()
	b := s3Backend(t)
	enableVersioning(t, b)
	mustPut(t, b.p, "a", "a")
	other := mustPut(t, b.p, "b", "b") // a real version ID that key "a" does not have
	if other.Version == "" {
		t.Fatalf("PutObject on a versioned bucket returned no Version")
	}
	in := storage.HeadObjectInput{Version: ptr(other.Version), BaseObjectInput: obj("a")}
	_, err := b.p.GetObject(t.Context(), &storage.GetObjectInput{HeadObjectInput: in})
	wantErr(t, "GetObject(unknown version)", err, storage.ErrVersionNotFound)
	_, err = b.p.HeadObject(t.Context(), &in)
	wantErr(t, "HeadObject(unknown version)", err, storage.ErrNotFound)
	if errors.Is(err, storage.ErrVersionNotFound) {
		t.Fatalf("HeadObject(unknown version) error also matches ErrVersionNotFound: %v", err)
	}
}

// keysOf returns the keys of objects.
func keysOf(objects []storage.ObjectSummary) []string {
	keys := make([]string, 0, len(objects))
	for _, o := range objects {
		keys = append(keys, o.Key)
	}
	return keys
}
