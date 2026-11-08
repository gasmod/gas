//nolint:mnd,goconst // test rules: counts and texts read best where they are checked.
package conformance

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/storage"
)

// inputRules returns the rules of the inputs, checked before any I/O.
func inputRules() map[string]func(*testing.T, Subject) {
	return map[string]func(*testing.T, Subject){
		"InvalidKey":       invalidKey,
		"OddKeys":          oddKeys,
		"InvalidBucket":    invalidBucket,
		"KeyBeforeBucket":  keyBeforeBucket,
		"Metadata":         metadata,
		"InvalidMetadata":  invalidMetadata,
		"Headers":          headers,
		"InvalidRange":     invalidRange,
		"ReadArguments":    readArguments,
		"InvalidCondition": invalidCondition,
	}
}

// A key that breaks a key rule is ErrInvalidKey, from every object
// method.
func invalidKey(t *testing.T, s Subject) {
	t.Helper()

	st, bucket := setup(t, s)

	for key, want := range map[string]string{
		"":                        "empty",
		strings.Repeat("k", 1025): "longer than 1024 bytes",
		"a\xffb":                  "not valid UTF-8",
		"a\x00b":                  "holds a control character",
		"a\tb":                    "holds a control character",
		"a\x7fb":                  "holds a control character",
		"a/.":                     `its last path element is "."`,
		".":                       `its last path element is "."`,
		"./":                      `its last path element is "."`,
	} {
		want = "invalid key: " + want

		_, err := st.PutObject(t.Context(), putIn(bucket, key, "v"))
		requireErr(t, err, storage.ErrInvalidKey, errText(s, opPutObject, want))

		_, err = st.HeadObject(t.Context(), headIn(bucket, key))
		requireErr(t, err, storage.ErrInvalidKey, errText(s, opHeadObject, want))

		_, err = st.GetObject(t.Context(), getIn(bucket, key))
		requireErr(t, err, storage.ErrInvalidKey, errText(s, opGetObject, want))

		err = st.DeleteObject(
			t.Context(),
			&storage.DeleteObjectInput{BaseObjectInput: ref(bucket, key)},
		)
		requireErr(t, err, storage.ErrInvalidKey, errText(s, opDeleteObject, want))
	}
}

// Keys that keep the rules round trip, however odd, as their own
// objects.
func oddKeys(t *testing.T, s Subject) {
	t.Helper()

	st, bucket := setup(t, s)
	keys := []string{
		`a\.`, "../x", "a//b", "/lead", "trail/", "a/./b", "sp ace", "%2e%2e", "q?x#y", "é/ü",
		strings.Repeat("k", 1024),
	}

	for _, key := range keys {
		put(t, st, bucket, key, "body of "+key)
	}

	for _, key := range keys {
		requireBody(t, st, bucket, key, "body of "+key)
	}

	out, err := st.ListObjects(t.Context(), &storage.ListObjectsInput{Bucket: &bucket})
	require.NoError(t, err)
	assert.Len(t, out.Objects, len(keys))
}

// A bucket name that breaks a bucket rule is ErrInvalidBucket, from the
// bucket methods and the object methods.
func invalidBucket(t *testing.T, s Subject) {
	t.Helper()

	st := open(t, s, "")

	for name, want := range map[string]string{
		"ab":                    "not 3 to 63 bytes",
		strings.Repeat("a", 64): "not 3 to 63 bytes",
		"Photos":                "holds a character other than a-z, 0-9, . and -",
		"pho_tos":               "holds a character other than a-z, 0-9, . and -",
		"-photos":               "does not start and end with a letter or a digit",
		"photos.":               "does not start and end with a letter or a digit",
		"pho..tos":              "holds two dots in a row",
		"192.168.5.4":           "shaped like an IPv4 address",
	} {
		want = "invalid bucket: " + want

		err := st.CreateBucket(t.Context(), &storage.CreateBucketInput{Bucket: name})
		requireErr(t, err, storage.ErrInvalidBucket, errText(s, opCreateBucket, want))

		_, err = st.HeadBucket(t.Context(), &storage.HeadBucketInput{Bucket: name})
		requireErr(t, err, storage.ErrInvalidBucket, errText(s, opHeadBucket, want))

		err = st.DeleteBucket(t.Context(), &storage.DeleteBucketInput{Bucket: name})
		requireErr(t, err, storage.ErrInvalidBucket, errText(s, opDeleteBucket, want))

		_, err = st.PutObject(t.Context(), putIn(name, "k", "v"))
		requireErr(t, err, storage.ErrInvalidBucket, errText(s, opPutObject, want))

		_, err = st.ListObjects(t.Context(), &storage.ListObjectsInput{Bucket: &name})
		requireErr(t, err, storage.ErrInvalidBucket, errText(s, opListObjects, want))
	}
}

