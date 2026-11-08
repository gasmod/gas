package s3_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/storage"
	s3store "github.com/gasmod/gas/storage/s3"
)

// errZqx is an error whose text the store must not quote.
var errZqx = errors.New("zqx")

// Expires is the URL's signing time, its X-Amz-Date, plus the TTL.
func TestPresign_Expires(t *testing.T) {
	t.Parallel()

	sg := openSigner(t, newFake(t).config("b-1"))

	out, err := sg.PresignDownloadURL(t.Context(), &storage.PresignDownloadURLInput{
		BaseObjectInput: keyK,
		TTL:             new(90 * time.Second),
	})
	require.NoError(t, err)

	u, err := url.Parse(out.URL)
	require.NoError(t, err)

	signed, err := time.Parse(amzDate, u.Query().Get("X-Amz-Date"))
	require.NoError(t, err)
	assert.Equal(t, signed.Add(90*time.Second), out.Expires)
	assert.Equal(t, "90", u.Query().Get("X-Amz-Expires"))
}

// A download signs its Range, Version and response header fields, and an
// upload its header fields, metadata and condition, which S3 holds the
// request to.
func TestPresign_Signed(t *testing.T) {
	t.Parallel()

	f := newFake(t)
	p := f.withBucket(t, "b-1")
	sg := openSigner(t, f.config("b-1"))
	out := putString(t, p, "k", "0123456789")

	down, err := sg.PresignDownloadURL(t.Context(), &storage.PresignDownloadURLInput{
		BaseObjectInput:         keyK,
		Range:                   new("bytes=2-4"),
		Version:                 &out.Version,
		ResponseCacheControl:    new("no-store"),
		ResponseContentEncoding: new("identity"),
		ResponseContentLanguage: new("en"),
		ResponseExpires:         new("Wed, 21 Oct 2015 07:28:00 GMT"),
	})
	require.NoError(t, err)

	req, err := http.NewRequestWithContext(t.Context(), down.Method, down.URL, nil)
	require.NoError(t, err)

	for name, values := range down.SignedHeader {
		if name != "Host" {
			req.Header[name] = values
		}
	}

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	assert.Equal(t, http.StatusPartialContent, resp.StatusCode)
	assert.Equal(t, "234", string(body))
	assert.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
	assert.Equal(t, "Wed, 21 Oct 2015 07:28:00 GMT", resp.Header.Get("Expires"))

	up, err := sg.PresignUploadURL(t.Context(), &storage.PresignUploadURLInput{
		BaseObjectInput:    keyK,
		Metadata:           map[string]string{"Author": "Ann"},
		CacheControl:       new("max-age=60"),
		ContentDisposition: new("inline"),
		ContentEncoding:    new("identity"),
		ContentLanguage:    new("en"),
		Expires:            new("Wed, 21 Oct 2015 07:28:00 GMT"),
		IfNoneMatch:        new("*"),
		ContentLength:      new(int64(3)),
	})
	require.NoError(t, err)
	assert.Equal(t, "*", up.SignedHeader.Get("If-None-Match"))
	assert.Equal(t, http.StatusPreconditionFailed, sendPresigned(t, up, "new"))

	up, err = sg.PresignUploadURL(t.Context(), &storage.PresignUploadURLInput{
		BaseObjectInput: keyK,
		IfMatch:         new(md5Hex([]byte("0123456789"))),
		ContentLength:   new(int64(3)),
	})
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, sendPresigned(t, up, "new"))
}

// blockingPresign is a PresignAPI whose calls block until their context
// ends, or that fail with err.
type blockingPresign struct {
	// err, when set, is what the calls return at once.
	err error
}

// PresignPutObject blocks until ctx ends, or returns err.
func (b blockingPresign) PresignPutObject(
	ctx context.Context,
	_ *awss3.PutObjectInput,
	_ ...func(*awss3.PresignOptions),
) (*v4.PresignedHTTPRequest, error) {
	if b.err != nil {
		return nil, b.err
	}

	<-ctx.Done()

	return nil, ctx.Err()
}

// PresignGetObject blocks until ctx ends, or returns err.
func (b blockingPresign) PresignGetObject(
	ctx context.Context,
	_ *awss3.GetObjectInput,
	_ ...func(*awss3.PresignOptions),
) (*v4.PresignedHTTPRequest, error) {
	return b.PresignPutObject(ctx, nil)
}

// A presigner's call ends with its context, a Close whose context ends
// cancels the calls in flight, and an error of signing is classified.
func TestPresign_Lifecycle(t *testing.T) {
	t.Parallel()

	in := &storage.PresignDownloadURLInput{
		Bucket: new("b-1"), Key: "k",
	}

	failing, _ := buildSigner(t, s3store.WithPresignerConfig(s3store.DefaultConfig()),
		s3store.WithPresignClient(blockingPresign{err: errZqx}))
	require.NoError(t, failing.Init(t.Context()))
	assert.Nil(t, failing.PresignClient())

	_, err := failing.PresignDownloadURL(t.Context(), in)
	require.ErrorIs(t, err, storage.ErrBackend)
	require.EqualError(t, err, "gas/storage/s3/presigner: presign download: failed")

	sg, log := buildSigner(t, s3store.WithPresignerConfig(s3store.DefaultConfig()),
		s3store.WithPresignClient(blockingPresign{}))
	require.NoError(t, sg.Init(t.Context()))

	ctx, cancel := context.WithCancel(t.Context())

	time.AfterFunc(100*time.Millisecond, cancel)

	_, err = sg.PresignDownloadURL(ctx, in)
	require.ErrorIs(t, err, context.Canceled)
	require.EqualError(t, err, "gas/storage/s3/presigner: presign download: context canceled")

	got := make(chan error, 1)

	go func() {
		_, inFlight := sg.PresignDownloadURL(context.Background(), in)
		got <- inFlight
	}()

	time.Sleep(100 * time.Millisecond)

	closeCtx, cancelClose := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancelClose()

	err = sg.Close(closeCtx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.EqualError(t, err, "gas/storage/s3/presigner: close: context deadline exceeded")
	require.EqualError(t, <-got, "gas/storage/s3/presigner: presign download: service closed")

	entries := log.entries()
	assert.Equal(t, "service closed, calls in flight canceled", entries[len(entries)-1].msg)
	assert.True(t, strings.HasPrefix(entries[0].msg, "service initialized"))
	assert.Equal(t, true, entries[0].fields["client"])
}
