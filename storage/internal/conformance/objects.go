//nolint:mnd,wrapcheck // test rules: the errors under test are returned as they are.
package conformance

import (
	"context"
	"crypto/md5" //nolint:gosec // G501: the ETag of the memory and dir backends, and of S3, is an MD5.
	"encoding/hex"
	"errors"
	"io"
	"maps"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/storage"
)

// objectRules returns the rules of the objects.
func objectRules() map[string]func(*testing.T, Subject) {
	return map[string]func(*testing.T, Subject){
		"RoundTrip":           roundTrip,
		"ETag":                etag,
		"DefaultContentType":  defaultContentType,
		"EmptyObject":         emptyObject,
		"UnknownLength":       unknownLength,
		"Overwrite":           overwrite,
		"ContentLength":       contentLength,
		"BodyError":           bodyError,
		"NotFound":            notFound,
		"GetMissingBucket":    getMissingBucket,
		"HeadMissingBucket":   headMissingBucket,
		"WriteMissingBucket":  writeMissingBucket,
		"DeleteObject":        deleteObject,
		"CallerUnchanged":     callerUnchanged,
		"BodyAfterClose":      bodyAfterClose,
		"ConfiguredBucket":    configuredBucket,
		"NoBucket":            noBucket,
		"Conditions":          conditions,
		"ConditionRace":       conditionRace,
		"Range":               rangeRule,
		"RangeNotSatisfiable": rangeNotSatisfiable,
		"Unsupported":         unsupported,
		"ErrorsHoldNoData":    errorsHoldNoData,
	}
}

// fullInput returns a PutObjectInput of body under key in bucket that sets
// every header field and some metadata.
func fullInput(bucket, key, body string) *storage.PutObjectInput {
	in := putIn(bucket, key, body)

	in.ObjectHeaders = storage.ObjectHeaders{
		Metadata:           map[string]string{"Author": "Ann Lee", "x-Count": "3"},
		CacheControl:       new("max-age=60"),
		ContentDisposition: new(`attachment; filename="tom.txt"`),
		ContentEncoding:    new("identity"),
		ContentLanguage:    new("en-GB"),
		ContentType:        new("text/plain; charset=utf-8"),
		Expires:            new("Wed, 21 Oct 2015 07:28:00 GMT"),
	}

	return in
}

// fullValues are the header fields fullInput sets.
var fullValues = storage.HeaderValues{
	CacheControl:       "max-age=60",
	ContentDisposition: `attachment; filename="tom.txt"`,
	ContentEncoding:    "identity",
	ContentLanguage:    "en-GB",
	ContentType:        "text/plain; charset=utf-8",
	Expires:            "Wed, 21 Oct 2015 07:28:00 GMT",
}

// md5Hex returns the hex MD5 of s.
func md5Hex(s string) string {
	sum := md5.Sum([]byte(s)) //nolint:gosec // G401: an ETag.

	return hex.EncodeToString(sum[:])
}

// requireModified fails t unless at is in UTC, to the second, and within
// a second around from and to.
func requireModified(t *testing.T, at, from, to time.Time) {
	t.Helper()

	require.Equal(t, time.UTC, at.Location())
	require.Equal(t, at, at.Truncate(time.Second))
	require.False(t, at.Before(from.Add(-time.Second)), "%v is before %v", at, from)
	require.False(t, at.After(to.Add(time.Second)), "%v is after %v", at, to)
}

