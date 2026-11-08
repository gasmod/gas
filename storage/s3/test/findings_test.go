package s3_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/storage"
	s3store "github.com/gasmod/gas/storage/s3"
)

// partBody is the length of a body the transfer manager writes in parts:
// past its 16 MiB threshold.
const partBody = 17 << 20

// With no region anywhere, the store was built and every request failed
// with the AWS SDK's `Invalid region`. Init now fails with ErrNoRegion, on
// the store and on the presigner.
//
// Not parallel: it sets AWS variables.
func TestFinding_NoRegion(t *testing.T) {
	isolateAWS(t)

	cfg := newFake(t).config("")

	cfg.Storage.Region = ""

	err := initErr(t, s3store.WithConfig(cfg))
	require.ErrorIs(t, err, s3store.ErrNoRegion)

	sg, _ := buildSigner(t, s3store.WithPresignerConfig(cfg))
	require.ErrorIs(t, sg.Init(t.Context()), s3store.ErrNoRegion)
	require.EqualError(t, sg.Init(t.Context()), "gas/storage/s3/presigner: init: no region")
}

// Static credentials with only an access key ID were used as they were,
// so every request failed; with only a secret, the default chain was used
// silently. Either now falls back to the chain with a warning naming the
// keys missing, and Init retrieves the credentials, so that none fails
// startup.
//
// Not parallel: it sets AWS variables.
func TestFinding_PartialCredentials(t *testing.T) {
	isolateAWS(t)

	f := newFake(t)

	for missing, edit := range map[string]func(*s3store.Settings){
		"storage.secretaccesskey": func(s *s3store.Settings) { s.SecretAccessKey = "" },
		"storage.accesskeyid":     func(s *s3store.Settings) { s.AccessKeyID = "" },
		"storage.accesskeyid, storage.secretaccesskey": func(s *s3store.Settings) {
			s.AccessKeyID, s.SecretAccessKey, s.SessionToken = "", "", "zqxtoken"
		},
	} {
		cfg := f.config("")
		edit(&cfg.Storage)

		p, log := build(t, s3store.WithConfig(cfg))

		err := p.Init(t.Context())
		require.ErrorIs(t, err, s3store.ErrNoCredentials, missing)

		entries := log.entries()
		require.NotEmpty(t, entries)
		assert.Equal(t, "warn", entries[0].level)
		assert.Equal(t, "static credentials incomplete, using the default chain", entries[0].msg)
		assert.Equal(t, missing, entries[0].fields["missing"])
		assertNoSecrets(t, log.text())
	}

	t.Setenv("AWS_ACCESS_KEY_ID", testKeyID)
	t.Setenv("AWS_SECRET_ACCESS_KEY", testSecret)

	cfg := f.config("")

	cfg.Storage.SecretAccessKey = ""

	p, log := build(t, s3store.WithConfig(cfg))
	require.NoError(t, p.Init(t.Context()))

	entries := log.entries()
	assert.Equal(t, "default chain", entries[len(entries)-1].fields["credentials"])
}

// The endpoint was not checked, and an endpoint forced path-style
// addressing, with no way to choose it. The endpoint must now be an http
// or https URL with nothing after its host but `/`, and UsePathStyle
// chooses the addressing, true by default with an endpoint.
func TestFinding_EndpointAndPathStyle(t *testing.T) {
	t.Parallel()

	f := newFake(t)

	for _, endpoint := range []string{
		"https://s3.example/?X-Token=zqx", "https://s3.example#zqx", "https://s3.example/zqx",
		"http://zqx@s3.example", "s3.example:4566", "ftp://s3.example",
	} {
		cfg := f.config("")

		cfg.Storage.Endpoint = endpoint

		err := initErr(t, s3store.WithConfig(cfg))
		require.ErrorIs(t, err, storage.ErrInvalidConfig, endpoint)
		require.EqualError(t, err, "gas/storage/s3: init: invalid config: "+
			"Storage.Endpoint must be an http or https URL with a host and nothing more but /")
	}

	for _, c := range []struct {
		usePathStyle *bool
		endpoint     string
		want         bool
	}{
		{nil, f.srv.URL, true},
		{new(false), f.srv.URL, false},
		{nil, "", false},
		{new(true), "", true},
	} {
		cfg := f.config("")

		cfg.Storage.Endpoint, cfg.Storage.UsePathStyle = c.endpoint, c.usePathStyle

		assert.Equal(t, c.want, open(t, cfg).Client().Options().UsePathStyle, c)
	}
}

