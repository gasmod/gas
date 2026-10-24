package modulesstorage_test

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gasmod/gas/storage"
)

// Docs: modules/storage.mdx#operations
// "`PutObject(ctx, *PutObjectInput)` | `*ObjectInfo` | Streams the body and
// replaces any object under the key. Headers, metadata, length and a write
// condition are optional. Returns the ETag"
// Docs: modules/storage.mdx#writing-objects
// "`Body` may be any reader, including one that cannot seek or has unknown
// length"
func TestPutObjectStreamsReplacesReturnsETag(t *testing.T) {
	t.Parallel()
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()
			mustPut(t, b.p, "stream", "old body")
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
				t.Fatalf("body = %q, want %q (replaced)", got, "streamed body")
			}
		})
	}
}

// Docs: modules/storage.mdx#writing-objects
// "a nil `Body` writes an empty object."
func TestNilBodyWritesEmptyObject(t *testing.T) {
	t.Parallel()
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()
			if _, err := b.p.PutObject(t.Context(), &storage.PutObjectInput{BaseObjectInput: obj("empty")}); err != nil {
				t.Fatalf("PutObject(nil Body) error: %v", err)
			}
			head, err := b.p.HeadObject(t.Context(), headIn("empty"))
			if err != nil {
				t.Fatalf("HeadObject() error: %v", err)
			}
			if head.Size != 0 || readObject(t, b.p, "empty") != "" {
				t.Fatalf("object after nil Body has Size %d, want an empty object", head.Size)
			}
		})
	}
}

// Docs: modules/storage.mdx#writing-objects
// "`ContentLength` is optional. When it is set, the body must yield exactly
// that many bytes: a shorter or longer body, or a negative length, fails with
// `storage.ErrContentLengthMismatch` and stores nothing, leaving any object
// already under the key as it was."
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
				{"negative", "abc", -1},
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
					t.Fatalf("%s: existing body = %q, want %q", tc.name, got, "original")
				}
				_, err := b.p.HeadObject(t.Context(), headIn("new-"+tc.name))
				wantErr(t, tc.name+": HeadObject of new key", err, storage.ErrNotFound)
			}
			if _, err := b.p.PutObject(t.Context(), &storage.PutObjectInput{
				Body: strings.NewReader("abcde"), ContentLength: ptr(int64(5)), BaseObjectInput: obj("exact"),
			}); err != nil {
				t.Fatalf("PutObject with matching ContentLength error: %v", err)
			}
		})
	}
}

// Docs: modules/storage.mdx#writing-objects
// "The body is read to EOF, so one that blocks after exactly `ContentLength`
// bytes without ending blocks `PutObject`."
func TestBodyReadToEOF(t *testing.T) {
	t.Parallel()
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()
			pr, pw := io.Pipe()
			done := make(chan error, 1)
			go func() {
				_, err := b.p.PutObject(t.Context(), &storage.PutObjectInput{
					Body: pr, ContentLength: ptr(int64(4)), BaseObjectInput: obj("eof"),
				})
				done <- err
			}()
			if _, err := pw.Write([]byte("abcd")); err != nil {
				t.Fatalf("write body: %v", err)
			}
			select {
			case err := <-done:
				t.Fatalf("PutObject returned (%v) while the body was still open after ContentLength bytes, want it blocked", err)
			case <-time.After(500 * time.Millisecond):
			}
			_ = pw.Close()
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("PutObject after EOF error: %v", err)
				}
			case <-time.After(30 * time.Second):
				t.Fatalf("PutObject did not return after EOF")
			}
			if got := readObject(t, b.p, "eof"); got != "abcd" {
				t.Fatalf("body = %q, want abcd", got)
			}
		})
	}
}

