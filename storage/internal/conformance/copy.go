//nolint:mnd // test rules: counts read best where they are checked.
package conformance

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/storage"
)

// copyRules returns the rules of CopyObject.
func copyRules() map[string]func(*testing.T, Subject) {
	return map[string]func(*testing.T, Subject){
		"CopySameBucket":       copySameBucket,
		"CopyCrossBucket":      copyCrossBucket,
		"CopyKeepsExpires":     copyKeepsExpires,
		"CopyOddKeys":          copyOddKeys,
		"CopyReplace":          copyReplace,
		"CopyOverwrite":        copyOverwrite,
		"CopyOntoItself":       copyOntoItself,
		"CopyMissing":          copyMissing,
		"CopyInvalidInput":     copyInvalidInput,
		"CopyCallerUnchanged":  copyCallerUnchanged,
		"CopyConcurrent":       copyConcurrent,
		"CopyErrorsHoldNoData": copyErrorsHoldNoData,
	}
}

// copyIn returns the CopyObjectInput of srcKey in srcBucket to dstKey in
// dstBucket, keeping the source's header fields and metadata.
func copyIn(srcBucket, srcKey, dstBucket, dstKey string) *storage.CopyObjectInput {
	return &storage.CopyObjectInput{
		BaseObjectInput: ref(dstBucket, dstKey),
		Source:          ref(srcBucket, srcKey),
	}
}

// fullMetadata returns the metadata fullInput sets, with the names in
// lower case, as a read returns them.
func fullMetadata() map[string]string {
	md := map[string]string{}

	for k, v := range fullInput("", "", "").Metadata {
		md[strings.ToLower(k)] = v
	}

	return md
}

// requireCopied fails t unless the object under key in bucket holds body,
// the header fields and metadata of fullInput, but for Expires, which
// copyKeepsExpires checks, and the ETag and size out reports, written
// between from and to.
func requireCopied(
	t *testing.T,
	st Store,
	bucket, key, body string,
	out *storage.CopyObjectOutput,
	from, to time.Time,
) {
	t.Helper()

	assert.Equal(t, md5Hex(body), out.ETag)
	assert.Equal(t, int64(len(body)), out.Size)

	info, err := st.HeadObject(t.Context(), headIn(bucket, key))
	require.NoError(t, err)

	obj, got := get(t, st, getIn(bucket, key))

	want := fullValues

	want.Expires = ""

	for _, i := range []*storage.ObjectInfo{info, &obj.ObjectInfo} {
		got := i.HeaderValues

		got.Expires = ""

		assert.Equal(t, out.ETag, i.ETag)
		assert.Equal(t, out.Size, i.Size)
		assert.Equal(t, want, got)
		assert.Equal(t, fullMetadata(), i.Metadata)
		requireModified(t, i.LastModified, from, to)
	}

	assert.Equal(t, body, got)
}

// A copy within a bucket holds the source's contents, header fields and
// metadata, with the ETag and the size CopyObject returned, and leaves
// the source as it was.
func copySameBucket(t *testing.T, s Subject) {
	t.Helper()

	st, bucket := setup(t, s)

	_, err := st.PutObject(t.Context(), fullInput(bucket, "src", "Tom"))
	require.NoError(t, err)

	before := time.Now()

	out, err := st.CopyObject(t.Context(), copyIn(bucket, "src", bucket, "cats/dst"))
	require.NoError(t, err)

	after := time.Now()

	requireCopied(t, st, bucket, "cats/dst", "Tom", out, before, after)
	requireBody(t, st, bucket, "src", "Tom")
}

// A copy goes to another bucket, and a nil Bucket, of the source or of
// the destination, names the configured one.
func copyCrossBucket(t *testing.T, s Subject) {
	t.Helper()

	st, bucket := setup(t, s)
	other := newBucketName()

	require.NoError(t, st.CreateBucket(t.Context(), &storage.CreateBucketInput{Bucket: other}))

	body := strings.Repeat("0123456789abcdef", 1<<12)

	_, err := st.PutObject(t.Context(), fullInput(bucket, "src", body))
	require.NoError(t, err)

	before := time.Now()

	out, err := st.CopyObject(t.Context(), &storage.CopyObjectInput{
		BaseObjectInput: ref(other, "dst"),
		Source:          storage.BaseObjectInput{Key: "src"},
	})
	require.NoError(t, err)

	back, err := st.CopyObject(t.Context(), &storage.CopyObjectInput{
		Key:    "back",
		Source: ref(other, "dst"),
	})
	require.NoError(t, err)

	after := time.Now()

	requireCopied(t, st, other, "dst", body, out, before, after)
	requireCopied(t, st, bucket, "back", body, back, before, after)
}

