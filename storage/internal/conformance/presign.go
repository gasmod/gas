//nolint:mnd // test rules: counts read best where they are checked.
package conformance

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/storage"
)

// presignRules returns the rules of the presigner, which skip when the
// Subject has none.
func presignRules() map[string]func(*testing.T, Subject) {
	rules := map[string]func(*testing.T, Subject){
		"PresignUpload":           presignUpload,
		"PresignDownload":         presignDownload,
		"PresignWrongLength":      presignWrongLength,
		"PresignExpired":          presignExpired,
		"PresignTTL":              presignTTL,
		"PresignMissing":          presignMissing,
		"PresignUploadArguments":  presignUploadArguments,
		"PresignDownloadArgs":     presignDownloadArguments,
		"PresignLifecycle":        presignLifecycle,
		"PresignCanceled":         presignCanceled,
		"PresignErrorsHoldNoData": presignErrorsHoldNoData,
	}

	out := make(map[string]func(*testing.T, Subject), len(rules))

	for name, rule := range rules {
		out[name] = func(t *testing.T, s Subject) {
			t.Helper()

			if s.NewPresigner == nil {
				t.Skip("the subject has no presigner")
			}

			rule(t, s)
		}
	}

	return out
}

// openSigner returns a new presigner of s, initialized, whose configured
// bucket is bucket, and closes it when t ends.
func openSigner(t *testing.T, s Subject, bucket string) Signer {
	t.Helper()

	sg := buildSigner(t, s, bucket)
	require.NoError(t, sg.Init(t.Context()))

	return sg
}

// buildSigner returns a new presigner of s, not initialized, and closes
// it when t ends.
func buildSigner(t *testing.T, s Subject, bucket string) Signer {
	t.Helper()

	sg := s.NewPresigner(t, bucket)
	t.Cleanup(func() { assert.NoError(t, sg.Close(context.Background())) })

	return sg
}

// signerText returns the text of an error of op of the presigner of s.
func signerText(s Subject, op, rest string) string {
	return s.PresignerName + ": " + op + ": " + rest
}

// send sends the presigned request p with body, as a client does: the
// method, the URL and every signed header field. With ownLength, the
// request carries the length of body instead of the signed one. It
// returns the response's status, header and body.
func send(
	t *testing.T,
	s Subject,
	p *storage.PresignURLResponse,
	body []byte,
	ownLength bool,
) (int, http.Header, string) {
	t.Helper()

	var r io.Reader

	if body != nil {
		r = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(t.Context(), p.Method, p.URL, r)
	require.NoError(t, err)

	for name, values := range p.SignedHeader {
		switch http.CanonicalHeaderKey(name) {
		case "Host":
			req.Host = values[0]
		case "Content-Length":
			if !ownLength {
				req.ContentLength, err = strconv.ParseInt(values[0], 10, 64)
				require.NoError(t, err)
			}
		default:
			req.Header[name] = values
		}
	}

	client := s.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}

	resp, err := client.Do(req)
	require.NoError(t, err)

	defer func() { require.NoError(t, resp.Body.Close()) }()

	got, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	return resp.StatusCode, resp.Header, string(got)
}

// requireExpires fails t unless at is ttl after a time between from and
// to, within a second.
func requireExpires(t *testing.T, at, from, to time.Time, ttl time.Duration) {
	t.Helper()

	require.False(t, at.Before(from.Add(ttl-time.Second)), "%v is too early", at)
	require.False(t, at.After(to.Add(ttl+time.Second)), "%v is too late", at)
}

// A presigned upload stores the body the client sends, with the header
// fields and metadata it was signed for.
func presignUpload(t *testing.T, s Subject) {
	t.Helper()

	st, bucket := setup(t, s)
	sg := openSigner(t, s, bucket)
	before := time.Now()

	p, err := sg.PresignUploadURL(t.Context(), &storage.PresignUploadURLInput{
		Key:           "up/k",
		ContentLength: new(int64(5)),
		TTL:           new(time.Minute),
		ContentType:   new("text/plain"),
		Metadata:      map[string]string{"Author": "Ann"},
	})
	require.NoError(t, err)
	assert.Equal(t, http.MethodPut, p.Method)
	requireExpires(t, p.Expires, before, time.Now(), time.Minute)

	status, _, _ := send(t, s, p, []byte("hello"), false)
	require.Less(t, status, 300, status)

	info, err := st.HeadObject(t.Context(), headIn(bucket, "up/k"))
	require.NoError(t, err)
	assert.Equal(t, int64(5), info.Size)
	assert.Equal(t, md5Hex("hello"), info.ETag)
	assert.Equal(t, "text/plain", info.ContentType)
	assert.Equal(t, map[string]string{"author": "Ann"}, info.Metadata)
}

