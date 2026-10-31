//nolint:mnd,wrapcheck // test rules: the errors under test are returned as they are.
package conformance

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"net/http"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/storage"
)

// Store is an object store with a lifecycle and readiness.
type Store interface {
	gas.Service
	gas.ReadyReporter
	storage.Provider
}

// Signer is a presigner with a lifecycle.
type Signer interface {
	gas.Service
	storage.Presigner
}

// Subject is a backend under test.
type Subject struct {
	// New returns a new store, not initialized, whose configured bucket is
	// bucket, none when it is empty. Every store of one Subject shares one
	// backend, such as one server. The suite closes it.
	New func(t *testing.T, bucket string) Store
	// NewPresigner returns a new presigner, not initialized, that signs
	// requests to the backend of New's stores, with bucket configured as
	// for New. Nil skips the presign rules. The suite closes it.
	NewPresigner func(t *testing.T, bucket string) Signer
	// HTTPClient sends the presigned requests. Nil is http.DefaultClient.
	HTTPClient *http.Client
	// Skip maps the name of a rule to the reason the subject skips it,
	// such as a backend that keeps versions, or an emulator that does not
	// do as S3 does.
	Skip map[string]string
	// Name is the store's ServiceName, which starts its errors.
	Name string
	// PresignerName is the presigner's ServiceName, which starts its
	// errors.
	PresignerName string
}

// The times the rules wait on.
const (
	// pause is how long a rule lets a call it started begin.
	pause = 100 * time.Millisecond
	// prompt is how long a call that should return at once may take.
	prompt = 5 * time.Second
)

// The operations errors name.
const (
	opInit         = "init"
	opReady        = "ready"
	opClose        = "close"
	opCreateBucket = "create bucket"
	opHeadBucket   = "head bucket"
	opDeleteBucket = "delete bucket"
	opListBuckets  = "list buckets"
	opHeadObject   = "head object"
	opPutObject    = "put object"
	opGetObject    = "get object"
	opDeleteObject = "delete object"
	opListObjects  = "list objects"
	opPresignUp    = "presign upload"
	opPresignDown  = "presign download"
)

// Run runs the suite on s, each rule as a parallel subtest.
func Run(t *testing.T, s Subject) {
	t.Helper()

	for _, group := range []map[string]func(*testing.T, Subject){
		lifecycleRules(), objectRules(), inputRules(), bucketRules(), listRules(), presignRules(),
	} {
		for name, rule := range group {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				if reason, ok := s.Skip[name]; ok {
					t.Skip(reason)
				}

				rule(t, s)
			})
		}
	}
}

// runID keeps apart the buckets of runs that share a server.
var runID = func() string {
	b := make([]byte, 4)

	// It never fails: it crashes the program instead.
	_, _ = rand.Read(b)

	return hex.EncodeToString(b)
}()

// bucketSeq numbers the buckets of a run.
var bucketSeq atomic.Int64

// newBucketName returns a bucket name no other rule uses.
func newBucketName() string {
	return "gasconf-" + runID + "-" + strconv.FormatInt(bucketSeq.Add(1), 10)
}

// open returns a new store of s, initialized, whose configured bucket is
// bucket, and closes it when t ends.
func open(t *testing.T, s Subject, bucket string) Store {
	t.Helper()

	st := build(t, s, bucket)
	require.NoError(t, st.Init(t.Context()))

	return st
}

// build returns a new store of s, not initialized, and closes it when t
// ends.
func build(t *testing.T, s Subject, bucket string) Store {
	t.Helper()

	st := s.New(t, bucket)
	t.Cleanup(func() { assert.NoError(t, st.Close(context.Background())) })

	return st
}

// setup returns a new store of s, initialized, with a new bucket created
// and configured.
func setup(t *testing.T, s Subject) (Store, string) {
	t.Helper()

	bucket := newBucketName()
	st := open(t, s, bucket)

	require.NoError(t, st.CreateBucket(t.Context(), &storage.CreateBucketInput{Bucket: bucket}))

	return st, bucket
}

// errText returns the text of an error of op of s: its Name, the
// operation, then rest.
func errText(s Subject, op, rest string) string {
	return s.Name + ": " + op + ": " + rest
}

// ref returns the BaseObjectInput of key in bucket.
func ref(bucket, key string) storage.BaseObjectInput {
	return storage.BaseObjectInput{Bucket: &bucket, Key: key}
}