// The key is checked before the bucket.
func keyBeforeBucket(t *testing.T, s Subject) {
	t.Helper()

	st := open(t, s, "")

	_, err := st.PutObject(t.Context(), putIn("BAD", "", "v"))
	requireErr(t, err, storage.ErrInvalidKey, errText(s, opPutObject, "invalid key: empty"))
}

// Metadata names read back in lower case, and the names and values may
// hold MaxMetadataSize bytes together.
func metadata(t *testing.T, s Subject) {
	t.Helper()

	st, bucket := setup(t, s)
	in := putIn(bucket, "k", "v")

	in.Metadata = map[string]string{
		"Mixed-Case":      "printable ASCII: !#$%&'()*+,-./:;<=>?@[\\]^_`{|}~ and a\ttab",
		"!#$%&'*+-.^_`|~": "token characters",
	}

	used := len("big")
	for k, v := range in.Metadata {
		used += len(k) + len(v)
	}

	in.Metadata["big"] = strings.Repeat("v", storage.MaxMetadataSize-used)

	_, err := st.PutObject(t.Context(), in)
	require.NoError(t, err)

	info, err := st.HeadObject(t.Context(), headIn(bucket, "k"))
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		"mixed-case":      in.Metadata["Mixed-Case"],
		"!#$%&'*+-.^_`|~": "token characters",
		"big":             in.Metadata["big"],
	}, info.Metadata)
}

// Metadata that breaks a rule is ErrInvalidMetadata, and nothing is
// written.
func invalidMetadata(t *testing.T, s Subject) {
	t.Helper()

	st, bucket := setup(t, s)

	for want, md := range map[string]map[string]string{
		"a name is not an HTTP token":   {"a b": "v"},
		"a name is not an HTTP token ":  {"": "v"},
		"a name is not an HTTP token  ": {"é": "v"},
		"a value is empty":              {"n": ""},
		"a value holds a character other than printable ASCII, space and tab":  {"n": "a\r\nb"},
		"a value holds a character other than printable ASCII, space and tab ": {"n": "café"},
		"a value starts or ends with a space or a tab":                         {"n": " v"},
		"two names differ only in case":                                        {"N": "v", "n": "v"},
		"2049 bytes, more than 2048": {
			"n": strings.Repeat("v", storage.MaxMetadataSize),
		},
	} {
		in := putIn(bucket, "k", "v")

		in.Metadata = md

		_, err := st.PutObject(t.Context(), in)
		requireErr(t, err, storage.ErrInvalidMetadata,
			errText(s, opPutObject, "invalid metadata: "+strings.TrimRight(want, " ")))
		requireAbsent(t, st, bucket, "k")
	}
}

// A header field that breaks a rule is ErrInvalidHeader, which names the
// field, and nothing is written.
func headers(t *testing.T, s Subject) {
	t.Helper()

	st, bucket := setup(t, s)

	for want, edit := range map[string]func(*storage.ObjectHeaders){
		"ContentType is empty": func(h *storage.ObjectHeaders) { h.ContentType = new("") },
		"CacheControl holds a character other than printable ASCII, space and tab": func(h *storage.ObjectHeaders) {
			h.CacheControl = new("a\nb")
		},
		"ContentDisposition holds a character other than printable ASCII, space and tab": func(h *storage.ObjectHeaders) {
			h.ContentDisposition = new(`attachment; filename="ü.txt"`)
		},
		"ContentEncoding starts or ends with a space or a tab": func(h *storage.ObjectHeaders) {
			h.ContentEncoding = new("gzip ")
		},
		"ContentLanguage starts or ends with a space or a tab": func(h *storage.ObjectHeaders) {
			h.ContentLanguage = new("\ten")
		},
		"Expires is not an IMF-fixdate": func(h *storage.ObjectHeaders) {
			h.Expires = new("Thu, 21 Oct 2015 07:28:00 GMT")
		},
		"ContentDisposition is longer than 1024 bytes": func(h *storage.ObjectHeaders) {
			h.ContentDisposition = new(strings.Repeat("a", storage.MaxHeaderSize+1))
		},
	} {
		in := putIn(bucket, "k", "v")
		edit(&in.ObjectHeaders)

		_, err := st.PutObject(t.Context(), in)
		requireErr(
			t,
			err,
			storage.ErrInvalidHeader,
			errText(s, opPutObject, "invalid header: "+want),
		)
		requireAbsent(t, st, bucket, "k")
	}

	longest := strings.Repeat("a", storage.MaxHeaderSize)
	in := putIn(bucket, "k", "v")

	in.ContentDisposition = &longest

	_, err := st.PutObject(t.Context(), in)
	require.NoError(t, err)

	info, err := st.HeadObject(t.Context(), headIn(bucket, "k"))
	require.NoError(t, err)
	assert.Equal(t, longest, info.ContentDisposition)
}

