//nolint:mnd,goconst // test rules: counts and texts read best where they are checked.
package conformance

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/storage"
)

// bucketRules returns the rules of the buckets.
func bucketRules() map[string]func(*testing.T, Subject) {
	return map[string]func(*testing.T, Subject){
		"Buckets":               buckets,
		"BucketNotEmpty":        bucketNotEmpty,
		"ListBuckets":           listBuckets,
		"ListBucketsArguments":  listBucketsArguments,
		"ListBucketsTokenBound": listBucketsTokenBound,
	}
}

// listRules returns the rules of ListObjects.
func listRules() map[string]func(*testing.T, Subject) {
	return map[string]func(*testing.T, Subject){
		"ListObjects":    listObjects,
		"ListNonASCII":   listNonASCII,
		"ListDelimiter":  listDelimiter,
		"ListStartAfter": listStartAfter,
		"ListArguments":  listArguments,
		"ListTokenBound": listTokenBound,
	}
}

// CreateBucket creates a bucket, and again is not an error; HeadBucket and
// ListBuckets describe it; DeleteBucket deletes it, and HeadBucket and
// DeleteBucket of it are then ErrBucketNotFound.
func buckets(t *testing.T, s Subject) {
	t.Helper()

	st := open(t, s, "")
	name := newBucketName()
	ctx := t.Context()
	before := time.Now()

	require.NoError(t, st.CreateBucket(ctx, &storage.CreateBucketInput{Bucket: name}))

	after := time.Now()

	require.NoError(t, st.CreateBucket(ctx, &storage.CreateBucketInput{Bucket: name}))

	b, err := st.HeadBucket(ctx, &storage.HeadBucketInput{Bucket: name})
	require.NoError(t, err)
	assert.Equal(t, name, b.Name)
	assert.True(t, b.CreationDate.IsZero())

	out, err := st.ListBuckets(ctx, &storage.ListBucketsInput{Prefix: &name})
	require.NoError(t, err)
	require.Len(t, out.Buckets, 1)
	assert.Equal(t, name, out.Buckets[0].Name)
	requireModified(t, out.Buckets[0].CreationDate, before, after)
	assert.Empty(t, out.NextContinuationToken)

	require.NoError(t, st.DeleteBucket(ctx, &storage.DeleteBucketInput{Bucket: name}))

	_, err = st.HeadBucket(ctx, &storage.HeadBucketInput{Bucket: name})
	requireErr(t, err, storage.ErrBucketNotFound, errText(s, opHeadBucket, "bucket not found"))

	err = st.DeleteBucket(ctx, &storage.DeleteBucketInput{Bucket: name})
	requireErr(t, err, storage.ErrBucketNotFound, errText(s, opDeleteBucket, "bucket not found"))
}

// DeleteBucket of a bucket that holds an object is ErrBucketNotEmpty,
// and deletes nothing.
func bucketNotEmpty(t *testing.T, s Subject) {
	t.Helper()

	st, bucket := setup(t, s)

	put(t, st, bucket, "k", "v")

	err := st.DeleteBucket(t.Context(), &storage.DeleteBucketInput{Bucket: bucket})
	requireErr(t, err, storage.ErrBucketNotEmpty, errText(s, opDeleteBucket, "bucket not empty"))
	requireBody(t, st, bucket, "k", "v")
}

// ListBuckets returns the buckets in name order, a page at a time, each
// once.
func listBuckets(t *testing.T, s Subject) {
	t.Helper()

	st := open(t, s, "")
	prefix := newBucketName() + "-"
	names := []string{prefix + "a", prefix + "b", prefix + "c"}

	for _, n := range []string{names[2], names[0], names[1]} {
		require.NoError(t, st.CreateBucket(t.Context(), &storage.CreateBucketInput{Bucket: n}))
	}

	all, err := st.ListBuckets(t.Context(), &storage.ListBucketsInput{Prefix: &prefix})
	require.NoError(t, err)
	assert.Equal(t, names, bucketNames(all.Buckets))

	in := &storage.ListBucketsInput{Prefix: &prefix, MaxBuckets: new(int32(2))}

	var got []string

	for range 3 {
		out, err := st.ListBuckets(t.Context(), in)
		require.NoError(t, err)

		got = append(got, bucketNames(out.Buckets)...)

		if out.NextContinuationToken == "" {
			break
		}

		in.ContinuationToken = &out.NextContinuationToken
	}

	assert.Equal(t, names, got)
}