// Docs: modules/storage.mdx#writing-objects
// "`PutObjectInput` embeds `ObjectHeaders`: the content type, `Cache-Control`,
// `Content-Disposition`, `Content-Encoding`, `Content-Language`, `Expires`, and
// user metadata. Every header a write sets reads back on the `ObjectInfo` from
// `HeadObject` and `GetObject`. The body is stored as given; a
// `Content-Encoding` does not encode or decode it."
func TestHeadersReadBackBodyAsGiven(t *testing.T) {
	t.Parallel()
	const expires = "Wed, 21 Oct 2026 07:28:00 GMT"
	// Not a valid gzip stream, so any decoding would fail or change it.
	body := "plain bytes, labelled gzip"
	in := storage.ObjectHeaders{
		ContentType:        ptr("text/csv"),
		CacheControl:       ptr("max-age=60"),
		ContentDisposition: ptr(`attachment; filename="r.csv"`),
		ContentEncoding:    ptr("gzip"),
		ContentLanguage:    ptr("en-GB"),
		Expires:            ptr(expires),
		Metadata:           map[string]string{"source": "api"},
	}
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()
			if _, err := b.p.PutObject(t.Context(), &storage.PutObjectInput{
				Body: strings.NewReader(body), ObjectHeaders: in, BaseObjectInput: obj("hdr"),
			}); err != nil {
				t.Fatalf("PutObject() error: %v", err)
			}
			head, err := b.p.HeadObject(t.Context(), headIn("hdr"))
			if err != nil {
				t.Fatalf("HeadObject() error: %v", err)
			}
			o, err := b.p.GetObject(t.Context(), getIn("hdr"))
			if err != nil {
				t.Fatalf("GetObject() error: %v", err)
			}
			got, _ := io.ReadAll(o.Body)
			_ = o.Body.Close()
			if string(got) != body {
				t.Errorf("GetObject body = %q, want the bytes as given %q", got, body)
			}
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

// Docs: modules/storage.mdx#writing-objects
// "`Expires` is text, so a stored value that is not a date survives the round
// trip. The s3 backend accepts only an IMF-fixdate such as
// `Wed, 21 Oct 2026 07:28:00 GMT` and fails anything else, including the
// obsolete RFC 850 and asctime forms and a date with the wrong weekday, with
// `storage.ErrInvalidHeader` before sending anything. The directory backend
// stores any text."
func TestExpires(t *testing.T) {
	t.Parallel()
	notIMF := []string{
		"tomorrow",
		"Wednesday, 21-Oct-26 07:28:00 GMT", // RFC 850
		"Wed Oct 21 07:28:00 2026",          // asctime
		"Thu, 21 Oct 2026 07:28:00 GMT",     // wrong weekday: 21 Oct 2026 is a Wednesday
		"2026-10-21T07:28:00Z",
	}
	t.Run("s3 accepts IMF-fixdate", func(t *testing.T) {
		t.Parallel()
		b := s3Backend(t)
		const imf = "Wed, 21 Oct 2026 07:28:00 GMT"
		if _, err := b.p.PutObject(t.Context(), &storage.PutObjectInput{
			Body: strings.NewReader("x"), ObjectHeaders: storage.ObjectHeaders{Expires: ptr(imf)}, BaseObjectInput: obj("e"),
		}); err != nil {
			t.Fatalf("PutObject(Expires %q) error = %v, want accepted", imf, err)
		}
	})
	t.Run("s3 rejects before sending", func(t *testing.T) {
		t.Parallel()
		f := newFakeS3(t, nil)
		p := f.provider(t, "us-east-1", "bkt")
		for _, v := range notIMF {
			_, err := p.PutObject(t.Context(), &storage.PutObjectInput{
				Body: strings.NewReader("x"), ObjectHeaders: storage.ObjectHeaders{Expires: ptr(v)}, BaseObjectInput: obj("bad"),
			})
			wantErr(t, "PutObject(Expires "+v+")", err, storage.ErrInvalidHeader)
		}
		if n := f.requests.Load(); n != 0 {
			t.Fatalf("rejected Expires values sent %d requests, want 0", n)
		}
	})
	t.Run("dir stores any text", func(t *testing.T) {
		t.Parallel()
		b := dirBackend(t)
		for _, v := range notIMF {
			if _, err := b.p.PutObject(t.Context(), &storage.PutObjectInput{
				Body: strings.NewReader("x"), ObjectHeaders: storage.ObjectHeaders{Expires: ptr(v)}, BaseObjectInput: obj("e"),
			}); err != nil {
				t.Fatalf("dir PutObject(Expires %q) error: %v", v, err)
			}
			info, err := b.p.HeadObject(t.Context(), headIn("e"))
			if err != nil {
				t.Fatalf("HeadObject() error: %v", err)
			}
			if info.Expires != v {
				t.Fatalf("dir Expires = %q, want the text %q", info.Expires, v)
			}
		}
	})
}

// Docs: modules/storage.mdx#writing-objects
// "Every object reports an `ETag`, unquoted. Compare ETags only for equality"
func TestETagUnquoted(t *testing.T) {
	t.Parallel()
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()
			put := mustPut(t, b.p, "et", "body")
			head, err := b.p.HeadObject(t.Context(), headIn("et"))
			if err != nil {
				t.Fatalf("HeadObject() error: %v", err)
			}
			o, err := b.p.GetObject(t.Context(), getIn("et"))
			if err != nil {
				t.Fatalf("GetObject() error: %v", err)
			}
			_ = o.Body.Close()
			list, err := b.p.ListObjects(t.Context(), &storage.ListObjectsInput{})
			if err != nil || len(list.Objects) != 1 {
				t.Fatalf("ListObjects() = %v, %v, want one object", list, err)
			}
			for where, e := range map[string]string{
				"PutObject": put.ETag, "HeadObject": head.ETag, "GetObject": o.ETag, "ListObjects": list.Objects[0].ETag,
			} {
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

// Docs: modules/storage.mdx#writing-objects
// "On s3 the `ObjectInfo` holds the ETag and the version S3 reports, and its
// other fields are empty."
func TestS3PutObjectReturn(t *testing.T) {
	t.Parallel()
	b := s3Backend(t)
	enableVersioning(t, b)
	info, err := b.p.PutObject(t.Context(), &storage.PutObjectInput{
		Body:            strings.NewReader("hello"),
		ObjectHeaders:   storage.ObjectHeaders{ContentType: ptr("text/plain"), Metadata: map[string]string{"a": "b"}},
		BaseObjectInput: obj("ret"),
	})
	if err != nil {
		t.Fatalf("PutObject() error: %v", err)
	}
	if info.ETag == "" || info.Version == "" {
		t.Fatalf("PutObject() = %+v, want the ETag and the version", info)
	}
	rest := *info
	rest.ETag, rest.Version = "", ""
	if !rest.LastModified.IsZero() || rest.Size != 0 || rest.ContentType != "" || len(rest.Metadata) != 0 ||
		rest.CacheControl != "" || rest.ContentDisposition != "" || rest.ContentEncoding != "" ||
		rest.ContentLanguage != "" || rest.Expires != "" {
		t.Fatalf("PutObject() other fields = %+v, want empty", rest)
	}
}

// Docs: modules/storage.mdx#writing-objects
// "The directory backend returns the object as `HeadObject` would read it: ETag
// (the hex MD5 of the body), size, modification time, content type, metadata
// and headers. With no content type, the directory backend sniffs one from the
// body with `http.DetectContentType`."
func TestDirPutObjectReturn(t *testing.T) {
	t.Parallel()
	b := dirBackend(t)
	body := "<html><body>hi</body></html>"
	info, err := b.p.PutObject(t.Context(), &storage.PutObjectInput{
		Body: strings.NewReader(body),
		ObjectHeaders: storage.ObjectHeaders{
			CacheControl: ptr("no-store"), Metadata: map[string]string{"a": "b"},
		},
		BaseObjectInput: obj("ret.html"),
	})
	if err != nil {
		t.Fatalf("PutObject() error: %v", err)
	}
	sum := md5.Sum([]byte(body))
	if info.ETag != hex.EncodeToString(sum[:]) {
		t.Errorf("ETag = %q, want the hex MD5 of the body %s", info.ETag, hex.EncodeToString(sum[:]))
	}
	if want := http.DetectContentType([]byte(body)); info.ContentType != want {
		t.Errorf("ContentType = %q, want the sniffed %q", info.ContentType, want)
	}
	head, err := b.p.HeadObject(t.Context(), headIn("ret.html"))
	if err != nil {
		t.Fatalf("HeadObject() error: %v", err)
	}
	if fmt.Sprintf("%+v", *info) != fmt.Sprintf("%+v", *head) {
		t.Fatalf("PutObject() = %+v, HeadObject() = %+v, want equal", *info, *head)
	}
	if info.Size != int64(len(body)) || info.LastModified.IsZero() || info.Metadata["a"] != "b" || info.CacheControl != "no-store" {
		t.Fatalf("PutObject() = %+v, want size, modification time, metadata and headers", *info)
	}
}

// Docs: modules/storage.mdx#operations
// "`GetObject(ctx, *GetObjectInput)` | `*Object` | Body plus `ObjectInfo`; the
// caller closes `Body`. A missing key is `ErrKeyNotFound`"
// Docs: modules/storage.mdx#reading-objects
// "`GetObject` returns an open `Body` that the caller must close. `HeadObject`
// returns the same `ObjectInfo` without the body."
func TestGetObjectAndHeadObject(t *testing.T) {
	t.Parallel()
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()
			if _, err := b.p.PutObject(t.Context(), &storage.PutObjectInput{
				Body:            strings.NewReader("hello"),
				ObjectHeaders:   storage.ObjectHeaders{ContentType: ptr("text/plain"), Metadata: map[string]string{"m": "v"}},
				BaseObjectInput: obj("g"),
			}); err != nil {
				t.Fatalf("PutObject() error: %v", err)
			}
			o, err := b.p.GetObject(t.Context(), getIn("g"))
			if err != nil {
				t.Fatalf("GetObject() error: %v", err)
			}
			body, _ := io.ReadAll(o.Body)
			if err := o.Body.Close(); err != nil {
				t.Fatalf("Body.Close() error: %v", err)
			}
			if string(body) != "hello" || o.Size != 5 || o.ContentType != "text/plain" || o.ETag == "" {
				t.Fatalf("GetObject() = body %q, info %+v", body, o.ObjectInfo)
			}
			head, err := b.p.HeadObject(t.Context(), headIn("g"))
			if err != nil {
				t.Fatalf("HeadObject() error: %v", err)
			}
			h, g := *head, o.ObjectInfo
			if h.ETag != g.ETag || h.Size != g.Size || h.ContentType != g.ContentType ||
				!h.LastModified.Equal(g.LastModified) || h.Metadata["m"] != g.Metadata["m"] || h.Version != g.Version {
				t.Fatalf("HeadObject() = %+v, GetObject info = %+v, want the same ObjectInfo", h, g)
			}
			_, err = b.p.GetObject(t.Context(), getIn("missing"))
			wantErr(t, "GetObject(missing)", err, storage.ErrKeyNotFound)
		})
	}
}