// A Range that is not one byte range is ErrInvalidRange, before any I/O.
func invalidRange(t *testing.T, s Subject) {
	t.Helper()

	st := open(t, s, "")
	missing := newBucketName()

	for _, rng := range []string{
		"", "bytes=1-0", "bytes=0-1,3-4", "items=0-1", "bytes=a-b", "bytes=-", "bytes 0-1", "bytes=+1-2",
	} {
		in := getIn(missing, "k")

		in.Range = &rng

		_, err := st.GetObject(t.Context(), in)
		requireErr(
			t,
			err,
			storage.ErrInvalidRange,
			errText(s, opGetObject, "invalid range: not one byte range"),
		)

		_, err = st.HeadObject(t.Context(), &in.HeadObjectInput)
		requireErr(
			t,
			err,
			storage.ErrInvalidRange,
			errText(s, opHeadObject, "invalid range: not one byte range"),
		)
	}
}

// A PartNumber out of its bounds, or with a Range, and an empty Version
// are ErrInvalidArgument, before any I/O.
func readArguments(t *testing.T, s Subject) {
	t.Helper()

	st := open(t, s, "")
	missing := newBucketName()

	for want, edit := range map[string]func(*storage.HeadObjectInput){
		"PartNumber is not 1 to 10000":  func(in *storage.HeadObjectInput) { in.PartNumber = new(int32(0)) },
		"PartNumber is not 1 to 10000 ": func(in *storage.HeadObjectInput) { in.PartNumber = new(int32(10001)) },
		"PartNumber and Range are both set": func(in *storage.HeadObjectInput) {
			in.PartNumber, in.Range = new(int32(1)), new("bytes=0-1")
		},
		"Version is empty": func(in *storage.HeadObjectInput) { in.Version = new("") },
	} {
		in := getIn(missing, "k")
		edit(&in.HeadObjectInput)

		want = "invalid argument: " + strings.TrimRight(want, " ")

		_, err := st.GetObject(t.Context(), in)
		requireErr(t, err, storage.ErrInvalidArgument, errText(s, opGetObject, want))

		_, err = st.HeadObject(t.Context(), &in.HeadObjectInput)
		requireErr(t, err, storage.ErrInvalidArgument, errText(s, opHeadObject, want))
	}

	err := st.DeleteObject(t.Context(), &storage.DeleteObjectInput{
		BaseObjectInput: ref(missing, "k"),
		Version:         new(""),
	})
	requireErr(
		t,
		err,
		storage.ErrInvalidArgument,
		errText(s, opDeleteObject, "invalid argument: Version is empty"),
	)
}

// A condition that cannot be evaluated is ErrInvalidCondition, and
// nothing is written.
func invalidCondition(t *testing.T, s Subject) {
	t.Helper()

	st, bucket := setup(t, s)

	for want, c := range map[string]storage.WriteCondition{
		"IfMatch and IfNoneMatch are both set": {IfMatch: new("abc"), IfNoneMatch: new("*")},
		`IfNoneMatch is not "*"`:               {IfNoneMatch: new("abc")},
		"IfMatch is not one entity tag":        {IfMatch: new("")},
		"IfMatch is not one entity tag ":       {IfMatch: new("*")},
		"IfMatch is not one entity tag  ":      {IfMatch: new(`""`)},
		"IfMatch is not one entity tag   ":     {IfMatch: new("a b")},
		"IfMatch is not one entity tag    ":    {IfMatch: new(`"a", "b"`)},
	} {
		in := putIn(bucket, "k", "v")

		in.WriteCondition = c

		_, err := st.PutObject(t.Context(), in)
		requireErr(t, err, storage.ErrInvalidCondition,
			errText(s, opPutObject, "invalid condition: "+strings.TrimRight(want, " ")))
		requireAbsent(t, st, bucket, "k")
	}
}