// Keys that keep the rules are copied from and to, however odd, with
// the characters a URL or a header gives a meaning to.
func copyOddKeys(t *testing.T, s Subject) {
	t.Helper()

	st, bucket := setup(t, s)

	for _, key := range []string{
		`a\.`, "../x", "a//b", "/lead", "trail/", "a/./b", "sp ace", "%2e%2e", "q?x#y", "é/ü",
		"a+b", "a&b=c;d,e:f@g$h", strings.Repeat("k", 1024),
	} {
		put(t, st, bucket, key, "body of "+key)

		_, err := st.CopyObject(t.Context(), copyIn(bucket, key, bucket, "dst"))
		require.NoError(t, err, key)
		requireBody(t, st, bucket, "dst", "body of "+key)

		put(t, st, bucket, "src", "back to "+key)

		_, err = st.CopyObject(t.Context(), copyIn(bucket, "src", bucket, key))
		require.NoError(t, err, key)
		requireBody(t, st, bucket, key, "back to "+key)
	}
}

// A copy keeps the source's Expires.
func copyKeepsExpires(t *testing.T, s Subject) {
	t.Helper()

	st, bucket := setup(t, s)

	_, err := st.PutObject(t.Context(), fullInput(bucket, "src", "Tom"))
	require.NoError(t, err)

	_, err = st.CopyObject(t.Context(), copyIn(bucket, "src", bucket, "dst"))
	require.NoError(t, err)

	info, err := st.HeadObject(t.Context(), headIn(bucket, "dst"))
	require.NoError(t, err)
	assert.Equal(t, fullValues.Expires, info.Expires)
}

// With Replace, the destination gets exactly the input's header fields
// and metadata, a nil ContentType as DefaultContentType, and the source's
// contents.
func copyReplace(t *testing.T, s Subject) {
	t.Helper()

	st, bucket := setup(t, s)

	_, err := st.PutObject(t.Context(), fullInput(bucket, "src", "Tom"))
	require.NoError(t, err)

	in := copyIn(bucket, "src", bucket, "csv")

	in.Replace = true
	in.ContentType = new("text/csv")
	in.ContentLanguage = new("fr")
	in.Metadata = map[string]string{"New-Name": "1"}

	out, err := st.CopyObject(t.Context(), in)
	require.NoError(t, err)
	assert.Equal(t, md5Hex("Tom"), out.ETag)
	assert.Equal(t, int64(3), out.Size)

	obj, body := get(t, st, getIn(bucket, "csv"))
	assert.Equal(t, "Tom", body)
	assert.Equal(t, md5Hex("Tom"), obj.ETag)
	assert.Equal(t, storage.HeaderValues{ContentType: "text/csv", ContentLanguage: "fr"},
		obj.HeaderValues)
	assert.Equal(t, map[string]string{"new-name": "1"}, obj.Metadata)

	bare := copyIn(bucket, "src", bucket, "bare")

	bare.Replace = true

	_, err = st.CopyObject(t.Context(), bare)
	require.NoError(t, err)

	info, err := st.HeadObject(t.Context(), headIn(bucket, "bare"))
	require.NoError(t, err)
	assert.Equal(t, storage.HeaderValues{ContentType: storage.DefaultContentType},
		info.HeaderValues)
	assert.Empty(t, info.Metadata)
}

// A copy replaces the object under the destination key, its header
// fields and metadata included.
func copyOverwrite(t *testing.T, s Subject) {
	t.Helper()

	st, bucket := setup(t, s)

	put(t, st, bucket, "src", "new")

	_, err := st.PutObject(t.Context(), fullInput(bucket, "dst", "old contents"))
	require.NoError(t, err)

	out, err := st.CopyObject(t.Context(), copyIn(bucket, "src", bucket, "dst"))
	require.NoError(t, err)
	assert.Equal(t, int64(3), out.Size)

	obj, body := get(t, st, getIn(bucket, "dst"))
	assert.Equal(t, "new", body)
	assert.Equal(t, storage.HeaderValues{ContentType: storage.DefaultContentType},
		obj.HeaderValues)
	assert.Empty(t, obj.Metadata)
}

