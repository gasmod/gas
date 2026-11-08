package storage_test

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/storage"
)

// A key is 1 to 1024 bytes of valid UTF-8 without a control character,
// whose last element is not `.`; `/` is the only separator, so a
// backslash is an ordinary byte.
func TestCheckKey(t *testing.T) {
	t.Parallel()

	for _, key := range []string{
		"k", "a/b", `a\.`, "../x", "a//b", "/lead", "trail/", "a/./b", "é", "..", "a/b/..",
		strings.Repeat("k", storage.MaxKeyLength),
	} {
		require.NoError(t, storage.CheckKey(key), key)
	}

	for _, key := range []string{
		"", strings.Repeat("k", storage.MaxKeyLength+1), "\xff", "a\x00", "\t", "\x7f", "a/.", ".", "./", "a/./", "a/..",
	} {
		require.ErrorIs(t, storage.CheckKey(key), storage.ErrInvalidKey, key)
	}
}

// A bucket name keeps S3's rules for a general purpose bucket.
func TestCheckBucket(t *testing.T) {
	t.Parallel()

	for _, name := range []string{
		"abc", "a.b-c", "1bucket", strings.Repeat("a", storage.MaxBucketLength), "10.0.0", "1.2.3.4.5", "a.1.2.3",
		// S3's reserved starts and ends are left to the backend.
		"xn--ab", "sthree-ab", "amzn-s3-demo-ab", "ab-s3alias", "ab--ol-s3", "ab.mrap", "ab--x-s3", "ab--table-s3",
		"ab-an",
	} {
		require.NoError(t, storage.CheckBucket(name), name)
	}

	for _, name := range []string{
		"", "ab", strings.Repeat("a", storage.MaxBucketLength+1), "ABC", "a_b", "a b", "ébc", ".ab", "ab.",
		"-ab", "ab-", "a..b", "1.2.3.4",
	} {
		require.ErrorIs(t, storage.CheckBucket(name), storage.ErrInvalidBucket, name)
	}
}

// A nil bucket is the configured one; a given one, even empty, is itself.
func TestResolveBucket(t *testing.T) {
	t.Parallel()

	b, err := storage.ResolveBucket(nil, "photos")
	require.NoError(t, err)
	assert.Equal(t, "photos", b)

	b, err = storage.ResolveBucket(new("other"), "photos")
	require.NoError(t, err)
	assert.Equal(t, "other", b)

	_, err = storage.ResolveBucket(new(""), "photos")
	require.ErrorIs(t, err, storage.ErrInvalidBucket)

	_, err = storage.ResolveBucket(nil, "")
	require.EqualError(t, err, "invalid bucket: none given and none configured")

	_, err = storage.ResolveBucket(nil, "BAD")
	require.ErrorIs(t, err, storage.ErrInvalidBucket)
}

// CheckObject checks the key before the bucket.
func TestCheckObject(t *testing.T) {
	t.Parallel()

	ref, err := storage.CheckObject(&storage.BaseObjectInput{Key: "k"}, "photos")
	require.NoError(t, err)
	assert.Equal(t, storage.ObjectRef{Bucket: "photos", Key: "k"}, ref)

	_, err = storage.CheckObject(&storage.BaseObjectInput{Bucket: new("BAD")}, "photos")
	require.ErrorIs(t, err, storage.ErrInvalidKey)

	_, err = storage.CheckObject(&storage.BaseObjectInput{Bucket: new("BAD"), Key: "k"}, "photos")
	require.ErrorIs(t, err, storage.ErrInvalidBucket)
}