// Docs: modules/storage.mdx#operations
// "`HeadObject(ctx, *HeadObjectInput)` | `*ObjectInfo` | Size, content type,
// modification time, ETag, version, metadata and stored headers. A missing
// object is `ErrNotFound`"
func TestHeadObjectFields(t *testing.T) {
	t.Parallel()
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()
			if _, err := b.p.PutObject(t.Context(), &storage.PutObjectInput{
				Body: strings.NewReader("12345678"),
				ObjectHeaders: storage.ObjectHeaders{
					ContentType: ptr("application/x-test"), CacheControl: ptr("no-cache"),
					Metadata: map[string]string{"owner": "alice"},
				},
				BaseObjectInput: obj("h"),
			}); err != nil {
				t.Fatalf("PutObject() error: %v", err)
			}
			info, err := b.p.HeadObject(t.Context(), headIn("h"))
			if err != nil {
				t.Fatalf("HeadObject() error: %v", err)
			}
			if info.Size != 8 || info.ContentType != "application/x-test" || info.LastModified.IsZero() ||
				info.ETag == "" || info.Metadata["owner"] != "alice" || info.CacheControl != "no-cache" {
				t.Fatalf("HeadObject() = %+v, want Size, content type, LastModified, ETag, metadata, Cache-Control", info)
			}
			_, err = b.p.HeadObject(t.Context(), headIn("missing"))
			wantErr(t, "HeadObject(missing)", err, storage.ErrNotFound)
			if errors.Is(err, storage.ErrKeyNotFound) {
				t.Fatalf("HeadObject(missing) also matches ErrKeyNotFound: %v", err)
			}
		})
	}
	t.Run("s3 version", func(t *testing.T) {
		t.Parallel()
		b := s3Backend(t)
		enableVersioning(t, b)
		put := mustPut(t, b.p, "v", "one")
		info, err := b.p.HeadObject(t.Context(), headIn("v"))
		if err != nil {
			t.Fatalf("HeadObject() error: %v", err)
		}
		if info.Version == "" || info.Version != put.Version {
			t.Fatalf("HeadObject().Version = %q, PutObject().Version = %q, want equal and non-empty", info.Version, put.Version)
		}
	})
}