// A copy of an object onto itself is ErrInvalidArgument without Replace,
// before any I/O, whichever way the bucket is named; with Replace it
// replaces the header fields and metadata and keeps the contents.
func copyOntoItself(t *testing.T, s Subject) {
	t.Helper()

	st, bucket := setup(t, s)
	want := errText(
		s,
		opCopyObject,
		"invalid argument: copies an object onto itself without Replace",
	)

	_, err := st.PutObject(t.Context(), fullInput(bucket, "k", "Tom"))
	require.NoError(t, err)

	for _, in := range []*storage.CopyObjectInput{
		copyIn(bucket, "k", bucket, "k"),
		{Key: "k", Source: ref(bucket, "k")},
		copyIn("gasconf-missing-"+runID, "k", "gasconf-missing-"+runID, "k"),
	} {
		_, err = st.CopyObject(t.Context(), in)
		requireErr(t, err, storage.ErrInvalidArgument, want)
	}

	in := copyIn(bucket, "k", bucket, "k")

	in.Replace = true
	in.CacheControl = new("no-cache")
	in.Metadata = map[string]string{"Edited": "yes"}

	out, err := st.CopyObject(t.Context(), in)
	require.NoError(t, err)
	assert.Equal(t, md5Hex("Tom"), out.ETag)
	assert.Equal(t, int64(3), out.Size)

	obj, body := get(t, st, getIn(bucket, "k"))
	assert.Equal(t, "Tom", body)
	assert.Equal(t, storage.HeaderValues{
		ContentType:  storage.DefaultContentType,
		CacheControl: "no-cache",
	}, obj.HeaderValues)
	assert.Equal(t, map[string]string{"edited": "yes"}, obj.Metadata)
}

// A missing source is ErrObjectNotFound, and a source in a missing
// bucket ErrObjectNotFound and ErrBucketNotFound, as GetObject reports
// them; a missing destination bucket is ErrBucketNotFound, as PutObject
// reports it. None writes anything.
func copyMissing(t *testing.T, s Subject) {
	t.Helper()

	st, bucket := setup(t, s)
	missing := newBucketName()

	put(t, st, bucket, "src", "v")

	_, err := st.CopyObject(t.Context(), copyIn(bucket, "absent", bucket, "dst"))
	requireErr(t, err, storage.ErrObjectNotFound, errText(s, opCopyObject, "object not found"))
	require.NotErrorIs(t, err, storage.ErrBucketNotFound)
	requireAbsent(t, st, bucket, "dst")

	_, err = st.CopyObject(t.Context(), copyIn(missing, "src", bucket, "dst"))
	require.ErrorIs(t, err, storage.ErrObjectNotFound)
	requireErr(t, err, storage.ErrBucketNotFound,
		errText(s, opCopyObject, "object not found: bucket not found"))
	requireAbsent(t, st, bucket, "dst")

	_, err = st.CopyObject(t.Context(), copyIn(bucket, "src", missing, "dst"))
	requireErr(t, err, storage.ErrBucketNotFound, errText(s, opCopyObject, "bucket not found"))
	require.NotErrorIs(t, err, storage.ErrObjectNotFound)

	_, err = st.HeadBucket(t.Context(), &storage.HeadBucketInput{Bucket: missing})
	require.ErrorIs(t, err, storage.ErrBucketNotFound)
}