// CheckRead parses the Range and checks the PartNumber and the Version,
// in that order, after the key and the bucket.
func TestCheckRead(t *testing.T) {
	t.Parallel()

	req, err := storage.CheckRead(&storage.HeadObjectInput{
		Key:        "k",
		PartNumber: new(int32(storage.MaxPartNumber)),
		Version:    new("v1"),
	}, "photos")
	require.NoError(t, err)
	assert.Equal(t, int32(storage.MaxPartNumber), req.PartNumber)
	assert.Equal(t, storage.ObjectRef{Bucket: "photos", Key: "k", Version: "v1"}, req.ObjectRef)
	assert.Nil(t, req.Range)

	req, err = storage.CheckRead(
		&storage.HeadObjectInput{Key: "k", Range: new("bytes=1-2")},
		"photos",
	)
	require.NoError(t, err)
	require.NotNil(t, req.Range)
	assert.Equal(t, "bytes=1-2", req.Range.String())

	for in, want := range map[*storage.HeadObjectInput]error{
		{Key: ""}:                   storage.ErrInvalidKey,
		{Key: "k", Bucket: new("")}: storage.ErrInvalidBucket,
		{Key: "k", Range: new("x"), Version: new("")}:           storage.ErrInvalidRange,
		{Key: "k", PartNumber: new(int32(-1))}:                  storage.ErrInvalidArgument,
		{Key: "k", Version: new(""), PartNumber: new(int32(0))}: storage.ErrInvalidArgument,
	} {
		_, err = storage.CheckRead(in, "photos")
		require.ErrorIs(t, err, want)
	}

	_, err = storage.CheckRead(&storage.HeadObjectInput{Key: "k", Version: new("")}, "photos")
	require.EqualError(t, err, "invalid argument: Version is empty")
}

// CheckDelete checks the key, the bucket and the Version.
func TestCheckDelete(t *testing.T) {
	t.Parallel()

	ref, err := storage.CheckDelete(
		&storage.DeleteObjectInput{Key: "k", Version: new("v")},
		"photos",
	)
	require.NoError(t, err)
	assert.Equal(t, storage.ObjectRef{Bucket: "photos", Key: "k", Version: "v"}, ref)

	_, err = storage.CheckDelete(&storage.DeleteObjectInput{Key: "k", Version: new("")}, "photos")
	require.ErrorIs(t, err, storage.ErrInvalidArgument)

	_, err = storage.CheckDelete(&storage.DeleteObjectInput{}, "photos")
	require.ErrorIs(t, err, storage.ErrInvalidKey)
}

// CheckPut returns the checked write: a copy of the metadata in lower
// case, the header values with the default Content-Type, the condition,
// and a body held to the ContentLength.
func TestCheckPut(t *testing.T) {
	t.Parallel()

	md := map[string]string{"Author": "Ann"}
	in := &storage.PutObjectInput{
		Key:           "k",
		Body:          strings.NewReader("abc"),
		ContentLength: new(int64(3)),
		Metadata:      md,
		IfMatch:       new(`"etag"`),
	}

	req, err := storage.CheckPut(in, "photos")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"author": "Ann"}, req.Metadata)
	assert.Equal(t, storage.DefaultContentType, req.ContentType)
	assert.Equal(t, "etag", req.IfMatch)
	assert.False(t, req.IfNoneMatch)
	assert.Equal(t, int64(3), req.ContentLength)
	assert.Equal(t, map[string]string{"Author": "Ann"}, md)

	req, err = storage.CheckPut(&storage.PutObjectInput{Key: "k", IfNoneMatch: new("*")}, "photos")
	require.NoError(t, err)
	assert.True(t, req.IfNoneMatch)
	assert.Equal(t, int64(-1), req.ContentLength)
	require.NotNil(t, req.Body)
	assert.Nil(t, req.Metadata)

	for in, want := range map[*storage.PutObjectInput]error{
		{}:                           storage.ErrInvalidKey,
		{Key: "k", Bucket: new("A")}: storage.ErrInvalidBucket,
		{Key: "k", ContentLength: new(int64(-1)), ContentType: new("")}:       storage.ErrInvalidArgument,
		{Key: "k", ContentType: new(""), Metadata: map[string]string{"": ""}}: storage.ErrInvalidHeader,
		{Key: "k", Metadata: map[string]string{"": ""}, IfMatch: new("")}:     storage.ErrInvalidMetadata,
		{Key: "k", IfMatch: new("")}:                                          storage.ErrInvalidCondition,
	} {
		_, err = storage.CheckPut(in, "photos")
		require.ErrorIs(t, err, want)
	}
}