// Docs: modules/storage.mdx#operations
// "`DeleteObject(ctx, *DeleteObjectInput)` | `*ObjectInfo` | Deleting a missing
// key succeeds"
func TestDeleteObject(t *testing.T) {
	t.Parallel()
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()
			if _, err := b.p.DeleteObject(t.Context(), &storage.DeleteObjectInput{BaseObjectInput: obj("never-written")}); err != nil {
				t.Fatalf("DeleteObject(missing) error = %v, want nil", err)
			}
			mustPut(t, b.p, "gone", "x")
			if _, err := b.p.DeleteObject(t.Context(), &storage.DeleteObjectInput{BaseObjectInput: obj("gone")}); err != nil {
				t.Fatalf("DeleteObject() error: %v", err)
			}
			_, err := b.p.HeadObject(t.Context(), headIn("gone"))
			wantErr(t, "HeadObject(deleted)", err, storage.ErrNotFound)
		})
	}
}

// Docs: modules/storage.mdx#reading-objects
// "A `Range` such as `bytes=0-1023` reads part of the object on both backends,
// and `Size` is then the length of that part. A range that cannot be satisfied,
// because its first byte is at or past the end or it is `bytes=-0`, is
// `storage.ErrInvalidRange`."
func TestRange(t *testing.T) {
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
				t.Fatalf("GetObject(bytes=0-1023) = %d bytes, Size %d, want the first 1024 and Size 1024", len(got), o.Size)
			}
			for _, rng := range []string{"bytes=3000-3010", "bytes=3000-", "bytes=5000-6000", "bytes=-0"} {
				if _, _, err := getRange(t, b.p, "r", rng); !errors.Is(err, storage.ErrInvalidRange) {
					t.Errorf("GetObject(Range %q) error = %v, want ErrInvalidRange", rng, err)
				}
			}
			// The last byte is satisfiable: "at or past the end" starts at 3000.
			if got, _, err := getRange(t, b.p, "r", "bytes=2999-"); err != nil || got != "9" {
				t.Errorf("GetObject(bytes=2999-) = %q, %v, want the last byte", got, err)
			}
		})
	}
}