// An object reads back with its contents, header fields and metadata,
// names in lower case, from HeadObject and GetObject, with the ETag and
// the size PutObject returned.
func roundTrip(t *testing.T, s Subject) {
	t.Helper()

	st, bucket := setup(t, s)
	before := time.Now()

	out, err := st.PutObject(t.Context(), fullInput(bucket, "cats/tom.txt", "Tom"))
	require.NoError(t, err)

	after := time.Now()

	assert.Equal(t, int64(3), out.Size)
	assert.NotEmpty(t, out.ETag)

	info, err := st.HeadObject(t.Context(), headIn(bucket, "cats/tom.txt"))
	require.NoError(t, err)

	obj, body := get(t, st, getIn(bucket, "cats/tom.txt"))

	for _, i := range []*storage.ObjectInfo{info, &obj.ObjectInfo} {
		assert.Equal(t, out.ETag, i.ETag)
		assert.Equal(t, int64(3), i.Size)
		assert.Equal(t, fullValues, i.HeaderValues)
		assert.Equal(t, map[string]string{"author": "Ann Lee", "x-count": "3"}, i.Metadata)
		requireModified(t, i.LastModified, before, after)
	}

	assert.Equal(t, "Tom", body)
}

// The ETag of contents written in one part is their hex MD5, without
// quotes: the same contents have the same ETag.
func etag(t *testing.T, s Subject) {
	t.Helper()

	st, bucket := setup(t, s)

	a := put(t, st, bucket, "a", "same")
	b := put(t, st, bucket, "b", "same")
	c := put(t, st, bucket, "c", "other")

	assert.Equal(t, md5Hex("same"), a.ETag)
	assert.Equal(t, a.ETag, b.ETag)
	assert.Equal(t, md5Hex("other"), c.ETag)
}

// An object written without a ContentType reads back with
// DefaultContentType, and without the other header fields or metadata.
func defaultContentType(t *testing.T, s Subject) {
	t.Helper()

	st, bucket := setup(t, s)

	put(t, st, bucket, "k", "v")

	info, err := st.HeadObject(t.Context(), headIn(bucket, "k"))
	require.NoError(t, err)
	assert.Equal(
		t,
		storage.HeaderValues{ContentType: storage.DefaultContentType},
		info.HeaderValues,
	)
	assert.Empty(t, info.Metadata)
}

// A nil Body, and an empty one with a ContentLength of 0, write an empty
// object.
func emptyObject(t *testing.T, s Subject) {
	t.Helper()

	st, bucket := setup(t, s)

	out, err := st.PutObject(
		t.Context(),
		&storage.PutObjectInput{BaseObjectInput: ref(bucket, "nil")},
	)
	require.NoError(t, err)
	assert.Equal(t, int64(0), out.Size)
	assert.Equal(t, md5Hex(""), out.ETag)

	put(t, st, bucket, "zero", "")

	for _, key := range []string{"nil", "zero"} {
		obj, body := get(t, st, getIn(bucket, key))
		assert.Empty(t, body)
		assert.Equal(t, int64(0), obj.Size)
	}
}

// A body whose length is not known, read a byte at a time, is stored
// whole.
func unknownLength(t *testing.T, s Subject) {
	t.Helper()

	st, bucket := setup(t, s)
	want := strings.Repeat("0123456789abcdef", 1<<12)
	in := putIn(bucket, "k", "")

	in.Body, in.ContentLength = iotest.OneByteReader(strings.NewReader(want)), nil

	out, err := st.PutObject(t.Context(), in)
	require.NoError(t, err)
	assert.Equal(t, int64(len(want)), out.Size)
	requireBody(t, st, bucket, "k", want)
}

// A write replaces the object under its key, its header fields and
// metadata included.
func overwrite(t *testing.T, s Subject) {
	t.Helper()

	st, bucket := setup(t, s)

	_, err := st.PutObject(t.Context(), fullInput(bucket, "k", "first"))
	require.NoError(t, err)
	put(t, st, bucket, "k", "second")

	obj, body := get(t, st, getIn(bucket, "k"))
	assert.Equal(t, "second", body)
	assert.Equal(t, storage.DefaultContentType, obj.ContentType)
	assert.Empty(t, obj.Metadata)
}