// bucketNames returns the names of bs.
func bucketNames(bs []storage.Bucket) []string {
	names := make([]string, 0, len(bs))
	for _, b := range bs {
		names = append(names, b.Name)
	}

	return names
}

// A MaxBuckets out of its bounds, or an invalid Prefix, is
// ErrInvalidArgument, and an empty or forged token ErrInvalidToken.
func listBucketsArguments(t *testing.T, s Subject) {
	t.Helper()

	st := open(t, s, "")

	for want, in := range map[string]*storage.ListBucketsInput{
		"invalid argument: MaxBuckets is not 1 to 10000":  {MaxBuckets: new(int32(0))},
		"invalid argument: MaxBuckets is not 1 to 10000 ": {MaxBuckets: new(int32(10001))},
		"invalid argument: Prefix is not valid UTF-8 of at most 1024 bytes": {
			Prefix: new("\xff"),
		},
		"invalid continuation token: empty": {ContinuationToken: new("")},
	} {
		_, err := st.ListBuckets(t.Context(), in)
		require.EqualError(t, err, errText(s, opListBuckets, strings.TrimRight(want, " ")))
	}

	_, err := st.ListBuckets(t.Context(), &storage.ListBucketsInput{ContinuationToken: new("AAAA")})
	require.ErrorIs(t, err, storage.ErrInvalidToken)
}

// A ListBuckets token is refused for another Prefix.
func listBucketsTokenBound(t *testing.T, s Subject) {
	t.Helper()

	st := open(t, s, "")
	prefix := newBucketName() + "-"

	for _, n := range []string{"a", "b"} {
		require.NoError(
			t,
			st.CreateBucket(t.Context(), &storage.CreateBucketInput{Bucket: prefix + n}),
		)
	}

	out, err := st.ListBuckets(
		t.Context(),
		&storage.ListBucketsInput{Prefix: &prefix, MaxBuckets: new(int32(1))},
	)
	require.NoError(t, err)
	require.NotEmpty(t, out.NextContinuationToken)

	_, err = st.ListBuckets(t.Context(), &storage.ListBucketsInput{
		Prefix:            new(prefix + "a"),
		ContinuationToken: &out.NextContinuationToken,
	})
	require.ErrorIs(t, err, storage.ErrInvalidToken)
}

// listAll lists every page of in, checking that each page keeps to
// MaxKeys and that the token and IsTruncated agree, and returns the
// entries in order, keys and common prefixes, the prefixes ending with a
// `*`.
func listAll(t *testing.T, st Store, in *storage.ListObjectsInput) []string {
	t.Helper()

	var got []string

	for range 100 {
		out, err := st.ListObjects(t.Context(), in)
		require.NoError(t, err)

		if in.MaxKeys != nil {
			require.LessOrEqual(t, len(out.Objects)+len(out.CommonPrefixes), int(*in.MaxKeys))
		}

		got = append(got, merge(out)...)

		require.Equal(t, out.IsTruncated, out.NextContinuationToken != "")

		if !out.IsTruncated {
			return got
		}

		in.ContinuationToken = &out.NextContinuationToken
	}

	require.Fail(t, "the listing did not end")

	return nil
}