// Docs: modules/storage.mdx#reading-objects
// "The directory backend applies RFC 9110 single-range semantics (one
// `bytes=first-last`, `bytes=first-` or `bytes=-suffix` range, clamped to the
// object) and ignores any other header, returning the whole object."
func TestDirRangeSemantics(t *testing.T) {
	t.Parallel()
	const full = "0123456789"
	d := dirBackend(t)
	mustPut(t, d.p, "r", full)
	for rng, want := range map[string]string{
		"bytes=2-4":   "234",
		"bytes=7-":    "789",
		"bytes=-3":    "789",
		"bytes=5-100": "56789",      // clamped
		"bytes=-100":  "0123456789", // clamped
	} {
		got, o, err := getRange(t, d.p, "r", rng)
		if err != nil || got != want || o.Size != int64(len(want)) {
			t.Errorf("dir GetObject(Range %q) = %q, %v, want %q with Size %d", rng, got, err, want, len(want))
		}
	}
	for _, rng := range []string{"bytes=5-2", "items=0-1", "bytes=0-1,4-5", "bytes=abc", "0-1", "bytes = 0-1"} {
		got, o, err := getRange(t, d.p, "r", rng)
		if err != nil || got != full || o.Size != int64(len(full)) {
			t.Errorf("dir GetObject(Range %q) = %q, %v, want the header ignored and the whole object", rng, got, err)
		}
	}
}

// Docs: modules/storage.mdx#reading-objects
// "The s3 backend sends the header as given, so the result is S3's, which
// agrees on valid single ranges but accepts some malformed ones; S3's `416`
// comes back as `storage.ErrInvalidRange`."
func TestS3RangeSentAsGiven(t *testing.T) {
	t.Parallel()
	t.Run("header as given", func(t *testing.T) {
		t.Parallel()
		f := newFakeS3(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", "1")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("x"))
		})
		p := f.provider(t, "us-east-1", "b-range")
		given := []string{"bytes=0-1", "bytes=5-2", "items=0-1", "bytes=0-1,4-5", "bytes=abc"}
		for _, rng := range given {
			if _, _, err := getRange(t, p, "k", rng); err != nil {
				t.Fatalf("GetObject(Range %q) error: %v", rng, err)
			}
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		var sent []string
		for _, h := range f.headers {
			sent = append(sent, h.Get("Range"))
		}
		if fmt.Sprint(sent) != fmt.Sprint(given) {
			t.Fatalf("Range headers sent = %q, want exactly %q", sent, given)
		}
	})
	t.Run("416 is ErrInvalidRange", func(t *testing.T) {
		t.Parallel()
		f := newFakeS3(t, func(w http.ResponseWriter, _ *http.Request) {
			s3Error(w, http.StatusRequestedRangeNotSatisfiable, "InvalidRange")
		})
		_, _, err := getRange(t, f.provider(t, "us-east-1", "b-range"), "k", "bytes=20-30")
		wantErr(t, "s3 GetObject answered 416", err, storage.ErrInvalidRange)
	})
	t.Run("agrees on valid single ranges", func(t *testing.T) {
		t.Parallel()
		b := s3Backend(t)
		mustPut(t, b.p, "r", "0123456789")
		for rng, want := range map[string]string{"bytes=2-4": "234", "bytes=7-": "789", "bytes=-3": "789", "bytes=5-100": "56789"} {
			got, o, err := getRange(t, b.p, "r", rng)
			if err != nil || got != want || o.Size != int64(len(want)) {
				t.Errorf("s3 GetObject(Range %q) = %q, %v, want %q", rng, got, err, want)
			}
		}
	})
}