// A body shorter or longer than its ContentLength is
// ErrContentLengthMismatch, and leaves the object under the key as it
// was; a negative ContentLength is ErrInvalidArgument.
func contentLength(t *testing.T, s Subject) {
	t.Helper()

	st, bucket := setup(t, s)

	put(t, st, bucket, "k", "kept")

	for body, want := range map[string]string{
		"abc":    "content length mismatch: body has 3 bytes, ContentLength is 5",
		"abcdef": "content length mismatch: body is longer than ContentLength 5",
	} {
		in := putIn(bucket, "k", body)

		in.ContentLength = new(int64(5))

		_, err := st.PutObject(t.Context(), in)
		requireErr(t, err, storage.ErrContentLengthMismatch, errText(s, opPutObject, want))
		requireBody(t, st, bucket, "k", "kept")
	}

	in := putIn(bucket, "k", "")

	in.ContentLength = new(int64(-1))

	_, err := st.PutObject(t.Context(), in)
	requireErr(t, err, storage.ErrInvalidArgument,
		errText(s, opPutObject, "invalid argument: ContentLength is negative"))
}

// errBody is the error of a body that fails.
var errBody = errors.New("disk gone")

// An error of the body's Read fails the write, wrapped, and stores
// nothing.
func bodyError(t *testing.T, s Subject) {
	t.Helper()

	st, bucket := setup(t, s)

	for _, n := range []*int64{nil, new(int64(10))} {
		in := putIn(bucket, "k", "")

		in.Body = io.MultiReader(strings.NewReader("abc"), iotest.ErrReader(errBody))
		in.ContentLength = n

		_, err := st.PutObject(t.Context(), in)
		requireErr(t, err, errBody, errText(s, opPutObject, "read body: disk gone"))
		requireAbsent(t, st, bucket, "k")
	}
}

// HeadObject and GetObject of a key that holds no object are
// ErrObjectNotFound, not ErrBucketNotFound.
func notFound(t *testing.T, s Subject) {
	t.Helper()

	st, bucket := setup(t, s)

	_, err := st.HeadObject(t.Context(), headIn(bucket, "missing"))
	requireErr(t, err, storage.ErrObjectNotFound, errText(s, opHeadObject, "object not found"))
	require.NotErrorIs(t, err, storage.ErrBucketNotFound)

	_, err = st.GetObject(t.Context(), getIn(bucket, "missing"))
	requireErr(t, err, storage.ErrObjectNotFound, errText(s, opGetObject, "object not found"))
	require.NotErrorIs(t, err, storage.ErrBucketNotFound)
}

// GetObject of an object in a missing bucket is ErrObjectNotFound and
// ErrBucketNotFound.
func getMissingBucket(t *testing.T, s Subject) {
	t.Helper()

	st := open(t, s, "")

	_, err := st.GetObject(t.Context(), getIn(newBucketName(), "k"))
	require.ErrorIs(t, err, storage.ErrObjectNotFound)
	requireErr(t, err, storage.ErrBucketNotFound,
		errText(s, opGetObject, "object not found: bucket not found"))
}

// HeadObject of an object in a missing bucket is ErrObjectNotFound and
// ErrBucketNotFound, on a backend that can tell.
func headMissingBucket(t *testing.T, s Subject) {
	t.Helper()

	st := open(t, s, "")

	_, err := st.HeadObject(t.Context(), headIn(newBucketName(), "k"))
	require.ErrorIs(t, err, storage.ErrObjectNotFound)
	requireErr(t, err, storage.ErrBucketNotFound,
		errText(s, opHeadObject, "object not found: bucket not found"))
}

// PutObject, DeleteObject and ListObjects of a missing bucket are
// ErrBucketNotFound.
func writeMissingBucket(t *testing.T, s Subject) {
	t.Helper()

	st := open(t, s, "")
	missing := newBucketName()

	_, err := st.PutObject(t.Context(), putIn(missing, "k", "v"))
	requireErr(t, err, storage.ErrBucketNotFound, errText(s, opPutObject, "bucket not found"))

	err = st.DeleteObject(
		t.Context(),
		&storage.DeleteObjectInput{BaseObjectInput: ref(missing, "k")},
	)
	requireErr(t, err, storage.ErrBucketNotFound, errText(s, opDeleteObject, "bucket not found"))

	_, err = st.ListObjects(t.Context(), &storage.ListObjectsInput{Bucket: &missing})
	requireErr(t, err, storage.ErrBucketNotFound, errText(s, opListObjects, "bucket not found"))
}

