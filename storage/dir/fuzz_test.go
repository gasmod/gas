package dir

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/gasmod/gas/storage"
)

// FuzzProviderInput drives PutObject, HeadObject, GetObject and DeleteObject
// with an arbitrary key, bucket name, metadata pair, content type, body and
// declared ContentLength, all through the public API. No input may panic. A
// key or bucket name that fails validation must be reported as
// storage.ErrInvalidKey or storage.ErrInvalidBucket, a ContentLength that
// does not match the body as storage.ErrContentLengthMismatch, and an object
// that was stored must read back with the body it was given.
func FuzzProviderInput(f *testing.F) {
	f.Add("k", testBucket, "mk", "mv", "text/plain", []byte("data"), false, int64(0))
	f.Add("../x", "B!", "\xff", "\x00", "", []byte{}, true, int64(0))
	f.Add("a/./b", testBucket, "", "", "\r\n", []byte("<html>"), true, int64(6))
	f.Add("/abs", testBucket, "Trace-ID", "\xc3\x28", "image/png", []byte{0, 1, 2}, true, int64(2))
	f.Add("k", testBucket, "", "", "", []byte("data"), true, int64(5))
	f.Add("k", testBucket, "", "", "", []byte("data"), true, int64(-1))

	f.Fuzz(func(t *testing.T, key, bucket, metaKey, metaValue, contentType string, body []byte,
		setLength bool, length int64,
	) {
		p, _ := newTestProvider(t)
		ctx := context.Background()
		in := storage.BaseObjectInput{Bucket: &bucket, Key: key}

		var contentLength *int64
		if setLength {
			contentLength = &length
		}
		_, putErr := p.PutObject(ctx, &storage.PutObjectInput{
			BaseObjectInput: in,
			Body:            bytes.NewReader(body),
			ContentLength:   contentLength,
			ContentType:     &contentType,
			Metadata:        map[string]string{metaKey: metaValue},
		})
		if putErr == nil && setLength && length != int64(len(body)) {
			t.Errorf(
				"PutObject() with ContentLength %d for %d bytes succeeded, want ErrContentLengthMismatch",
				length,
				len(body),
			)
		}
		_, headErr := p.HeadObject(ctx, &storage.HeadObjectInput{BaseObjectInput: in})
		obj, getErr := p.GetObject(ctx, &storage.GetObjectInput{
			HeadObjectInput: storage.HeadObjectInput{BaseObjectInput: in},
		})
		if getErr == nil {
			got, readErr := io.ReadAll(obj.Body)
			if closeErr := obj.Body.Close(); closeErr != nil {
				t.Errorf("close body: %v", closeErr)
			}
			if readErr != nil {
				t.Fatalf("read body: %v", readErr)
			}
			if putErr == nil && !bytes.Equal(got, body) {
				t.Errorf("GetObject() body = %q, want %q", got, body)
			}
		}
		_, delErr := p.DeleteObject(ctx, &storage.DeleteObjectInput{BaseObjectInput: in})

		for op, err := range map[string]error{
			"PutObject":    putErr,
			"HeadObject":   headErr,
			"GetObject":    getErr,
			"DeleteObject": delErr,
		} {
			if err != nil && !fuzzErrAllowed(op, err, putErr) {
				t.Errorf(
					"%s() error = %v, want nil or an invalid-input, bucket-not-found, "+
						"content-length or (after a rejected put) not-found sentinel",
					op,
					err,
				)
			}
		}
		// Every iteration uses a fresh provider, so the key is new: a put
		// rejected for its length must leave nothing to read.
		if errors.Is(putErr, storage.ErrContentLengthMismatch) &&
			!errors.Is(headErr, storage.ErrNotFound) {
			t.Errorf("HeadObject() after a rejected put error = %v, want ErrNotFound", headErr)
		}
		if putErr == nil && (headErr != nil || getErr != nil) {
			t.Errorf("stored object not readable: head %v, get %v", headErr, getErr)
		}
	})
}

// fuzzErrAllowed reports whether err is an error FuzzProviderInput accepts
// from op: an invalid key or bucket, a missing bucket, a content-length
// mismatch from PutObject, or, when the put was rejected, a not-found from
// the reads that follow it.
func fuzzErrAllowed(op string, err, putErr error) bool {
	switch {
	case errors.Is(err, storage.ErrInvalidKey),
		errors.Is(err, storage.ErrInvalidBucket),
		errors.Is(err, storage.ErrBucketNotFound):
		return true
	case op == "PutObject":
		return errors.Is(err, storage.ErrContentLengthMismatch)
	default:
		return putErr != nil &&
			(errors.Is(err, storage.ErrKeyNotFound) || errors.Is(err, storage.ErrNotFound))
	}
}

// FuzzMetadataOnDisk overwrites an object's metadata file with arbitrary
// bytes, then reads, overwrites and deletes the object. Data read back from
// disk is untrusted: no content may panic, a DataFile naming a path outside
// the object's own data files must not be followed, and a PutObject over any
// metadata must replace it, as the package documents.
func FuzzMetadataOnDisk(f *testing.F) {
	f.Add([]byte(`{}`))
	f.Add([]byte(`null`))
	f.Add([]byte(`{"Key":"k","DataFile":"../../../../etc/passwd","Metadata":{"a":"b"}}`))
	f.Add([]byte(`{"Key":"k","DataFile":"` + sha("k") + `_00000000-0000-0000-0000-000000000000"}`))
	f.Add([]byte(`{"Key":"other","DataFile":"` + sha("other") + `_x","ContentType":5}`))
	f.Add([]byte("\x00\xff{"))

	f.Fuzz(func(t *testing.T, raw []byte) {
		p, dir := newTestProvider(t)
		put(t, p, "k", []byte("orig"))
		writeFile(t, metaFile(dir, "k"), raw)

		ctx := context.Background()
		in := base("k")
		_, _ = p.HeadObject(ctx, &storage.HeadObjectInput{BaseObjectInput: in})
		obj, err := p.GetObject(ctx, &storage.GetObjectInput{
			HeadObjectInput: storage.HeadObjectInput{BaseObjectInput: in},
		})
		if err == nil {
			got, readErr := io.ReadAll(obj.Body)
			if closeErr := obj.Body.Close(); closeErr != nil {
				t.Errorf("close body: %v", closeErr)
			}
			if readErr == nil && !bytes.Equal(got, []byte("orig")) {
				t.Errorf(
					"GetObject() over rewritten metadata body = %q, want %q or an error",
					got,
					"orig",
				)
			}
		}

		if _, err := p.PutObject(ctx, &storage.PutObjectInput{
			BaseObjectInput: in,
			Body:            bytes.NewReader([]byte("new")),
		}); err != nil {
			t.Fatalf(
				"PutObject() over rewritten metadata error: %v, want the metadata replaced",
				err,
			)
		}
		if _, got := get(t, p, "k"); !bytes.Equal(got, []byte("new")) {
			t.Errorf("GetObject() after replacing metadata = %q, want %q", got, "new")
		}
		if _, err := del(p, "k"); err != nil {
			t.Errorf("DeleteObject() error: %v", err)
		}
	})
}