// CheckHeaders checks the fields in their order and names the one that
// breaks a rule; a nil ContentType is the default.
func TestCheckHeaders(t *testing.T) {
	t.Parallel()

	h := storage.ObjectHeaders{
		CacheControl:       new("no-cache"),
		ContentDisposition: new("inline"),
		ContentEncoding:    new("gzip"),
		ContentLanguage:    new("en"),
		ContentType:        new("text/plain"),
		Expires:            new("Wed, 21 Oct 2015 07:28:00 GMT"),
	}

	v, err := storage.CheckHeaders(&h)
	require.NoError(t, err)
	assert.Equal(t, storage.HeaderValues{
		CacheControl:       "no-cache",
		ContentDisposition: "inline",
		ContentEncoding:    "gzip",
		ContentLanguage:    "en",
		ContentType:        "text/plain",
		Expires:            "Wed, 21 Oct 2015 07:28:00 GMT",
	}, v)

	both := storage.ObjectHeaders{ContentType: new("\n"), CacheControl: new("\n")}

	_, err = storage.CheckHeaders(&both)
	require.EqualError(t, err,
		"invalid header: ContentType holds a character other than printable ASCII, space and tab")

	_, err = storage.CheckHeaders(&storage.ObjectHeaders{ContentDisposition: new(" x")})
	require.EqualError(
		t,
		err,
		"invalid header: ContentDisposition starts or ends with a space or a tab",
	)

	_, err = storage.CheckHeaders(&storage.ObjectHeaders{ContentEncoding: new("")})
	require.EqualError(t, err, "invalid header: ContentEncoding is empty")

	v, err = storage.CheckHeaders(&storage.ObjectHeaders{ContentType: new("a\tb")})
	require.NoError(t, err)
	assert.Equal(t, "a\tb", v.ContentType)

	// Each field holds at most MaxHeaderSize bytes, so a stored object's
	// metadata file has a size bound.
	_, err = storage.CheckHeaders(
		&storage.ObjectHeaders{CacheControl: new(strings.Repeat("a", storage.MaxHeaderSize))},
	)
	require.NoError(t, err)

	_, err = storage.CheckHeaders(
		&storage.ObjectHeaders{CacheControl: new(strings.Repeat("a", storage.MaxHeaderSize+1))},
	)
	require.EqualError(t, err, "invalid header: CacheControl is longer than 1024 bytes")
}

// CheckResponseHeaders checks the response fields as CheckHeaders does,
// with no default.
func TestCheckResponseHeaders(t *testing.T) {
	t.Parallel()

	v, err := storage.CheckResponseHeaders(&storage.ResponseHeaders{})
	require.NoError(t, err)
	assert.Equal(t, storage.HeaderValues{}, v)

	v, err = storage.CheckResponseHeaders(&storage.ResponseHeaders{
		ResponseCacheControl:       new("a"),
		ResponseContentDisposition: new("b"),
		ResponseContentEncoding:    new("c"),
		ResponseContentLanguage:    new("d"),
		ResponseContentType:        new("e"),
		ResponseExpires:            new("Wed, 21 Oct 2015 07:28:00 GMT"),
	})
	require.NoError(t, err)
	assert.Equal(t, storage.HeaderValues{
		CacheControl: "a", ContentDisposition: "b", ContentEncoding: "c", ContentLanguage: "d",
		ContentType: "e", Expires: "Wed, 21 Oct 2015 07:28:00 GMT",
	}, v)

	_, err = storage.CheckResponseHeaders(&storage.ResponseHeaders{ResponseExpires: new("x")})
	require.EqualError(t, err, "invalid header: ResponseExpires is not an IMF-fixdate")
}

// ParseExpires reads only an IMF-fixdate exactly as http.TimeFormat
// writes it.
func TestParseExpires(t *testing.T) {
	t.Parallel()

	at, err := storage.ParseExpires("Wed, 21 Oct 2015 07:28:00 GMT")
	require.NoError(t, err)
	assert.Equal(t, time.Date(2015, 10, 21, 7, 28, 0, 0, time.UTC), at)

	for _, s := range []string{
		"", "tomorrow", "Thu, 21 Oct 2015 07:28:00 GMT", "Wednesday, 21-Oct-15 07:28:00 GMT",
		"Wed Oct 21 07:28:00 2015", "Wed, 21 Oct 2015 07:28:00 UTC", " Wed, 21 Oct 2015 07:28:00 GMT",
	} {
		_, err = storage.ParseExpires(s)
		require.EqualError(t, err, "is not an IMF-fixdate", s)
	}
}