// DeleteObject deletes an object; deleting a key that holds none is not
// an error.
func deleteObject(t *testing.T, s Subject) {
	t.Helper()

	st, bucket := setup(t, s)

	put(t, st, bucket, "k", "v")

	for range 2 {
		require.NoError(t, st.DeleteObject(t.Context(),
			&storage.DeleteObjectInput{BaseObjectInput: ref(bucket, "k")}))
		requireAbsent(t, st, bucket, "k")
	}
}

// A backend never changes the caller's input, and keeps none of its maps;
// the metadata a read returns is the caller's own.
func callerUnchanged(t *testing.T, s Subject) {
	t.Helper()

	st, bucket := setup(t, s)
	in := fullInput(bucket, "k", "v")
	md := maps.Clone(in.Metadata)

	_, err := st.PutObject(t.Context(), in)
	require.NoError(t, err)
	assert.Equal(t, md, in.Metadata)
	assert.Equal(t, "k", in.Key)
	assert.Equal(t, bucket, *in.Bucket)

	in.Metadata["Author"] = "changed"

	info, err := st.HeadObject(t.Context(), headIn(bucket, "k"))
	require.NoError(t, err)
	assert.Equal(t, "Ann Lee", info.Metadata["author"])

	info.Metadata["author"] = "changed"

	again, err := st.HeadObject(t.Context(), headIn(bucket, "k"))
	require.NoError(t, err)
	assert.Equal(t, "Ann Lee", again.Metadata["author"])
}

// A Body GetObject returned stays readable after Close.
func bodyAfterClose(t *testing.T, s Subject) {
	t.Helper()

	st, bucket := setup(t, s)

	put(t, st, bucket, "k", "still here")

	obj, err := st.GetObject(t.Context(), getIn(bucket, "k"))
	require.NoError(t, err)
	require.NoError(t, st.Close(t.Context()))

	b, err := io.ReadAll(obj.Body)
	require.NoError(t, err)
	require.NoError(t, obj.Body.Close())
	assert.Equal(t, "still here", string(b))
}

// A nil Bucket names the configured bucket.
func configuredBucket(t *testing.T, s Subject) {
	t.Helper()

	st, bucket := setup(t, s)

	_, err := st.PutObject(t.Context(), &storage.PutObjectInput{
		Key:  "k",
		Body: strings.NewReader("v"),
	})
	require.NoError(t, err)
	requireBody(t, st, bucket, "k", "v")

	out, err := st.ListObjects(t.Context(), &storage.ListObjectsInput{})
	require.NoError(t, err)
	require.Len(t, out.Objects, 1)
}

// A nil Bucket with no bucket configured is ErrInvalidBucket, and so is
// a pointer to an empty name with one configured.
func noBucket(t *testing.T, s Subject) {
	t.Helper()

	st := open(t, s, "")
	none := storage.BaseObjectInput{Key: "k"}
	want := "invalid bucket: none given and none configured"

	_, err := st.PutObject(t.Context(), &storage.PutObjectInput{BaseObjectInput: none})
	requireErr(t, err, storage.ErrInvalidBucket, errText(s, opPutObject, want))

	_, err = st.HeadObject(t.Context(), &storage.HeadObjectInput{BaseObjectInput: none})
	requireErr(t, err, storage.ErrInvalidBucket, errText(s, opHeadObject, want))

	err = st.DeleteObject(t.Context(), &storage.DeleteObjectInput{BaseObjectInput: none})
	requireErr(t, err, storage.ErrInvalidBucket, errText(s, opDeleteObject, want))

	_, err = st.ListObjects(t.Context(), &storage.ListObjectsInput{})
	requireErr(t, err, storage.ErrInvalidBucket, errText(s, opListObjects, want))

	configured, _ := setup(t, s)

	_, err = configured.GetObject(t.Context(), getIn("", "k"))
	requireErr(
		t,
		err,
		storage.ErrInvalidBucket,
		errText(s, opGetObject, "invalid bucket: not 3 to 63 bytes"),
	)
}