// `%+v` and JSON of a Config printed the secret key and the session
// token. They are now gas.Secrets, so a Config prints them as
// `[redacted]`.
func TestFinding_SecretsRedacted(t *testing.T) {
	t.Parallel()

	cfg := s3store.DefaultConfig()

	cfg.Storage.SecretAccessKey, cfg.Storage.SessionToken = "zqxsecret", "zqxtoken"

	text := fmt.Sprintf("%+v", cfg)
	assert.NotContains(t, text, "zqx")
	assert.Contains(t, text, "[redacted]")
}

// The AWS configuration was loaded with context.Background in the
// constructor. Init now loads it within its own context.
func TestFinding_InitContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	p, _ := build(t, s3store.WithConfig(newFake(t).config("")))

	err := p.Init(ctx)
	require.ErrorIs(t, err, context.Canceled)
	require.EqualError(t, err, "gas/storage/s3: init: context canceled")
}

// A HeadObject against a server that accepts and never answers was still
// blocked after 20s with a context that never ends. OpTimeout now bounds
// every request, retries included.
func TestFinding_OpTimeout(t *testing.T) {
	t.Parallel()

	cfg := newFake(t).config("b-1")

	cfg.Storage.Endpoint, cfg.Storage.OpTimeout = "http://"+blackhole(t), 300*time.Millisecond

	start := time.Now()

	_, err := open(t, cfg).HeadObject(context.Background(), &storage.HeadObjectInput{
		Key: "k",
	})
	require.ErrorIs(t, err, s3store.ErrConnect)
	require.ErrorIs(t, err, storage.ErrBackend)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.EqualError(t, err, "gas/storage/s3: head object: timed out")
	assert.Less(t, time.Since(start), 3*time.Second)
}

// AWS_MAX_ATTEMPTS overrode the retries, which had no setting. MaxRetries
// now holds whatever AWS_MAX_ATTEMPTS and AWS_RETRY_MODE say.
//
// Not parallel: it sets AWS variables.
func TestFinding_MaxAttemptsEnv(t *testing.T) {
	isolateAWS(t)
	t.Setenv("AWS_MAX_ATTEMPTS", "5")
	t.Setenv("AWS_RETRY_MODE", "adaptive")

	for retries, attempts := range map[int]int32{0: 1, 1: 2} {
		f := newFake(t)
		cfg := f.config("b-1")

		cfg.Storage.MaxRetries = retries

		p := open(t, cfg)

		var n atomic.Int32

		f.setHook(func(w http.ResponseWriter, r *http.Request) bool {
			n.Add(1)
			fakeError(w, r, http.StatusInternalServerError, "InternalError")

			return true
		})

		_, err := p.ListObjects(t.Context(), &storage.ListObjectsInput{})
		require.ErrorIs(t, err, storage.ErrBackend)
		assert.Equal(t, attempts, n.Load(), "MaxRetries %d", retries)
	}
}

// A write the server could never accept, such as one with a header value
// holding a CR or LF, was sent, and retried. Every input is now checked
// before any request.
func TestFinding_InvalidNeverSent(t *testing.T) {
	t.Parallel()

	f := newFake(t)
	p := f.withBucket(t, "b-1")

	for _, in := range []*storage.PutObjectInput{
		{
			Key:      "k",
			Metadata: map[string]string{"n": "a\r\nb"},
		},
		{
			Key:         "k",
			ContentType: new("text/plain\n"),
		},
		{
			Key:      "k",
			Metadata: map[string]string{"n": "café"},
		},
	} {
		_, err := p.PutObject(t.Context(), in)
		require.Error(t, err)
	}

	assert.Zero(t, f.count("PutObject"))
}