// Docs: modules/storage.mdx#reading-objects
// "The directory backend's `Body` also implements `io.ReadSeeker`, for whole and
// ranged reads."
func TestDirBodySeeks(t *testing.T) {
	t.Parallel()
	d := dirBackend(t)
	mustPut(t, d.p, "s", "0123456789")
	for rng, want := range map[string]string{"": "0123456789", "bytes=2-6": "23456"} {
		in := getIn("s")
		if rng != "" {
			in.Range = ptr(rng)
		}
		o, err := d.p.GetObject(t.Context(), in)
		if err != nil {
			t.Fatalf("GetObject(%q) error: %v", rng, err)
		}
		rs, ok := o.Body.(io.ReadSeeker)
		if !ok {
			_ = o.Body.Close()
			t.Fatalf("GetObject(%q) Body is %T, want an io.ReadSeeker", rng, o.Body)
		}
		first, _ := io.ReadAll(rs)
		if _, err := rs.Seek(1, io.SeekStart); err != nil {
			t.Fatalf("Seek() error: %v", err)
		}
		again, _ := io.ReadAll(rs)
		_ = o.Body.Close()
		if string(first) != want || string(again) != want[1:] {
			t.Errorf("Range %q: read %q, after Seek(1) %q, want %q and %q", rng, first, again, want, want[1:])
		}
	}
}