// IfNoneMatch writes only a key that holds no object, and IfMatch only
// over the object with that ETag, quoted or not; a condition that does
// not hold is ErrPreconditionFailed and writes nothing, and IfMatch of a
// key that holds none is ErrObjectNotFound.
func conditions(t *testing.T, s Subject) {
	t.Helper()

	st, bucket := setup(t, s)
	write := func(body string, c storage.WriteCondition) error {
		in := putIn(bucket, "k", body)

		in.WriteCondition = c

		_, err := st.PutObject(t.Context(), in)

		return err
	}

	err := write("v1", storage.WriteCondition{IfMatch: new(md5Hex("v0"))})
	requireErr(t, err, storage.ErrObjectNotFound, errText(s, opPutObject, "object not found"))
	requireAbsent(t, st, bucket, "k")

	require.NoError(t, write("v1", storage.WriteCondition{IfNoneMatch: new("*")}))

	err = write("v2", storage.WriteCondition{IfNoneMatch: new("*")})
	requireErr(
		t,
		err,
		storage.ErrPreconditionFailed,
		errText(s, opPutObject, "precondition failed"),
	)

	err = write("v2", storage.WriteCondition{IfMatch: new(md5Hex("other"))})
	requireErr(
		t,
		err,
		storage.ErrPreconditionFailed,
		errText(s, opPutObject, "precondition failed"),
	)
	requireBody(t, st, bucket, "k", "v1")

	require.NoError(t, write("v2", storage.WriteCondition{IfMatch: new(md5Hex("v1"))}))
	require.NoError(t, write("v3", storage.WriteCondition{IfMatch: new(`"` + md5Hex("v2") + `"`)}))
	requireBody(t, st, bucket, "k", "v3")
}

// Of writes with IfNoneMatch that race on one key, one wins.
func conditionRace(t *testing.T, s Subject) {
	t.Helper()

	st, bucket := setup(t, s)

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		wins int
	)

	for range 8 {
		wg.Go(func() {
			in := putIn(bucket, "k", "v")

			in.IfNoneMatch = new("*")

			_, err := st.PutObject(context.Background(), in)
			if err == nil {
				mu.Lock()

				wins++

				mu.Unlock()

				return
			}

			assert.ErrorIs(t, err, storage.ErrPreconditionFailed)
		})
	}

	wg.Wait()
	assert.Equal(t, 1, wins)
}

// A Range selects bytes of the object, with Size their length.
func rangeRule(t *testing.T, s Subject) {
	t.Helper()

	st, bucket := setup(t, s)

	put(t, st, bucket, "k", "0123456789")

	for rng, want := range map[string]string{
		"bytes=0-4":                    "01234",
		"bytes=5-":                     "56789",
		"bytes=-3":                     "789",
		"bytes=8-100":                  "89",
		"bytes=-100":                   "0123456789",
		" Bytes=1-1\t":                 "1", //nolint:gocritic // the blanks around it are the rule.
		"bytes=0-99999999999999999999": "0123456789",
	} {
		in := getIn(bucket, "k")

		in.Range = &rng

		obj, body := get(t, st, in)
		assert.Equal(t, want, body, rng)
		assert.Equal(t, int64(len(want)), obj.Size, rng)

		info, err := st.HeadObject(t.Context(), &in.HeadObjectInput)
		require.NoError(t, err)
		assert.Equal(t, int64(len(want)), info.Size, rng)
	}
}