// A reply was read whole, whatever its size: a ListObjectsV2 reply of 360
// MiB was decoded. Every reply but the contents of a GetObject is now cut
// off past 16 MiB, as an invalid reply; an object larger than that is
// still read whole.
func TestFinding_ReplyCapped(t *testing.T) {
	t.Parallel()

	f := newFake(t)
	p := f.withBucket(t, "b-1")
	big := bytes.Repeat([]byte("x"), partBody)

	_, err := p.PutObject(t.Context(), &storage.PutObjectInput{
		Key:  "big",
		Body: bytes.NewReader(big),
	})
	require.NoError(t, err)

	obj, err := p.GetObject(t.Context(), &storage.GetObjectInput{
		Key: "big",
	})
	require.NoError(t, err)

	var got bytes.Buffer

	_, err = got.ReadFrom(obj.Body)
	require.NoError(t, err)
	require.NoError(t, obj.Body.Close())
	assert.Equal(t, partBody, got.Len())

	huge := append([]byte("<ListBucketResult><Name>"), big...)

	for status, want := range map[int]string{
		http.StatusOK:                  "gas/storage/s3: list objects: invalid reply",
		http.StatusInternalServerError: "gas/storage/s3: list objects: invalid reply",
	} {
		f.setHook(rawReply(status, huge))

		cfg := f.config("b-1")

		cfg.Storage.MaxRetries = 0

		_, err = open(t, cfg).ListObjects(t.Context(), &storage.ListObjectsInput{})
		require.ErrorIs(t, err, storage.ErrBackend)
		require.EqualError(t, err, want, status)
	}
}

// A PutObject in parts whose context ended was not aborted: the abort ran
// on the ended context and failed, leaving the parts stored. The abort
// now runs on a context of its own, bounded by OpTimeout.
func TestFinding_CanceledUploadAborted(t *testing.T) {
	t.Parallel()

	f := newFake(t)
	p := f.withBucket(t, "b-1")
	ctx, cancel := context.WithCancel(t.Context())

	defer cancel()

	f.setHook(func(_ http.ResponseWriter, r *http.Request) bool {
		if r.URL.Query().Has("partNumber") {
			cancel()
		}

		return false
	})

	_, err := p.PutObject(ctx, &storage.PutObjectInput{
		Key:  "big",
		Body: &endless{},
	})
	require.ErrorIs(t, err, context.Canceled)
	require.EqualError(t, err, "gas/storage/s3: put object: context canceled")
	assert.Equal(t, 1, f.count("AbortMultipartUpload"))
	assert.Zero(t, f.pending())
}

// The abort of a failed upload in parts was reported as a rare case, and
// its failure only in text. It is now joined to the error, classified,
// wrapping ErrUploadNotAborted, and logged.
func TestFinding_AbortFailureReported(t *testing.T) {
	t.Parallel()

	f := newFake(t)
	err := canceledUpload(t, f)

	require.EqualError(t, err,
		"gas/storage/s3: put object: context canceled; upload not aborted: failed (InternalError)")
	assert.Equal(t, 1, f.pending())
	assertNoSecrets(t, err.Error())
}

// A condition on a write in parts is evaluated when the upload completes,
// after every part was sent; the upload is then aborted, and the error
// holds no upload ID.
func TestFinding_ConditionInParts(t *testing.T) {
	t.Parallel()

	f := newFake(t)
	p := f.withBucket(t, "b-1")

	putString(t, p, "k", "v")

	_, err := p.PutObject(t.Context(), &storage.PutObjectInput{
		Key:         "k",
		Body:        bytes.NewReader(make([]byte, partBody)),
		IfNoneMatch: new("*"),
	})
	require.ErrorIs(t, err, storage.ErrPreconditionFailed)
	require.EqualError(t, err, "gas/storage/s3: put object: precondition failed")
	assert.Equal(t, 3, f.count("UploadPart"))
	assert.Equal(t, 1, f.count("AbortMultipartUpload"))
	assert.Zero(t, f.pending())
}

