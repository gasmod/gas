package storage_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/storage"
)

// Survey 2.4: dir stored metadata and header values as given, CR and LF
// included, while S3 refused them after three attempts or mangled a value
// that is not ASCII. Every backend now checks them by the same rules
// before any I/O.
func TestFindings_MetadataAndHeadersCheckedBeforeIO(t *testing.T) {
	t.Parallel()

	for _, in := range []*storage.PutObjectInput{
		{Key: "k", Metadata: map[string]string{"n": "a\r\nX: y"}},
		{Key: "k", Metadata: map[string]string{"n": "café"}},
		{Key: "k", Metadata: map[string]string{"x y": "v"}},
		{Key: "k", Metadata: map[string]string{"n": strings.Repeat("v", 3<<10)}},
		{Key: "k", ContentType: new("text/plain\r\nX: y")},
	} {
		_, err := storage.CheckPut(in, "photos")
		require.True(
			t,
			errors.Is(err, storage.ErrInvalidMetadata) || errors.Is(err, storage.ErrInvalidHeader),
		)
	}
}

// Survey 2.5: a malformed Range was ignored and the whole object
// returned. It is now ErrInvalidRange, before any I/O.
func TestFindings_MalformedRangeRefused(t *testing.T) {
	t.Parallel()

	_, err := storage.CheckRead(
		&storage.HeadObjectInput{Key: "k", Range: new("bytes=5-1")},
		"photos",
	)
	require.ErrorIs(t, err, storage.ErrInvalidRange)
}

// Survey 1: three sentinels said "object missing" (ErrKeyNotFound,
// ErrNotFound, and ErrBucketNotFound from HeadObject on dir). One,
// ErrObjectNotFound, now does, and a missing bucket is wrapped beside it.
func TestFindings_OneObjectNotFound(t *testing.T) {
	t.Parallel()

	err := errors.Join(storage.ErrObjectNotFound, storage.ErrBucketNotFound)

	require.ErrorIs(t, err, storage.ErrObjectNotFound)
	require.NotErrorIs(t, storage.ErrObjectNotFound, storage.ErrBucketNotFound)
	require.NotErrorIs(t, storage.ErrVersionNotFound, storage.ErrObjectNotFound)
}

// Survey 1: dir sniffed a Content-Type and s3 sent none, so the two read
// back different types. A write without one is now DefaultContentType.
func TestFindings_DefaultContentType(t *testing.T) {
	t.Parallel()

	req, err := storage.CheckPut(
		&storage.PutObjectInput{Key: "k", Body: strings.NewReader("<html>")},
		"photos",
	)
	require.NoError(t, err)
	assert.Equal(t, "application/octet-stream", req.ContentType)
}

// Survey 1 and 2.14: a presigned upload without a ContentLength let a
// client upload any size. It now needs one.
func TestFindings_PresignUploadNeedsLength(t *testing.T) {
	t.Parallel()

	_, err := storage.CheckPresignUpload(&storage.PresignUploadURLInput{Key: "k"}, "photos")
	require.EqualError(t, err, "invalid argument: ContentLength is required")
}