// merge returns the keys and the common prefixes of out in byte order,
// the prefixes ending with a `*`.
func merge(out *storage.ListObjectsOutput) []string {
	var got []string

	keys, prefixes := out.Objects, out.CommonPrefixes

	for len(keys) > 0 || len(prefixes) > 0 {
		if len(prefixes) == 0 || (len(keys) > 0 && keys[0].Key < prefixes[0]) {
			got = append(got, keys[0].Key)
			keys = keys[1:]

			continue
		}

		got = append(got, prefixes[0]+"*")
		prefixes = prefixes[1:]
	}

	return got
}

// ListObjects returns the objects in key order, a page at a time, each
// once, with the ETag, size and time HeadObject gives; Prefix limits it.
func listObjects(t *testing.T, s Subject) {
	t.Helper()

	st, bucket := setup(t, s)
	keys := []string{"a", "b/1", "b/2", "c"}

	for _, k := range []string{"c", "b/2", "a", "b/1"} {
		put(t, st, bucket, k, "body "+k)
	}

	empty, err := st.ListObjects(
		t.Context(),
		&storage.ListObjectsInput{Bucket: &bucket, Prefix: new("z")},
	)
	require.NoError(t, err)
	assert.Empty(t, empty.Objects)
	assert.Empty(t, empty.CommonPrefixes)
	assert.False(t, empty.IsTruncated)
	assert.Empty(t, empty.NextContinuationToken)

	for _, n := range []int32{1, 2, 1000} {
		assert.Equal(
			t,
			keys,
			listAll(t, st, &storage.ListObjectsInput{Bucket: &bucket, MaxKeys: &n}),
		)
	}

	assert.Equal(t, []string{"b/1", "b/2"},
		listAll(t, st, &storage.ListObjectsInput{Bucket: &bucket, Prefix: new("b/")}))

	out, err := st.ListObjects(
		t.Context(),
		&storage.ListObjectsInput{Bucket: &bucket, Prefix: new("c")},
	)
	require.NoError(t, err)
	require.Len(t, out.Objects, 1)

	info, err := st.HeadObject(t.Context(), headIn(bucket, "c"))
	require.NoError(t, err)
	assert.Equal(t, storage.ObjectSummary{
		LastModified: info.LastModified,
		Key:          "c",
		ETag:         info.ETag,
		Size:         info.Size,
	}, out.Objects[0])
}

// A key that is not ASCII is listed in byte order, after every ASCII key,
// on every page size, the pages after the first included.
func listNonASCII(t *testing.T, s Subject) {
	t.Helper()

	st, bucket := setup(t, s)
	keys := []string{"a", "c", "é", "ö/1"}

	for _, k := range []string{"ö/1", "é", "c", "a"} {
		put(t, st, bucket, k, "body "+k)
	}

	for _, n := range []int32{1, 2, 1000} {
		assert.Equal(
			t,
			keys,
			listAll(t, st, &storage.ListObjectsInput{Bucket: &bucket, MaxKeys: &n}),
		)
	}

	assert.Equal(t, []string{"ö/1"},
		listAll(t, st, &storage.ListObjectsInput{Bucket: &bucket, Prefix: new("ö")}))
}

// A Delimiter rolls keys up into common prefixes, each listed once across
// the pages, in the place of its first key.
func listDelimiter(t *testing.T, s Subject) {
	t.Helper()

	st, bucket := setup(t, s)

	for _, k := range []string{"a/1", "a/2", "b", "c/1/x", "c/2", "d--e", "d--f/g"} {
		put(t, st, bucket, k, "v")
	}

	for _, n := range []int32{1, 2, 1000} {
		in := &storage.ListObjectsInput{Bucket: &bucket, Delimiter: new("/"), MaxKeys: &n}
		assert.Equal(t, []string{"a/*", "b", "c/*", "d--e", "d--f/*"}, listAll(t, st, in), n)

		in = &storage.ListObjectsInput{
			Bucket:    &bucket,
			Delimiter: new("/"),
			Prefix:    new("c/"),
			MaxKeys:   &n,
		}
		assert.Equal(t, []string{"c/1/*", "c/2"}, listAll(t, st, in), n)

		in = &storage.ListObjectsInput{Bucket: &bucket, Delimiter: new("--"), MaxKeys: &n}
		assert.Equal(t, []string{"a/1", "a/2", "b", "c/1/x", "c/2", "d--*"}, listAll(t, st, in), n)
	}
}