// CheckMetadata returns a copy with the names in lower case, refuses what
// breaks a rule, and counts the size over the names and values.
func TestCheckMetadata(t *testing.T) {
	t.Parallel()

	out, err := storage.CheckMetadata(nil)
	require.NoError(t, err)
	assert.Nil(t, out)

	out, err = storage.CheckMetadata(map[string]string{})
	require.NoError(t, err)
	assert.Nil(t, out)

	in := map[string]string{"A-B": "x y\tz", "!#$%&'*+-.^_`|~0aZ": "~"}

	out, err = storage.CheckMetadata(in)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"a-b": "x y\tz", "!#$%&'*+-.^_`|~0az": "~"}, out)

	out["a-b"] = "changed"

	assert.Equal(t, "x y\tz", in["A-B"])

	_, err = storage.CheckMetadata(
		map[string]string{"n": strings.Repeat("v", storage.MaxMetadataSize-1)},
	)
	require.NoError(t, err)

	for _, name := range []string{"", "a b", "a:b", "a\"b", "é", "a(b)", "a/b", "a@b", "a\x00"} {
		_, err = storage.CheckMetadata(map[string]string{name: "v"})
		require.EqualError(t, err, "invalid metadata: a name is not an HTTP token", name)
	}
}

// CheckCondition returns the IfMatch ETag without its quotes, and refuses
// a condition that cannot be evaluated.
func TestCheckCondition(t *testing.T) {
	t.Parallel()

	for given, want := range map[string]string{
		"abc": "abc", `"abc"`: "abc", `"a-b_c"`: "a-b_c", `W/"x"`: `W/"x"`, `"`: `"`,
	} {
		etag, none, err := storage.CheckCondition(&storage.WriteCondition{IfMatch: &given})
		if want == `W/"x"` || want == `"` {
			require.ErrorIs(t, err, storage.ErrInvalidCondition, given)

			continue
		}

		require.NoError(t, err, given)
		assert.Equal(t, want, etag)
		assert.False(t, none)
	}

	etag, none, err := storage.CheckCondition(&storage.WriteCondition{})
	require.NoError(t, err)
	assert.Empty(t, etag)
	assert.False(t, none)

	_, none, err = storage.CheckCondition(&storage.WriteCondition{IfNoneMatch: new("*")})
	require.NoError(t, err)
	assert.True(t, none)
}

// CheckListObjects returns the listing with its defaults, and checks its
// arguments in order.
func TestCheckListObjects(t *testing.T) {
	t.Parallel()

	req, err := storage.CheckListObjects(&storage.ListObjectsInput{}, "photos")
	require.NoError(t, err)
	assert.Equal(t, storage.ListObjectsRequest{Bucket: "photos", MaxKeys: storage.MaxListKeys}, req)

	req, err = storage.CheckListObjects(&storage.ListObjectsInput{
		Bucket:            new("other"),
		ContinuationToken: new("t"),
		Delimiter:         new("/"),
		MaxKeys:           new(int32(1)),
		Prefix:            new("p"),
		StartAfter:        new("s"),
	}, "photos")
	require.NoError(t, err)
	assert.Equal(t, storage.ListObjectsRequest{
		Bucket:            "other",
		Prefix:            "p",
		Delimiter:         "/",
		StartAfter:        "s",
		ContinuationToken: "t",
		MaxKeys:           1,
	}, req)

	_, err = storage.CheckListObjects(&storage.ListObjectsInput{MaxKeys: new(int32(0))}, "")
	require.ErrorIs(t, err, storage.ErrInvalidBucket)

	_, err = storage.CheckListObjects(&storage.ListObjectsInput{
		Prefix: new(strings.Repeat("p", storage.MaxKeyLength+1)), Delimiter: new(""),
	}, "photos")
	require.EqualError(t, err, "invalid argument: Prefix is not valid UTF-8 of at most 1024 bytes")

	_, err = storage.CheckListObjects(
		&storage.ListObjectsInput{Delimiter: new(""), ContinuationToken: new("")},
		"photos",
	)
	require.EqualError(t, err, "invalid argument: Delimiter is empty")

	_, err = storage.CheckListObjects(
		&storage.ListObjectsInput{ContinuationToken: new("")},
		"photos",
	)
	require.ErrorIs(t, err, storage.ErrInvalidToken)
}

// CheckListBuckets returns the listing with its defaults.
func TestCheckListBuckets(t *testing.T) {
	t.Parallel()

	req, err := storage.CheckListBuckets(&storage.ListBucketsInput{})
	require.NoError(t, err)
	assert.Equal(t, storage.ListBucketsRequest{MaxBuckets: storage.MaxListBuckets}, req)

	req, err = storage.CheckListBuckets(&storage.ListBucketsInput{
		ContinuationToken: new(
			"t",
		),
		MaxBuckets: new(int32(storage.MaxListBuckets)),
		Prefix:     new("p"),
	})
	require.NoError(t, err)
	assert.Equal(
		t,
		storage.ListBucketsRequest{Prefix: "p", ContinuationToken: "t", MaxBuckets: 10000},
		req,
	)

	_, err = storage.CheckListBuckets(&storage.ListBucketsInput{MaxBuckets: new(int32(10001))})
	require.ErrorIs(t, err, storage.ErrInvalidArgument)

	_, err = storage.CheckListBuckets(&storage.ListBucketsInput{Prefix: new("\xff")})
	require.ErrorIs(t, err, storage.ErrInvalidArgument)
}