// Errors wrapped the AWS SDK's, which held the URL, so the bucket and the
// key, the server's message, the request ID, header names and upload
// IDs, and errors.As reached the SDK's types. Errors now have a fixed
// text, and wrap only sentinels.
func TestFinding_ErrorsSanitized(t *testing.T) {
	t.Parallel()

	f := newFake(t)
	cfg := f.config("zqx-private")

	cfg.Storage.MaxRetries = 0

	p := open(t, cfg)
	key := storage.BaseObjectInput{Key: "users/zqxalice/passport.pdf"}

	_, missing := p.GetObject(t.Context(), &storage.GetObjectInput{
		BaseObjectInput: key,
	})

	f.setHook(awsError(http.StatusInternalServerError, "InternalError"))

	_, failed := p.HeadObject(t.Context(), &storage.HeadObjectInput{BaseObjectInput: key})

	cfg.Storage.Endpoint = "http://" + closedAddr(t)

	_, refused := open(t, cfg).PutObject(t.Context(), &storage.PutObjectInput{BaseObjectInput: key})

	for _, err := range []error{missing, failed, refused} {
		require.Error(t, err)
		assertNoSecrets(t, err.Error(), "users", "private", "passport", "Request", "Resource")

		_, ok := errors.AsType[smithy.APIError](err)
		assert.False(t, ok, "%v reaches the SDK's error", err)
	}
}

// A 403 on an object operation or a listing carried no sentinel. Every
// refusal of the credentials is now ErrAccessDenied.
func TestFinding_AccessDenied(t *testing.T) {
	t.Parallel()

	f := newFake(t)

	f.withBucket(t, "b-1")

	cfg := f.config("b-1")

	cfg.Storage.AccessKeyID = "AKIDWRONG"

	p := open(t, cfg)
	key := storage.BaseObjectInput{Key: "k"}
	ctx := t.Context()

	_, head := p.HeadObject(ctx, &storage.HeadObjectInput{BaseObjectInput: key})
	_, put := p.PutObject(ctx, &storage.PutObjectInput{BaseObjectInput: key})
	_, list := p.ListObjects(ctx, &storage.ListObjectsInput{})

	for err, text := range map[error]string{
		head: "gas/storage/s3: head object: access denied (Forbidden)",
		put:  "gas/storage/s3: put object: access denied (InvalidAccessKeyId)",
		list: "gas/storage/s3: list objects: access denied (InvalidAccessKeyId)",
		p.DeleteObject(ctx, &storage.DeleteObjectInput{BaseObjectInput: key}): "gas/storage/s3: " +
			"delete object: access denied (InvalidAccessKeyId)",
		p.CheckReady(ctx): "gas/storage/s3: ready: access denied (Forbidden)",
	} {
		require.ErrorIs(t, err, storage.ErrAccessDenied)
		require.EqualError(t, err, text)
	}
}

// A 409 ConditionalRequestConflict, a 301 PermanentRedirect, a throttle
// and a transport failure carried no sentinel. Each now has its own,
// with storage.ErrBackend.
func TestFinding_ReplySentinels(t *testing.T) {
	t.Parallel()

	for code, c := range map[string]struct {
		sentinel error
		status   int
	}{
		"ConditionalRequestConflict": {s3store.ErrConflict, http.StatusConflict},
		"OperationAborted":           {s3store.ErrConflict, http.StatusConflict},
		"PermanentRedirect":          {s3store.ErrWrongRegion, http.StatusMovedPermanently},
		"SlowDown":                   {s3store.ErrThrottled, http.StatusServiceUnavailable},
	} {
		p := replying(t, awsError(c.status, code))

		_, err := p.ListObjects(t.Context(), &storage.ListObjectsInput{})
		require.ErrorIs(t, err, c.sentinel, code)
		require.ErrorIs(t, err, storage.ErrBackend, code)
	}

	p := replying(t, awsError(http.StatusMovedPermanently, ""))

	_, err := p.HeadObject(t.Context(), &storage.HeadObjectInput{
		Key: "k",
	})
	require.ErrorIs(t, err, s3store.ErrWrongRegion)
	require.EqualError(t, err, "gas/storage/s3: head object: wrong region (MovedPermanently)")
}