// A presigned download returns the object, with the response header
// fields it was signed for.
func presignDownload(t *testing.T, s Subject) {
	t.Helper()

	st, bucket := setup(t, s)
	sg := openSigner(t, s, bucket)

	put(t, st, bucket, "dl/k", "hello world")

	in := &storage.PresignDownloadURLInput{GetObjectInput: *getIn(bucket, "dl/k")}

	in.ResponseContentDisposition = new(`attachment; filename="x.txt"`)
	in.ResponseContentType = new("application/x-test")

	p, err := sg.PresignDownloadURL(t.Context(), in)
	require.NoError(t, err)
	assert.Equal(t, http.MethodGet, p.Method)

	status, header, body := send(t, s, p, nil, false)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, "hello world", body)
	assert.Equal(t, `attachment; filename="x.txt"`, header.Get("Content-Disposition"))
	assert.Equal(t, "application/x-test", header.Get("Content-Type"))
}

// The backend refuses an upload whose body is not of the length signed
// for, and stores nothing.
func presignWrongLength(t *testing.T, s Subject) {
	t.Helper()

	st, bucket := setup(t, s)
	sg := openSigner(t, s, bucket)

	p, err := sg.PresignUploadURL(t.Context(), &storage.PresignUploadURLInput{
		BaseObjectInput: ref(bucket, "k"),
		ContentLength:   new(int64(5)),
	})
	require.NoError(t, err)

	for _, body := range []string{"hell", "hello!"} {
		status, _, _ := send(t, s, p, []byte(body), true)
		require.GreaterOrEqual(t, status, 400, body)
		requireAbsent(t, st, bucket, "k")
	}
}

// The backend refuses a request after its URL expired.
func presignExpired(t *testing.T, s Subject) {
	t.Helper()

	st, bucket := setup(t, s)
	sg := openSigner(t, s, bucket)

	p, err := sg.PresignUploadURL(t.Context(), &storage.PresignUploadURLInput{
		BaseObjectInput: ref(bucket, "k"),
		ContentLength:   new(int64(5)),
		TTL:             new(time.Second),
	})
	require.NoError(t, err)

	time.Sleep(time.Until(p.Expires) + 1500*time.Millisecond)

	status, _, _ := send(t, s, p, []byte("hello"), false)
	require.GreaterOrEqual(t, status, 400)
	requireAbsent(t, st, bucket, "k")
}

// A nil or 0 TTL is DefaultPresignTTL, and a TTL is truncated to a whole
// second.
func presignTTL(t *testing.T, s Subject) {
	t.Helper()

	sg := openSigner(t, s, newBucketName())

	for _, c := range []struct {
		ttl  *time.Duration
		want time.Duration
	}{
		{nil, storage.DefaultPresignTTL},
		{new(time.Duration(0)), storage.DefaultPresignTTL},
		{new(2500 * time.Millisecond), 2 * time.Second},
		{new(storage.MaxPresignTTL), storage.MaxPresignTTL},
	} {
		before := time.Now()

		p, err := sg.PresignDownloadURL(t.Context(), &storage.PresignDownloadURLInput{
			Key: "k",
			TTL: c.ttl,
		})
		require.NoError(t, err)
		requireExpires(t, p.Expires, before, time.Now(), c.want)
	}
}

// Signing does no I/O: a request for a missing object is signed, and
// fails only when it is sent.
func presignMissing(t *testing.T, s Subject) {
	t.Helper()

	st, bucket := setup(t, s)
	sg := openSigner(t, s, bucket)

	p, err := sg.PresignDownloadURL(t.Context(), &storage.PresignDownloadURLInput{
		GetObjectInput: *getIn(bucket, "missing"),
	})
	require.NoError(t, err)

	status, _, _ := send(t, s, p, nil, false)
	require.Equal(t, http.StatusNotFound, status)
	requireAbsent(t, st, bucket, "missing")
}