// CheckPresignTTL defaults, bounds and truncates the TTL.
func TestCheckPresignTTL(t *testing.T) {
	t.Parallel()

	for given, want := range map[time.Duration]time.Duration{
		0:                       storage.DefaultPresignTTL,
		time.Second:             time.Second,
		1999 * time.Millisecond: time.Second,
		storage.MaxPresignTTL:   storage.MaxPresignTTL,
	} {
		got, err := storage.CheckPresignTTL(&given)
		require.NoError(t, err)
		assert.Equal(t, want, got)
	}

	got, err := storage.CheckPresignTTL(nil)
	require.NoError(t, err)
	assert.Equal(t, storage.DefaultPresignTTL, got)

	for _, ttl := range []time.Duration{-time.Second, time.Nanosecond, storage.MaxPresignTTL + 1} {
		_, err = storage.CheckPresignTTL(&ttl)
		require.ErrorIs(t, err, storage.ErrInvalidArgument)
	}
}

// CheckPresignUpload checks the key, the bucket, the ContentLength, the
// write, then the TTL, and returns no body.
func TestCheckPresignUpload(t *testing.T) {
	t.Parallel()

	req, err := storage.CheckPresignUpload(&storage.PresignUploadURLInput{
		Key:           "k",
		ContentLength: new(int64(storage.MaxPresignUploadSize)),
		TTL:           new(time.Minute),
		ContentType:   new("text/plain"),
	}, "photos")
	require.NoError(t, err)
	assert.Nil(t, req.Body)
	assert.Equal(t, int64(storage.MaxPresignUploadSize), req.ContentLength)
	assert.Equal(t, time.Minute, req.TTL)
	assert.Equal(t, "text/plain", req.ContentType)

	for in, want := range map[*storage.PresignUploadURLInput]error{
		{}:                               storage.ErrInvalidKey,
		{Key: "k", Bucket: new("A")}:     storage.ErrInvalidBucket,
		{Key: "k", ContentType: new("")}: storage.ErrInvalidArgument,
		{Key: "k", ContentLength: new(int64(0)), ContentType: new("")}: storage.ErrInvalidHeader,
		{Key: "k", ContentLength: new(int64(0)), TTL: new(-time.Hour)}: storage.ErrInvalidArgument,
	} {
		_, err = storage.CheckPresignUpload(in, "photos")
		require.ErrorIs(t, err, want)
	}
}

// CheckPresignDownload checks the read, the response header fields, then
// the TTL.
func TestCheckPresignDownload(t *testing.T) {
	t.Parallel()

	req, err := storage.CheckPresignDownload(&storage.PresignDownloadURLInput{
		Key:                 "k",
		ResponseContentType: new("text/plain"),
	}, "photos")
	require.NoError(t, err)
	assert.Equal(t, "text/plain", req.Response.ContentType)
	assert.Equal(t, storage.DefaultPresignTTL, req.TTL)
	assert.Equal(t, "photos", req.Bucket)

	for in, want := range map[*storage.PresignDownloadURLInput]error{
		{}: storage.ErrInvalidKey,
		{Key: "k", ResponseContentType: new(""), TTL: new(-time.Hour)}: storage.ErrInvalidHeader,
		{Key: "k", TTL: new(-time.Hour)}:                               storage.ErrInvalidArgument,
	} {
		_, err = storage.CheckPresignDownload(in, "photos")
		require.ErrorIs(t, err, want)
	}
}

// ruleText is the shape of every error of the rules: a sentinel's text
// and a reason, which never quotes the input.
var ruleText = regexp.MustCompile(
	`^(invalid (key|bucket|metadata|header|condition|range|argument)|` +
		`invalid continuation token|content length mismatch|range not satisfiable|read body)(: [ -~]+)?$`)