// A ListObjects of a token S3 did not issue failed with no sentinel, and
// the token was S3's own, which can tell a key, as the fake's and
// LocalStack's do. The token now seals S3's, so it tells nothing, and one
// the store did not issue is ErrInvalidToken, before any request.
func TestFinding_TokensSealed(t *testing.T) {
	t.Parallel()

	f := newFake(t)
	p := f.withBucket(t, "b-1")

	for _, k := range []string{"zqx-a", "zqx-b"} {
		putString(t, p, k, "v")
	}

	out, err := p.ListObjects(t.Context(), &storage.ListObjectsInput{MaxKeys: new(int32(1))})
	require.NoError(t, err)
	require.NotEmpty(t, out.NextContinuationToken)

	for _, leak := range []string{"zqx", base64.StdEncoding.EncodeToString([]byte("kzqx-a"))} {
		assert.NotContains(t, out.NextContinuationToken, strings.TrimRight(leak, "="))
	}

	lists := f.count("ListObjectsV2")

	_, err = p.ListObjects(t.Context(), &storage.ListObjectsInput{ContinuationToken: new("AAAA")})
	require.ErrorIs(t, err, storage.ErrInvalidToken)
	assert.Equal(t, lists, f.count("ListObjectsV2"))

	next, err := p.ListObjects(t.Context(), &storage.ListObjectsInput{
		ContinuationToken: &out.NextContinuationToken,
	})
	require.NoError(t, err)
	require.Len(t, next.Objects, 1)
	assert.Equal(t, "zqx-b", next.Objects[0].Key)
}

// KI-051: WithClient, WithUploaderClient, WithPresignerS3Client and
// WithPresignClient assigned a nil client, though their comments said a
// nil one was ignored. A nil client, or a nil *s3.Client or
// *s3.PresignClient, now fails Init with ErrNilClient, and the comments
// say so; the uploader is no longer an option.
func TestFinding_KI051(t *testing.T) {
	t.Parallel()

	cfg := s3store.DefaultConfig()

	for _, opt := range []s3store.Option{
		s3store.WithClient(nil), s3store.WithClient((*awss3.Client)(nil)),
	} {
		err := initErr(t, s3store.WithConfig(cfg), opt)
		require.ErrorIs(t, err, s3store.ErrNilClient)
		require.EqualError(t, err, "gas/storage/s3: init: client is nil")
	}

	for _, opt := range []s3store.PresignerOption{
		s3store.WithPresignClient(nil), s3store.WithPresignClient((*awss3.PresignClient)(nil)),
	} {
		sg, _ := buildSigner(t, s3store.WithPresignerConfig(cfg), opt)

		err := sg.Init(t.Context())
		require.ErrorIs(t, err, s3store.ErrNilClient)
		require.EqualError(t, err, "gas/storage/s3/presigner: init: client is nil")
	}
}

// The store was no gas.Service: it bound its configuration in the
// constructor, had no Close, and no readiness. It is now a service whose
// CheckReady sends a HeadBucket of the configured bucket, and nothing
// without one.
func TestFinding_ReadyHeadBucket(t *testing.T) {
	t.Parallel()

	f := newFake(t)
	p := f.withBucket(t, "b-1")

	require.NoError(t, p.CheckReady(t.Context()))
	assert.Equal(t, 1, f.count("HeadBucket"))

	require.NoError(t, open(t, f.config("")).CheckReady(t.Context()))
	assert.Equal(t, 1, f.count("HeadBucket"))
}

// A presigned upload without a ContentLength let a client upload any
// size, and one of 0 bytes signed no length: the AWS SDK signs no
// Content-Length of 0. ContentLength is now required, and an upload of 0
// bytes signs the Content-MD5 of the empty body, which S3 checks.
func TestFinding_PresignLength(t *testing.T) {
	t.Parallel()

	f := newFake(t)

	presignLength(t, f.withBucket(t, "b-1"), openSigner(t, f.config("b-1")))
}

// An upload of 0 bytes is held to 0 bytes on LocalStack too.
func TestFinding_PresignLengthLocalStack(t *testing.T) {
	t.Parallel()

	endpoint := localStackEndpoint(t)
	bucket := fmt.Sprintf("gas-s3-length-%d", time.Now().UnixNano())
	p := open(t, localStackConfig(endpoint, bucket))

	require.NoError(t, p.CreateBucket(t.Context(), &storage.CreateBucketInput{Bucket: bucket}))
	presignLength(t, p, openSigner(t, localStackConfig(endpoint, bucket)))
}