// Docs: modules/storage.mdx#conditional-writes
// "`IfNoneMatch: "*"` writes only when no object exists under the key."
// Docs: modules/storage.mdx#conditional-writes
// "A condition that does not hold fails with `storage.ErrPreconditionFailed`,
// and nothing is written."
func TestIfNoneMatch(t *testing.T) {
	t.Parallel()
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()
			put := func(body string) error {
				_, err := b.p.PutObject(t.Context(), &storage.PutObjectInput{
					Body: strings.NewReader(body), WriteCondition: storage.WriteCondition{IfNoneMatch: ptr("*")},
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

// Docs: modules/storage.mdx#conditional-writes
// "`IfMatch` writes only over the object with that ETag, quoted or not. A
// missing key is `storage.ErrKeyNotFound`."
func TestIfMatch(t *testing.T) {
	t.Parallel()
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()
			put := func(key, etag, body string) error {
				_, err := b.p.PutObject(t.Context(), &storage.PutObjectInput{
					Body: strings.NewReader(body), WriteCondition: storage.WriteCondition{IfMatch: ptr(etag)},
					BaseObjectInput: obj(key),
				})
				return err
			}
			v1 := mustPut(t, b.p, "im", "v1")
			if err := put("im", v1.ETag, "v2"); err != nil {
				t.Fatalf("PutObject(IfMatch ETag) error = %v, want nil", err)
			}
			wantErr(t, "PutObject(IfMatch stale ETag)", put("im", v1.ETag, "v3"), storage.ErrPreconditionFailed)
			if got := readObject(t, b.p, "im"); got != "v2" {
				t.Fatalf("body = %q, want %q (stale write not applied)", got, "v2")
			}
			head, err := b.p.HeadObject(t.Context(), headIn("im"))
			if err != nil {
				t.Fatalf("HeadObject() error: %v", err)
			}
			if err := put("im", `"`+head.ETag+`"`, "v4"); err != nil {
				t.Fatalf("PutObject(IfMatch quoted ETag) error = %v, want nil", err)
			}
			if got := readObject(t, b.p, "im"); got != "v4" {
				t.Fatalf("body after quoted IfMatch = %q, want v4", got)
			}
			wantErr(t, "PutObject(IfMatch) of a missing key", put("missing", head.ETag, "x"), storage.ErrKeyNotFound)
			_, err = b.p.HeadObject(t.Context(), headIn("missing"))
			wantErr(t, "HeadObject after IfMatch of a missing key", err, storage.ErrNotFound)
		})
	}
}

// Docs: modules/storage.mdx#conditional-writes
// "`PutObjectInput` embeds a `WriteCondition`, at most one field of which may be
// set"
// Docs: modules/storage.mdx#conditional-writes
// "A condition that cannot be evaluated (an `IfNoneMatch` other than `*`, an
// empty `IfMatch` or one of `*`, or both set) fails with
// `storage.ErrInvalidCondition` before anything is sent."
func TestInvalidCondition(t *testing.T) {
	t.Parallel()
	conds := map[string]storage.WriteCondition{
		"IfNoneMatch etag":  {IfNoneMatch: ptr("abc")},
		"IfNoneMatch empty": {IfNoneMatch: ptr("")},
		"IfMatch empty":     {IfMatch: ptr("")},
		"IfMatch *":         {IfMatch: ptr("*")},
		"both":              {IfMatch: ptr("abc"), IfNoneMatch: ptr("*")},
	}
	f := newFakeS3(t, nil)
	d := dirBackend(t)
	for name, p := range map[string]storage.Provider{"s3": f.provider(t, "us-east-1", "bkt"), "dir": d.p} {
		for cn, c := range conds {
			_, err := p.PutObject(t.Context(), &storage.PutObjectInput{
				Body: strings.NewReader("x"), WriteCondition: c, BaseObjectInput: obj("ic"),
			})
			wantErr(t, name+" PutObject("+cn+")", err, storage.ErrInvalidCondition)
		}
	}
	pre := f.presigner(t, "us-east-1", "bkt")
	for cn, c := range conds {
		_, err := pre.PresignUploadURL(t.Context(), &storage.PresignUploadURLInput{PutObjectInput: storage.PutObjectInput{
			WriteCondition: c, BaseObjectInput: obj("ic"),
		}})
		wantErr(t, "PresignUploadURL("+cn+")", err, storage.ErrInvalidCondition)
	}
	if n := f.requests.Load(); n != 0 {
		t.Fatalf("invalid conditions sent %d requests, want 0", n)
	}
	_, err := d.p.HeadObject(t.Context(), headIn("ic"))
	wantErr(t, "dir HeadObject after invalid conditions", err, storage.ErrNotFound)
}

// Docs: modules/storage.mdx#conditional-writes
// "On s3 the condition is sent as `If-Match` or `If-None-Match` and S3's `412` is
// `storage.ErrPreconditionFailed`; a `409 ConditionalRequestConflict`, which S3
// answers when a concurrent write conflicts, carries no sentinel."
func TestS3ConditionOnTheWire(t *testing.T) {
	t.Parallel()
	t.Run("headers", func(t *testing.T) {
		t.Parallel()
		f := newFakeS3(t, nil)
		p := f.provider(t, "us-east-1", "bkt")
		_, _ = p.PutObject(t.Context(), &storage.PutObjectInput{
			Body: strings.NewReader("x"), WriteCondition: storage.WriteCondition{IfMatch: ptr("abc")}, BaseObjectInput: obj("a"),
		})
		_, _ = p.PutObject(t.Context(), &storage.PutObjectInput{
			Body: strings.NewReader("x"), WriteCondition: storage.WriteCondition{IfNoneMatch: ptr("*")}, BaseObjectInput: obj("b"),
		})
		f.mu.Lock()
		defer f.mu.Unlock()
		var ifMatch, ifNoneMatch bool
		for i, h := range f.headers {
			if strings.HasPrefix(f.seenPaths[i], "/bkt/a") && strings.Trim(h.Get("If-Match"), `"`) == "abc" {
				ifMatch = true
			}
			if strings.HasPrefix(f.seenPaths[i], "/bkt/b") && h.Get("If-None-Match") == "*" {
				ifNoneMatch = true
			}
		}
		if !ifMatch || !ifNoneMatch {
			t.Fatalf("If-Match sent = %v, If-None-Match sent = %v, want both", ifMatch, ifNoneMatch)
		}
	})
	t.Run("412", func(t *testing.T) {
		t.Parallel()
		f := newFakeS3(t, func(w http.ResponseWriter, _ *http.Request) {
			s3Error(w, http.StatusPreconditionFailed, "PreconditionFailed")
		})
		_, err := f.provider(t, "us-east-1", "bkt").PutObject(t.Context(), &storage.PutObjectInput{
			Body: strings.NewReader("x"), WriteCondition: storage.WriteCondition{IfNoneMatch: ptr("*")}, BaseObjectInput: obj("a"),
		})
		wantErr(t, "PutObject answered 412", err, storage.ErrPreconditionFailed)
	})
	t.Run("409", func(t *testing.T) {
		t.Parallel()
		f := newFakeS3(t, func(w http.ResponseWriter, _ *http.Request) {
			s3Error(w, http.StatusConflict, "ConditionalRequestConflict")
		})
		_, err := f.provider(t, "us-east-1", "bkt").PutObject(t.Context(), &storage.PutObjectInput{
			Body: strings.NewReader("x"), WriteCondition: storage.WriteCondition{IfNoneMatch: ptr("*")}, BaseObjectInput: obj("a"),
		})
		if err == nil {
			t.Fatalf("PutObject answered 409 error = nil, want an error")
		}
		for _, s := range storageSentinels {
			if errors.Is(err, s) {
				t.Fatalf("PutObject answered 409 error = %v, matches %v, want no sentinel", err, s)
			}
		}
	})
	t.Run("localstack", func(t *testing.T) {
		t.Parallel()
		b := s3Backend(t)
		mustPut(t, b.p, "k", "x")
		_, err := b.p.PutObject(t.Context(), &storage.PutObjectInput{
			Body: strings.NewReader("y"), WriteCondition: storage.WriteCondition{IfNoneMatch: ptr("*")}, BaseObjectInput: obj("k"),
		})
		wantErr(t, "LocalStack PutObject(IfNoneMatch *) over an object", err, storage.ErrPreconditionFailed)
	})
}

// Docs: modules/storage.mdx#writing-objects
// "On s3, `PutObject` uploads through the SDK's transfer manager, which switches
// to a multipart upload for large bodies and aborts a failed one. If that abort
// fails, the incomplete upload stays stored and billed until a bucket lifecycle
// rule removes it, and the abort's error is not reported."
// Billing and lifecycle rules are S3's and not tested. A part upload is failed
// with a 400 so the SDK does not retry it.
func TestS3MultipartAbort(t *testing.T) {
	t.Parallel()
	for _, abortFails := range []bool{false, true} {
		t.Run(fmt.Sprintf("abort fails %v", abortFails), func(t *testing.T) {
			t.Parallel()
			f := newFakeS3(t, func(w http.ResponseWriter, r *http.Request) {
				q := r.URL.Query()
				switch {
				case r.Method == http.MethodPost && q.Has("uploads"):
					w.Header().Set("Content-Type", "application/xml")
					_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><InitiateMultipartUploadResult>` +
						`<Bucket>bkt</Bucket><Key>big</Key><UploadId>UP1</UploadId></InitiateMultipartUploadResult>`))
				case r.Method == http.MethodPut && q.Has("partNumber"):
					s3Error(w, http.StatusBadRequest, "PartRejectedForTest")
				case r.Method == http.MethodDelete && q.Get("uploadId") == "UP1":
					if abortFails {
						s3Error(w, http.StatusBadRequest, "AbortRejectedForTest")
						return
					}
					w.WriteHeader(http.StatusNoContent)
				default:
					w.WriteHeader(http.StatusOK)
				}
			})
			p := f.provider(t, "us-east-1", "bkt")
			big := bytes.Repeat([]byte("x"), 20<<20) // well over one part
			_, err := p.PutObject(t.Context(), &storage.PutObjectInput{
				Body: io.MultiReader(bytes.NewReader(big)), BaseObjectInput: obj("big"),
			})
			if err == nil {
				t.Fatalf("PutObject with a failing part error = nil, want an error")
			}
			f.mu.Lock()
			var initiated, aborted bool
			for i, m := range f.methods {
				if m == http.MethodPost && strings.Contains(f.seenPaths[i], "uploads") {
					initiated = true
				}
				if m == http.MethodDelete && strings.Contains(f.seenPaths[i], "uploadId=UP1") {
					aborted = true
				}
			}
			f.mu.Unlock()
			if !initiated || !aborted {
				t.Fatalf("multipart initiated = %v, aborted = %v, want both", initiated, aborted)
			}
			if abortFails && strings.Contains(err.Error(), "AbortRejectedForTest") {
				t.Fatalf("PutObject error = %v, reports the abort's error, want it not reported", err)
			}
		})
	}
}