// StartAfter starts the listing after a key, which need not exist, and
// applies to the keys before they are rolled up.
func listStartAfter(t *testing.T, s Subject) {
	t.Helper()

	st, bucket := setup(t, s)

	for _, k := range []string{"a/1", "a/2", "b", "c"} {
		put(t, st, bucket, k, "v")
	}

	for after, want := range map[string][]string{
		"a/1": {"a/2", "b", "c"},
		"a/9": {"b", "c"},
		"bb":  {"c"},
		"c":   nil,
	} {
		in := &storage.ListObjectsInput{Bucket: &bucket, StartAfter: &after}
		assert.Equal(t, want, listAll(t, st, in), after)
	}

	in := &storage.ListObjectsInput{Bucket: &bucket, StartAfter: new("a/1"), Delimiter: new("/")}
	assert.Equal(t, []string{"a/*", "b", "c"}, listAll(t, st, in))
}

// A MaxKeys out of its bounds, an empty Delimiter, or an invalid Prefix
// or StartAfter is ErrInvalidArgument; an empty or forged token is
// ErrInvalidToken.
func listArguments(t *testing.T, s Subject) {
	t.Helper()

	st, bucket := setup(t, s)
	long := strings.Repeat("k", 1025)

	for want, in := range map[string]*storage.ListObjectsInput{
		"invalid argument: MaxKeys is not 1 to 1000":  {MaxKeys: new(int32(0))},
		"invalid argument: MaxKeys is not 1 to 1000 ": {MaxKeys: new(int32(1001))},
		"invalid argument: Prefix is not valid UTF-8 of at most 1024 bytes": {
			Prefix: &long,
		},
		"invalid argument: Delimiter is not valid UTF-8 of at most 1024 bytes": {
			Delimiter: new("\xff"),
		},
		"invalid argument: StartAfter is not valid UTF-8 of at most 1024 bytes": {
			StartAfter: new("\xff"),
		},
		"invalid argument: Delimiter is empty": {Delimiter: new("")},
		"invalid continuation token: empty":    {ContinuationToken: new("")},
	} {
		in.Bucket = &bucket

		_, err := st.ListObjects(t.Context(), in)
		require.EqualError(t, err, errText(s, opListObjects, strings.TrimRight(want, " ")))
	}

	_, err := st.ListObjects(t.Context(), &storage.ListObjectsInput{
		Bucket:            &bucket,
		ContinuationToken: new("AAAA"),
	})
	require.ErrorIs(t, err, storage.ErrInvalidToken)
}

// A ListObjects token is refused for another bucket, Prefix, Delimiter or
// StartAfter.
func listTokenBound(t *testing.T, s Subject) {
	t.Helper()

	st, bucket := setup(t, s)
	other := newBucketName()

	require.NoError(t, st.CreateBucket(t.Context(), &storage.CreateBucketInput{Bucket: other}))

	for _, k := range []string{"a1", "a2"} {
		put(t, st, bucket, k, "v")
		put(t, st, other, k, "v")
	}

	out, err := st.ListObjects(
		t.Context(),
		&storage.ListObjectsInput{Bucket: &bucket, MaxKeys: new(int32(1))},
	)
	require.NoError(t, err)
	require.NotEmpty(t, out.NextContinuationToken)

	token := &out.NextContinuationToken

	for _, in := range []*storage.ListObjectsInput{
		{Bucket: &other},
		{Bucket: &bucket, Prefix: new("a")},
		{Bucket: &bucket, Delimiter: new("/")},
		{Bucket: &bucket, StartAfter: new("a")},
	} {
		in.ContinuationToken = token

		_, err = st.ListObjects(t.Context(), in)
		require.ErrorIs(t, err, storage.ErrInvalidToken)
	}
}