// requireRuleError fails t unless err is nil or has the shape of an error
// of the rules, and does not hold input, which is long or distinctive
// enough to be told apart from the reasons.
func requireRuleError(t *testing.T, err error, input string) {
	t.Helper()

	if err == nil {
		return
	}

	require.Regexp(t, ruleText, err.Error())

	if len(input) >= 8 {
		require.NotContains(t, err.Error(), input)
	}
}

// CheckKey and CheckBucket are deterministic, never panic, return nil or
// their sentinel, never quote their input, and refuse every key or name
// that breaks a documented rule.
func FuzzCheckKey(f *testing.F) {
	for _, seed := range []string{"", "k", "a/.", "\xff", "a\x00b", "../../etc/passwd", "hunter2-secret-key"} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, s string) {
		keyErr := storage.CheckKey(s)
		require.Equal(t, keyErr, storage.CheckKey(s))
		requireRuleError(t, keyErr, s)

		if keyErr != nil {
			require.ErrorIs(t, keyErr, storage.ErrInvalidKey)
		} else {
			require.True(t, s != "" && len(s) <= storage.MaxKeyLength && utf8.ValidString(s))
			require.False(
				t,
				strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }),
			)
		}

		bucketErr := storage.CheckBucket(s)
		requireRuleError(t, bucketErr, s)

		if bucketErr != nil {
			require.ErrorIs(t, bucketErr, storage.ErrInvalidBucket)
		} else {
			require.Regexp(t, `^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`, s)
			require.NotContains(t, s, "..")
		}
	})
}

// CheckMetadata is deterministic, never panics, returns nil or
// ErrInvalidMetadata, never quotes a name or a value, and returns only
// lower case names whose entries keep the rules.
func FuzzCheckMetadata(f *testing.F) {
	f.Add("Author", "Ann", "x", "y")
	f.Add("a b", "v", "A", "v")
	f.Add("n", "secret-value\r\n", "N", "v")

	f.Fuzz(func(t *testing.T, k1, v1, k2, v2 string) {
		in := map[string]string{k1: v1, k2: v2}

		out, err := storage.CheckMetadata(in)
		again, againErr := storage.CheckMetadata(in)
		require.Equal(t, out, again)
		require.Equal(t, err, againErr)

		if err != nil {
			require.ErrorIs(t, err, storage.ErrInvalidMetadata)

			for _, s := range []string{k1, v1, k2, v2} {
				requireRuleError(t, err, s)
			}

			return
		}

		size := 0

		for k, v := range out {
			require.Equal(t, strings.ToLower(k), k)
			require.NotEmpty(t, v)
			require.Regexp(t, `^[!-~]([ -~\t]*[!-~])?$`, v)

			size += len(k) + len(v)
		}

		require.LessOrEqual(t, size, storage.MaxMetadataSize)
		require.Len(t, out, len(in))
	})
}

// ParseExpires is deterministic, never panics, and accepts only text
// that http.TimeFormat writes back unchanged.
func FuzzParseExpires(f *testing.F) {
	f.Add("Wed, 21 Oct 2015 07:28:00 GMT")
	f.Add("Thu, 21 Oct 2015 07:28:00 GMT")
	f.Add("Wednesday, 21-Oct-15 07:28:00 GMT")

	f.Fuzz(func(t *testing.T, s string) {
		at, err := storage.ParseExpires(s)
		again, againErr := storage.ParseExpires(s)
		require.Equal(t, at, again)
		require.Equal(t, err, againErr)

		if err != nil {
			require.EqualError(t, err, "is not an IMF-fixdate")

			return
		}

		require.Equal(t, s, at.UTC().Format(http.TimeFormat))
	})
}

// headerError is the error CheckHeaders returns for one field set to s.
func headerError(s string) error {
	_, err := storage.CheckHeaders(&storage.ObjectHeaders{CacheControl: &s})

	return err
}

// A header value keeps the rules exactly when it is not empty, holds only
// printable ASCII, space and tab, and does not start or end with a blank.
func FuzzCheckHeaders(f *testing.F) {
	for _, seed := range []string{"", "a", " a", "a\r\nb", "é", "max-age=60"} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, s string) {
		err := headerError(s)
		valid := regexp.MustCompile(`^[!-~]([ -~\t]*[!-~])?$`).MatchString(s)

		require.Equal(t, valid, err == nil, "%q", s)

		if err != nil {
			require.ErrorIs(t, err, storage.ErrInvalidHeader)
			requireRuleError(t, err, s)
		}
	})
}