// An input the rules refuse fails before any I/O, the source's key and
// bucket first, after `source: `, then the destination's, after
// `destination: `, then the header fields and the metadata, which must
// not be set without Replace; nothing is written.
//
//nolint:funlen // one case per rule.
func copyInvalidInput(t *testing.T, s Subject) {
	t.Helper()

	st, bucket := setup(t, s)
	unconfigured := open(t, s, "")
	missing := newBucketName()

	put(t, st, bucket, "src", "v")

	for want, c := range map[string]struct {
		target error
		in     *storage.CopyObjectInput
		st     Store
	}{
		"source: invalid key: empty": {
			storage.ErrInvalidKey, copyIn(missing, "", "BAD", ""), st,
		},
		"source: invalid bucket: holds a character other than a-z, 0-9, . and -": {
			storage.ErrInvalidBucket, copyIn("BAD", "src", bucket, ""), st,
		},
		"source: invalid bucket: none given and none configured": {
			storage.ErrInvalidBucket,
			&storage.CopyObjectInput{BaseObjectInput: ref(missing, "dst"), Source: storage.BaseObjectInput{Key: "src"}},
			unconfigured,
		},
		`destination: invalid key: its last path element is "."`: {
			storage.ErrInvalidKey, copyIn(bucket, "src", "BAD", "a/."), st,
		},
		"destination: invalid bucket: not 3 to 63 bytes": {
			storage.ErrInvalidBucket, copyIn(bucket, "src", "", "dst"), st,
		},
		"invalid argument: header fields or metadata set without Replace": {
			storage.ErrInvalidArgument,
			&storage.CopyObjectInput{
				BaseObjectInput: ref(bucket, "dst"),
				ContentType:     new("text/plain"),
				Source:          ref(bucket, "src"),
			},
			st,
		},
		"invalid argument: header fields or metadata set without Replace ": {
			storage.ErrInvalidArgument,
			&storage.CopyObjectInput{
				BaseObjectInput: ref(bucket, "dst"),
				Metadata:        map[string]string{"n": "v"},
				Source:          ref(bucket, "src"),
			},
			st,
		},
		"invalid header: Expires is not an IMF-fixdate": {
			storage.ErrInvalidHeader,
			&storage.CopyObjectInput{
				BaseObjectInput: ref(bucket, "dst"),
				Expires:         new("tomorrow"),
				Source:          ref(bucket, "src"),
				Replace:         true,
			},
			st,
		},
		"invalid metadata: two names differ only in case": {
			storage.ErrInvalidMetadata,
			&storage.CopyObjectInput{
				BaseObjectInput: ref(bucket, "dst"),
				Metadata:        map[string]string{"N": "v", "n": "v"},
				Source:          ref(bucket, "src"),
				Replace:         true,
			},
			st,
		},
	} {
		_, err := c.st.CopyObject(t.Context(), c.in)
		requireErr(t, err, c.target, errText(s, opCopyObject, strings.TrimRight(want, " ")))
		requireAbsent(t, st, bucket, "dst")
	}
}

// CopyObject never changes the caller's input, and keeps none of its
// maps.
func copyCallerUnchanged(t *testing.T, s Subject) {
	t.Helper()

	st, bucket := setup(t, s)

	put(t, st, bucket, "src", "v")

	in := copyIn(bucket, "src", bucket, "dst")

	owner := "Bo Chan"

	in.Replace = true
	in.Metadata = map[string]string{"Owner": owner}

	_, err := st.CopyObject(t.Context(), in)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"Owner": owner}, in.Metadata)
	assert.Equal(t, copyIn(bucket, "src", bucket, "dst").Source, in.Source)
	assert.Equal(t, "dst", in.Key)

	in.Metadata["Owner"] = "edited"

	info, err := st.HeadObject(t.Context(), headIn(bucket, "dst"))
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"owner": owner}, info.Metadata)
}

// Copies that cross, a to b and b to a, in one bucket and across two,
// with copies of each object onto itself that replace its metadata, run
// at once without waiting on each other, for -race; each object then
// holds the contents and the metadata of one write. The objects hold the
// same contents, so that no copy changes the contents of a source another
// copy reads, which S3 would refuse as a conflict past its retries.
func copyConcurrent(t *testing.T, s Subject) {
	t.Helper()

	st, bucket := setup(t, s)
	other := newBucketName()
	body := strings.Repeat("0123456789abcdef", 1<<10)

	require.NoError(t, st.CreateBucket(t.Context(), &storage.CreateBucketInput{Bucket: other}))

	for _, b := range []string{bucket, other} {
		for _, k := range []string{"a", "b"} {
			put(t, st, b, k, body)
		}
	}

	ctx, cancel := context.WithTimeout(t.Context(), 4*prompt)
	defer cancel()

	var wg sync.WaitGroup

	for _, pr := range []*storage.CopyObjectInput{
		copyIn(bucket, "a", bucket, "b"),
		copyIn(bucket, "b", bucket, "a"),
		copyIn(bucket, "a", other, "a"),
		copyIn(other, "a", bucket, "a"),
		copyIn(bucket, "b", other, "b"),
		copyIn(other, "b", bucket, "b"),
	} {
		wg.Go(func() { copyBackAndForth(ctx, t, st, pr) })
	}

	wg.Wait()
	require.NoError(t, ctx.Err(), "the copies did not end in time")

	for _, b := range []string{bucket, other} {
		for _, k := range []string{"a", "b"} {
			requireOneWrite(t, st, b, k, body)
		}
	}
}