// The inputs of a presigned upload keep the rules of PutObject, need a
// ContentLength, and a TTL within its bounds.
func presignUploadArguments(t *testing.T, s Subject) {
	t.Helper()

	bucket := newBucketName()
	sg := openSigner(t, s, bucket)
	valid := func(edit func(*storage.PresignUploadURLInput)) *storage.PresignUploadURLInput {
		in := &storage.PresignUploadURLInput{
			BaseObjectInput: ref(bucket, "k"),
			ContentLength:   new(int64(5)),
		}
		edit(in)

		return in
	}

	_, err := sg.PresignUploadURL(t.Context(), nil)
	requireErr(t, err, storage.ErrNilInput, signerText(s, opPresignUp, "input is nil"))

	for want, in := range map[string]*storage.PresignUploadURLInput{
		"invalid key: empty": valid(func(in *storage.PresignUploadURLInput) { in.Key = "" }),
		"invalid bucket: holds a character other than a-z, 0-9, . and -": valid(
			func(in *storage.PresignUploadURLInput) { in.Bucket = new("Bad") }),
		"invalid argument: ContentLength is required": valid(
			func(in *storage.PresignUploadURLInput) { in.ContentLength = nil }),
		"invalid argument: ContentLength is not 0 to 5368709120": valid(
			func(in *storage.PresignUploadURLInput) { in.ContentLength = new(int64(-1)) }),
		"invalid argument: ContentLength is not 0 to 5368709120 ": valid(
			func(in *storage.PresignUploadURLInput) {
				in.ContentLength = new(int64(storage.MaxPresignUploadSize + 1))
			}),
		"invalid header: ContentType is empty": valid(
			func(in *storage.PresignUploadURLInput) { in.ContentType = new("") }),
		"invalid metadata: a name is not an HTTP token": valid(
			func(in *storage.PresignUploadURLInput) { in.Metadata = map[string]string{"a b": "v"} }),
		`invalid condition: IfNoneMatch is not "*"`: valid(
			func(in *storage.PresignUploadURLInput) { in.IfNoneMatch = new("x") }),
		"invalid argument: TTL is not 1s to 168h0m0s": valid(
			func(in *storage.PresignUploadURLInput) { in.TTL = new(500 * time.Millisecond) }),
		"invalid argument: TTL is not 1s to 168h0m0s ": valid(
			func(in *storage.PresignUploadURLInput) { in.TTL = new(-time.Second) }),
		"invalid argument: TTL is not 1s to 168h0m0s  ": valid(
			func(in *storage.PresignUploadURLInput) { in.TTL = new(storage.MaxPresignTTL + time.Second) }),
	} {
		_, err = sg.PresignUploadURL(t.Context(), in)
		require.EqualError(t, err, signerText(s, opPresignUp, strings.TrimRight(want, " ")))
	}
}

// The inputs of a presigned download keep the rules of GetObject, and
// their response header fields those of the header fields.
func presignDownloadArguments(t *testing.T, s Subject) {
	t.Helper()

	bucket := newBucketName()
	sg := openSigner(t, s, bucket)
	valid := func(edit func(*storage.PresignDownloadURLInput)) *storage.PresignDownloadURLInput {
		in := &storage.PresignDownloadURLInput{GetObjectInput: *getIn(bucket, "k")}
		edit(in)

		return in
	}

	_, err := sg.PresignDownloadURL(t.Context(), nil)
	requireErr(t, err, storage.ErrNilInput, signerText(s, opPresignDown, "input is nil"))

	for want, in := range map[string]*storage.PresignDownloadURLInput{
		`invalid key: its last path element is "."`: valid(
			func(in *storage.PresignDownloadURLInput) { in.Key = "a/." }),
		"invalid range: not one byte range": valid(
			func(in *storage.PresignDownloadURLInput) { in.Range = new("x") }),
		"invalid argument: Version is empty": valid(
			func(in *storage.PresignDownloadURLInput) { in.Version = new("") }),
		"invalid header: ResponseContentDisposition holds a character other than printable ASCII, space and tab": valid(
			func(in *storage.PresignDownloadURLInput) { in.ResponseContentDisposition = new("a\nb") }),
		"invalid header: ResponseExpires is not an IMF-fixdate": valid(
			func(in *storage.PresignDownloadURLInput) { in.ResponseExpires = new("tomorrow") }),
		"invalid argument: TTL is not 1s to 168h0m0s": valid(
			func(in *storage.PresignDownloadURLInput) { in.TTL = new(time.Millisecond) }),
	} {
		_, err = sg.PresignDownloadURL(t.Context(), in)
		require.EqualError(t, err, signerText(s, opPresignDown, want))
	}
}