// presignLength checks that sg, which signs for the configured bucket of
// p, requires a ContentLength, and holds an upload of 0 bytes to 0 bytes.
func presignLength(t *testing.T, p *s3store.Provider, sg *s3store.Presigner) {
	t.Helper()

	_, err := sg.PresignUploadURL(t.Context(), &storage.PresignUploadURLInput{
		Key: "k",
	})
	require.ErrorIs(t, err, storage.ErrInvalidArgument)

	up, err := sg.PresignUploadURL(t.Context(), &storage.PresignUploadURLInput{
		Key:           "k",
		ContentLength: new(int64(0)),
	})
	require.NoError(t, err)
	assert.Equal(t, "1B2M2Y8AsgTpgAmY7PhCfg==", up.SignedHeader.Get("Content-MD5"))

	assert.GreaterOrEqual(t, sendPresigned(t, up, "more than nothing"), http.StatusBadRequest)
	requireMissing(t, p, "k")

	assert.Equal(t, http.StatusOK, sendPresigned(t, up, ""))

	info, err := p.HeadObject(t.Context(), &storage.HeadObjectInput{
		Key: "k",
	})
	require.NoError(t, err)
	assert.Zero(t, info.Size)
}

// A presigned URL holds the session token of temporary credentials, which
// the documentation now says.
func TestFinding_PresignSessionToken(t *testing.T) {
	t.Parallel()

	cfg := newFake(t).config("b-1")

	cfg.Storage.SessionToken = "zqxtoken"

	out, err := openSigner(t, cfg).PresignDownloadURL(t.Context(), &storage.PresignDownloadURLInput{
		Key: "k",
	})
	require.NoError(t, err)
	assert.Contains(t, out.URL, "X-Amz-Security-Token=zqxtoken")
}

// sendPresigned sends the presigned request p with body, every signed
// header field included, and returns the response's status.
func sendPresigned(t *testing.T, p *storage.PresignURLResponse, body string) int {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), p.Method, p.URL, strings.NewReader(body))
	require.NoError(t, err)

	for name, values := range p.SignedHeader {
		if name != "Host" {
			req.Header[name] = values
		}
	}

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	return resp.StatusCode
}

// requireMissing fails the test unless the configured bucket of p holds
// no object under key.
func requireMissing(t *testing.T, p storage.Provider, key string) {
	t.Helper()

	_, err := p.HeadObject(t.Context(), &storage.HeadObjectInput{
		Key: key,
	})
	require.ErrorIs(t, err, storage.ErrObjectNotFound)
}

// A canceled upload in parts is aborted on LocalStack too, which lists no
// upload left behind.
func TestFinding_CanceledUploadAbortedLocalStack(t *testing.T) {
	t.Parallel()

	endpoint := localStackEndpoint(t)
	bucket := fmt.Sprintf("gas-s3-abort-%d", time.Now().UnixNano())

	p := open(t, localStackConfig(endpoint, bucket))
	require.NoError(t, p.CreateBucket(t.Context(), &storage.CreateBucketInput{Bucket: bucket}))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	// The upload has begun by the time 30 MiB are read: the transfer
	// manager begins it once it has read 16.
	_, err := p.PutObject(ctx, &storage.PutObjectInput{
		Key:  "big",
		Body: &cancelAfter{left: 30 << 20, cancel: cancel},
	})
	require.ErrorIs(t, err, context.Canceled)

	raw := awss3.New(awss3.Options{
		Region:       testRegion,
		BaseEndpoint: aws.String(endpoint),
		UsePathStyle: true,
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{
				AccessKeyID:     localStackKeyID,
				SecretAccessKey: localStackSecret,
			}, nil
		}),
	})

	uploads, err := raw.ListMultipartUploads(t.Context(),
		&awss3.ListMultipartUploadsInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	assert.Empty(t, uploads.Uploads)
}

// cancelAfter is an endless body that calls cancel once it has yielded
// left bytes.
type cancelAfter struct {
	// cancel is called once left reaches 0.
	cancel context.CancelFunc
	// left counts the bytes to yield before cancel.
	left int
}