// A Range whose first byte is at or past the end, `bytes=-0`, and a
// first-last range on an empty object are ErrRangeNotSatisfiable; a
// suffix on an empty object selects nothing.
func rangeNotSatisfiable(t *testing.T, s Subject) {
	t.Helper()

	st, bucket := setup(t, s)

	put(t, st, bucket, "k", "0123456789")
	put(t, st, bucket, "empty", "")

	for _, c := range []struct{ key, rng string }{
		{"k", "bytes=10-"}, {"k", "bytes=10-20"}, {"k", "bytes=-0"}, {"empty", "bytes=0-0"},
	} {
		in := getIn(bucket, c.key)

		in.Range = &c.rng

		_, err := st.GetObject(t.Context(), in)
		requireErr(
			t,
			err,
			storage.ErrRangeNotSatisfiable,
			errText(s, opGetObject, "range not satisfiable"),
		)
	}

	in := getIn(bucket, "empty")

	in.Range = new("bytes=-5")

	obj, body := get(t, st, in)
	assert.Empty(t, body)
	assert.Equal(t, int64(0), obj.Size)
}

// A Version or a PartNumber is ErrNotSupported on a backend that keeps
// no versions or parts, after the rules and before any I/O.
func unsupported(t *testing.T, s Subject) {
	t.Helper()

	st := open(t, s, "")
	missing := newBucketName()

	version := getIn(missing, "k")

	version.Version = new("v1")

	part := getIn(missing, "k")

	part.PartNumber = new(int32(1))

	for _, in := range []*storage.GetObjectInput{version, part} {
		_, err := st.GetObject(t.Context(), in)
		require.ErrorIs(t, err, storage.ErrNotSupported)

		_, err = st.HeadObject(t.Context(), &in.HeadObjectInput)
		require.ErrorIs(t, err, storage.ErrNotSupported)
	}

	err := st.DeleteObject(t.Context(), &storage.DeleteObjectInput{
		BaseObjectInput: ref(missing, "k"),
		Version:         new("v1"),
	})
	require.ErrorIs(t, err, storage.ErrNotSupported)
}

// No error holds a key, a bucket name, a metadata name or value, a header
// value or a token.
func errorsHoldNoData(t *testing.T, s Subject) {
	t.Helper()

	st, bucket := setup(t, s)
	secret := "zq7"
	missing := "zq7-" + newBucketName()
	ctx := t.Context()

	put(t, st, bucket, "zq7/key", "v")

	var errs []error

	record := func(_ any, err error) { errs = append(errs, err) }

	record(st.HeadObject(ctx, headIn(bucket, "zq7/missing")))
	record(st.GetObject(ctx, getIn(missing, "zq7/key")))
	record(st.HeadObject(ctx, headIn("ZQ7-bucket", "k")))
	record(st.HeadObject(ctx, headIn(bucket, "zq7/.")))
	record(st.PutObject(ctx, putIn(missing, "zq7/key", "v")))

	for _, edit := range []func(*storage.PutObjectInput){
		func(in *storage.PutObjectInput) { in.Metadata = map[string]string{"zq7 name": "v"} },
		func(in *storage.PutObjectInput) { in.Metadata = map[string]string{"n": "zq7\n"} },
		func(in *storage.PutObjectInput) { in.ContentType = new("zq7\r\n") },
		func(in *storage.PutObjectInput) { in.Expires = new("zq7") },
		func(in *storage.PutObjectInput) { in.IfNoneMatch = new("zq7") },
		func(in *storage.PutObjectInput) { in.IfMatch = new("zq7") },
	} {
		in := putIn(bucket, "zq7/key", "v")
		edit(in)
		record(st.PutObject(ctx, in))
	}

	record(
		st.ListObjects(
			ctx,
			&storage.ListObjectsInput{Bucket: &bucket, ContinuationToken: new("zq7zq7")},
		),
	)
	record(st.ListBuckets(ctx, &storage.ListBucketsInput{ContinuationToken: new("zq7zq7")}))

	errs = append(errs, st.DeleteBucket(ctx, &storage.DeleteBucketInput{Bucket: missing}))

	for _, err := range errs {
		require.Error(t, err)
		assert.NotContains(t, strings.ToLower(err.Error()), secret)
		assert.NotContains(t, err.Error(), bucket)
	}
}