// A presigner returns ErrNotInitialized before Init and ErrClosed after
// Close, as a store does; a nil input is ErrNilInput first.
func presignLifecycle(t *testing.T, s Subject) {
	t.Helper()

	bucket := newBucketName()
	sg := buildSigner(t, s, bucket)
	ctx := t.Context()
	up := &storage.PresignUploadURLInput{
		BaseObjectInput: ref(bucket, "k"),
		ContentLength:   new(int64(1)),
	}
	down := &storage.PresignDownloadURLInput{GetObjectInput: *getIn(bucket, "k")}
	check := func(sentinel error, text string) {
		_, err := sg.PresignUploadURL(ctx, up)
		requireErr(t, err, sentinel, signerText(s, opPresignUp, text))

		_, err = sg.PresignDownloadURL(ctx, down)
		requireErr(t, err, sentinel, signerText(s, opPresignDown, text))
	}

	_, err := sg.PresignUploadURL(ctx, nil)
	require.ErrorIs(t, err, storage.ErrNilInput)

	check(storage.ErrNotInitialized, "service not initialized")
	require.NoError(t, sg.Init(ctx))
	requireErr(t, sg.Init(ctx), storage.ErrAlreadyInitialized,
		signerText(s, opInit, "service already initialized"))
	require.NoError(t, sg.Close(ctx))
	check(storage.ErrClosed, "service closed")
	require.NoError(t, sg.Close(ctx))
	require.NoError(t, sg.Init(ctx))

	_, err = sg.PresignUploadURL(ctx, up)
	require.NoError(t, err)
}

// A presigner returns the context's error when its context has ended.
func presignCanceled(t *testing.T, s Subject) {
	t.Helper()

	bucket := newBucketName()
	sg := openSigner(t, s, bucket)

	_, err := sg.PresignUploadURL(canceled(), &storage.PresignUploadURLInput{
		BaseObjectInput: ref(bucket, "k"),
		ContentLength:   new(int64(1)),
	})
	requireErr(t, err, context.Canceled, signerText(s, opPresignUp, "context canceled"))

	_, err = sg.PresignDownloadURL(canceled(), &storage.PresignDownloadURLInput{
		GetObjectInput: *getIn(bucket, "k"),
	})
	requireErr(t, err, context.Canceled, signerText(s, opPresignDown, "context canceled"))
}

// No error of a presigner holds a key, a bucket name, a metadata name or
// value, or a header value.
func presignErrorsHoldNoData(t *testing.T, s Subject) {
	t.Helper()

	bucket := newBucketName()
	sg := openSigner(t, s, bucket)

	errs := make([]error, 0, 6)

	for _, edit := range []func(*storage.PresignUploadURLInput){
		func(in *storage.PresignUploadURLInput) { in.Key = "zq7/." },
		func(in *storage.PresignUploadURLInput) { in.Bucket = new("ZQ7-bucket") },
		func(in *storage.PresignUploadURLInput) { in.Metadata = map[string]string{"zq7 n": "v"} },
		func(in *storage.PresignUploadURLInput) { in.Metadata = map[string]string{"n": "zq7\n"} },
		func(in *storage.PresignUploadURLInput) { in.ContentDisposition = new("zq7\n") },
		func(in *storage.PresignUploadURLInput) { in.IfMatch = new("zq7 x") },
	} {
		in := &storage.PresignUploadURLInput{
			BaseObjectInput: ref(bucket, "k"),
			ContentLength:   new(int64(1)),
		}
		edit(in)

		_, err := sg.PresignUploadURL(t.Context(), in)

		errs = append(errs, err)
	}

	for _, err := range errs {
		require.Error(t, err)
		assert.NotContains(t, strings.ToLower(err.Error()), "zq7")
		assert.NotContains(t, err.Error(), bucket)
	}
}