// headIn returns the HeadObjectInput of key in bucket.
func headIn(bucket, key string) *storage.HeadObjectInput {
	return &storage.HeadObjectInput{BaseObjectInput: ref(bucket, key)}
}

// getIn returns the GetObjectInput of key in bucket.
func getIn(bucket, key string) *storage.GetObjectInput {
	return &storage.GetObjectInput{HeadObjectInput: *headIn(bucket, key)}
}

// putIn returns the PutObjectInput of body under key in bucket, with its
// ContentLength.
func putIn(bucket, key, body string) *storage.PutObjectInput {
	return &storage.PutObjectInput{
		BaseObjectInput: ref(bucket, key),
		Body:            bytes.NewReader([]byte(body)),
		ContentLength:   new(int64(len(body))),
	}
}

// put writes body under key in bucket.
func put(t *testing.T, st Store, bucket, key, body string) *storage.PutObjectOutput {
	t.Helper()

	out, err := st.PutObject(t.Context(), putIn(bucket, key, body))
	require.NoError(t, err)

	return out
}

// get returns the object under key in bucket, and its contents.
func get(t *testing.T, st Store, in *storage.GetObjectInput) (*storage.Object, string) {
	t.Helper()

	obj, err := st.GetObject(t.Context(), in)
	require.NoError(t, err)

	defer func() { require.NoError(t, obj.Body.Close()) }()

	b, err := io.ReadAll(obj.Body)
	require.NoError(t, err)

	return obj, string(b)
}

// requireAbsent fails t unless no object is stored under key in bucket.
func requireAbsent(t *testing.T, st Store, bucket, key string) {
	t.Helper()

	_, err := st.HeadObject(t.Context(), headIn(bucket, key))
	require.ErrorIs(t, err, storage.ErrObjectNotFound)
}

// requireBody fails t unless body is stored under key in bucket.
func requireBody(t *testing.T, st Store, bucket, key, body string) {
	t.Helper()

	_, got := get(t, st, getIn(bucket, key))
	require.Equal(t, body, got)
}

// requireErr fails t unless err wraps target and reads want.
func requireErr(t *testing.T, err, target error, want string) {
	t.Helper()

	require.ErrorIs(t, err, target)
	require.EqualError(t, err, want)
}

// call is a call of one method with valid inputs on bucket, which may
// not exist, returning only its error.
type call struct {
	// fn makes the call.
	fn func(ctx context.Context, st Store, bucket string) error
	// op is the operation errors name.
	op string
}

// calls returns a call of each method of a store, with valid inputs.
func calls() []call {
	return []call{
		{func(ctx context.Context, st Store, b string) error {
			return st.CreateBucket(ctx, &storage.CreateBucketInput{Bucket: b})
		}, opCreateBucket},
		{func(ctx context.Context, st Store, b string) error {
			_, err := st.HeadBucket(ctx, &storage.HeadBucketInput{Bucket: b})

			return err
		}, opHeadBucket},
		{func(ctx context.Context, st Store, b string) error {
			return st.DeleteBucket(ctx, &storage.DeleteBucketInput{Bucket: b})
		}, opDeleteBucket},
		{func(ctx context.Context, st Store, _ string) error {
			_, err := st.ListBuckets(ctx, &storage.ListBucketsInput{})

			return err
		}, opListBuckets},
		{func(ctx context.Context, st Store, b string) error {
			_, err := st.HeadObject(ctx, headIn(b, "k"))

			return err
		}, opHeadObject},
		{func(ctx context.Context, st Store, b string) error {
			_, err := st.PutObject(ctx, putIn(b, "k", "v"))

			return err
		}, opPutObject},
		{func(ctx context.Context, st Store, b string) error {
			_, err := st.GetObject(ctx, getIn(b, "k"))

			return err
		}, opGetObject},
		{func(ctx context.Context, st Store, b string) error {
			return st.DeleteObject(ctx, &storage.DeleteObjectInput{BaseObjectInput: ref(b, "k")})
		}, opDeleteObject},
		{func(ctx context.Context, st Store, b string) error {
			_, err := st.ListObjects(ctx, &storage.ListObjectsInput{Bucket: &b})

			return err
		}, opListObjects},
		{func(ctx context.Context, st Store, _ string) error {
			return st.CheckReady(ctx)
		}, opReady},
	}
}

// canceled returns a context that has ended.
func canceled() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	return ctx
}