// copyBackAndForth makes the copy of in five times, each followed by a
// copy of its source onto itself that replaces its metadata, and reports
// a failure to t.
func copyBackAndForth(ctx context.Context, t *testing.T, st Store, in *storage.CopyObjectInput) {
	t.Helper()

	for n := range 5 {
		_, err := st.CopyObject(ctx, in)
		if err != nil {
			t.Errorf("copy: %v", err)

			return
		}

		self := copyIn(*in.Source.Bucket, in.Source.Key, *in.Source.Bucket, in.Source.Key)

		self.Replace = true
		self.Metadata = map[string]string{"n": strconv.Itoa(n)}

		_, err = st.CopyObject(ctx, self)
		if err != nil {
			t.Errorf("copy onto itself: %v", err)

			return
		}
	}
}

// requireOneWrite fails t unless the object under key in bucket holds
// body and either no metadata or the metadata of one of the copies of
// copyConcurrent onto itself.
func requireOneWrite(t *testing.T, st Store, bucket, key, body string) {
	t.Helper()

	obj, got := get(t, st, getIn(bucket, key))
	assert.Equal(t, body, got)
	assert.Equal(t, md5Hex(body), obj.ETag)

	if len(obj.Metadata) == 0 {
		return
	}

	n, err := strconv.Atoi(obj.Metadata["n"])
	require.NoError(t, err)
	assert.True(t, n >= 0 && n < 5, "%s/%s holds metadata no write wrote", bucket, key)
	assert.Len(t, obj.Metadata, 1)
}

// No error of CopyObject holds a key, a bucket name, a metadata name or
// value, or a header value.
func copyErrorsHoldNoData(t *testing.T, s Subject) {
	t.Helper()

	st, bucket := setup(t, s)
	secret := "zq7"
	missing := "zq7-" + newBucketName()
	ctx := t.Context()

	put(t, st, bucket, "zq7/key", "v")

	var errs []error

	record := func(_ any, err error) { errs = append(errs, err) }

	record(st.CopyObject(ctx, copyIn(bucket, "zq7/missing", bucket, "zq7/dst")))
	record(st.CopyObject(ctx, copyIn(missing, "zq7/key", bucket, "zq7/dst")))
	record(st.CopyObject(ctx, copyIn(bucket, "zq7/key", missing, "zq7/dst")))
	record(st.CopyObject(ctx, copyIn(bucket, "zq7/key", bucket, "zq7/key")))
	record(st.CopyObject(ctx, copyIn("ZQ7-bucket", "zq7/key", bucket, "zq7/dst")))
	record(st.CopyObject(ctx, copyIn(bucket, "zq7/.", bucket, "zq7/dst")))

	for _, edit := range []func(*storage.CopyObjectInput){
		func(in *storage.CopyObjectInput) { in.Metadata = map[string]string{"zq7 name": "v"} },
		func(in *storage.CopyObjectInput) { in.Metadata = map[string]string{"n": "zq7\r"} },
		func(in *storage.CopyObjectInput) { in.ContentType = new("zq7\r\n") },
	} {
		for _, replace := range []bool{false, true} {
			in := copyIn(bucket, "zq7/key", bucket, "zq7/dst")

			in.Replace = replace

			edit(in)
			record(st.CopyObject(ctx, in))
		}
	}

	for _, err := range errs {
		require.Error(t, err)
		assert.NotContains(t, strings.ToLower(err.Error()), secret)
		assert.NotContains(t, err.Error(), bucket)
	}
}