// Read fills p with `x`, and calls cancel once left bytes were read.
func (c *cancelAfter) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}

	c.left -= len(p)
	if c.left <= 0 {
		c.cancel()
	}

	return len(p), nil
}

// The AWS SDK logged to standard error, outside the store's logger, with
// text of its own, such as a Date header of a reply it could not parse.
// The client now gives it a logger that discards everything.
//
// Not parallel: it replaces os.Stderr.
func TestFinding_SDKLogsNothing(t *testing.T) {
	f := newFake(t)

	stderr, err := os.CreateTemp(t.TempDir(), "stderr")
	require.NoError(t, err)

	saved := os.Stderr

	os.Stderr = stderr

	t.Cleanup(func() { os.Stderr = saved })

	p := f.withBucket(t, "b-1")
	putString(t, p, "k", "v")

	f.setHook(func(w http.ResponseWriter, _ *http.Request) bool {
		w.Header().Set("Date", "zqx-not-a-date")

		return false
	})

	_, err = p.HeadObject(t.Context(), &storage.HeadObjectInput{Key: "k"})
	require.NoError(t, err)

	os.Stderr = saved

	require.NoError(t, stderr.Close())

	written, err := os.ReadFile(stderr.Name())
	require.NoError(t, err)
	assert.Empty(t, string(written))
}

// A presigned upload of 0 bytes signed no Content-Type: the AWS SDK
// removes the field when the length is 0, so a client could send any.
// It is now signed at every length.
func TestFinding_PresignEmptyContentType(t *testing.T) {
	t.Parallel()

	sg := openSigner(t, newFake(t).config("b-1"))

	for _, n := range []int64{0, 1} {
		up, err := sg.PresignUploadURL(t.Context(), &storage.PresignUploadURLInput{
			Key:           "k",
			ContentLength: new(n),
			ContentType:   new("image/png"),
		})
		require.NoError(t, err)
		assert.Equal(t, "image/png", up.SignedHeader.Get("Content-Type"), n)
		assert.Contains(t, up.URL, "content-type", n)
	}
}

// A continuation token of S3's longer than about 1500 bytes was sealed
// into a token the store then refused as not its own, so the listing
// could not go on. S3 does not bound its tokens; one too long to seal now
// fails the listing with ErrBackend rather than hand out a token that
// never opens.
func TestFinding_LongServerToken(t *testing.T) {
	t.Parallel()

	f := newFake(t)
	cfg := f.config("b-1")

	cfg.Storage.MaxRetries = 0

	p := open(t, cfg)

	for _, c := range []struct {
		token string
		fails bool
	}{
		{strings.Repeat("t", 1536), false},
		{strings.Repeat("t", 1537), true},
	} {
		f.setHook(rawReply(http.StatusOK, []byte(
			`<ListBucketResult><Name>b-1</Name><KeyCount>1</KeyCount><MaxKeys>1</MaxKeys>`+
				`<IsTruncated>true</IsTruncated><EncodingType>url</EncodingType>`+
				`<NextContinuationToken>`+c.token+`</NextContinuationToken>`+
				`<Contents><Key>k</Key><Size>1</Size></Contents></ListBucketResult>`)))

		out, err := p.ListObjects(t.Context(), &storage.ListObjectsInput{})
		if !c.fails {
			require.NoError(t, err)
			require.NotEmpty(t, out.NextContinuationToken)

			// The token opens: the next page asks S3 with its token.
			f.setHook(func(w http.ResponseWriter, r *http.Request) bool {
				assert.Equal(t, c.token, r.URL.Query().Get("continuation-token"))

				return rawReply(http.StatusOK, []byte(
					`<ListBucketResult><Name>b-1</Name><IsTruncated>false</IsTruncated>`+
						`<EncodingType>url</EncodingType></ListBucketResult>`))(w, r)
			})

			_, err = p.ListObjects(t.Context(), &storage.ListObjectsInput{
				ContinuationToken: &out.NextContinuationToken,
			})
			require.NoError(t, err)

			continue
		}

		require.ErrorIs(t, err, storage.ErrBackend)
		require.EqualError(t, err, "gas/storage/s3: list objects: invalid reply")
	}
}
