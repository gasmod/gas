//nolint:mnd // test rules: counts read best where they are checked.
package conformance

import (
	"context"
	"errors"
	"io"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/storage"
)

// lifecycleRules returns the rules of the lifecycle, the context and the
// concurrency.
func lifecycleRules() map[string]func(*testing.T, Subject) {
	return map[string]func(*testing.T, Subject){
		"Lifecycle":           lifecycle,
		"CloseNotInitialized": closeNotInitialized,
		"InitCanceled":        initCanceled,
		"RestartKeepsObjects": restartKeepsObjects,
		"CanceledContext":     canceledContext,
		"NilInput":            nilInput,
		"CloseWaits":          closeWaits,
		"CloseCancels":        closeCancels,
		"PutCanceled":         putCanceled,
		"RestartRace":         restartRace,
		"Concurrent":          concurrent,
	}
}

// Every method returns ErrNotInitialized before Init, and ErrClosed after
// Close until Init succeeds again; a second Init is ErrAlreadyInitialized,
// a second Close nil, and Close then Init restarts the store.
func lifecycle(t *testing.T, s Subject) {
	t.Helper()

	bucket := newBucketName()
	st := build(t, s, bucket)
	ctx := t.Context()

	for _, c := range calls() {
		requireErr(t, c.fn(ctx, st, bucket), storage.ErrNotInitialized,
			errText(s, c.op, "service not initialized"))
	}

	require.NoError(t, st.Init(ctx))
	requireErr(t, st.Init(ctx), storage.ErrAlreadyInitialized,
		errText(s, opInit, "service already initialized"))
	require.NoError(t, st.CreateBucket(ctx, &storage.CreateBucketInput{Bucket: bucket}))
	require.NoError(t, st.CheckReady(ctx))
	require.NoError(t, st.Close(ctx))

	for _, c := range calls() {
		requireErr(t, c.fn(ctx, st, bucket), storage.ErrClosed, errText(s, c.op, "service closed"))
	}

	require.NoError(t, st.Close(ctx))
	require.NoError(t, st.Init(ctx))
	require.NoError(t, st.CheckReady(ctx))
	put(t, st, bucket, "k", "v")
}

// Close on a store that is not initialized returns nil.
func closeNotInitialized(t *testing.T, s Subject) {
	t.Helper()

	st := s.New(t, "")

	require.NoError(t, st.Close(t.Context()))
	require.NoError(t, st.Close(t.Context()))
}

// Init with an ended context returns the context's error and opens
// nothing; a later Init succeeds.
func initCanceled(t *testing.T, s Subject) {
	t.Helper()

	st := build(t, s, newBucketName())

	requireErr(t, st.Init(canceled()), context.Canceled, errText(s, opInit, "context canceled"))
	requireErr(t, st.CheckReady(t.Context()), storage.ErrNotInitialized,
		errText(s, opReady, "service not initialized"))
	require.NoError(t, st.Init(t.Context()))
}

// The buckets and objects outlive a restart.
func restartKeepsObjects(t *testing.T, s Subject) {
	t.Helper()

	st, bucket := setup(t, s)

	put(t, st, bucket, "k", "kept")
	require.NoError(t, st.Close(t.Context()))
	require.NoError(t, st.Init(t.Context()))
	requireBody(t, st, bucket, "k", "kept")
}

// Every method returns the context's error, wrapped, when its context has
// ended before it starts, and changes nothing.
func canceledContext(t *testing.T, s Subject) {
	t.Helper()

	st, bucket := setup(t, s)
	other := newBucketName()

	for _, c := range calls() {
		requireErr(
			t,
			c.fn(canceled(), st, other),
			context.Canceled,
			errText(s, c.op, "context canceled"),
		)
	}

	_, err := st.HeadBucket(t.Context(), &storage.HeadBucketInput{Bucket: other})
	require.ErrorIs(t, err, storage.ErrBucketNotFound)

	_, err = st.PutObject(canceled(), putIn(bucket, "k", "v"))
	require.ErrorIs(t, err, context.Canceled)
	requireAbsent(t, st, bucket, "k")
}

// A nil input is ErrNilInput, before the state of the store is checked.
func nilInput(t *testing.T, s Subject) {
	t.Helper()

	st := build(t, s, newBucketName())
	ctx := t.Context()

	_, headBucket := st.HeadBucket(ctx, nil)
	_, listBuckets := st.ListBuckets(ctx, nil)
	_, head := st.HeadObject(ctx, nil)
	_, putErr := st.PutObject(ctx, nil)
	_, getErr := st.GetObject(ctx, nil)
	_, list := st.ListObjects(ctx, nil)

	for op, err := range map[string]error{
		opCreateBucket: st.CreateBucket(ctx, nil),
		opHeadBucket:   headBucket,
		opDeleteBucket: st.DeleteBucket(ctx, nil),
		opListBuckets:  listBuckets,
		opHeadObject:   head,
		opPutObject:    putErr,
		opGetObject:    getErr,
		opDeleteObject: st.DeleteObject(ctx, nil),
		opListObjects:  list,
	} {
		requireErr(t, err, storage.ErrNilInput, errText(s, op, "input is nil"))
	}
}

// gatedBody is a body whose first Read blocks until release is closed,
// then ends.
type gatedBody struct {
	// entered is closed when the first Read starts.
	entered chan struct{}
	// release ends the Read.
	release chan struct{}
	// once closes entered once.
	once sync.Once
}

// newGatedBody returns a gatedBody that has not been read.
func newGatedBody() *gatedBody {
	return &gatedBody{entered: make(chan struct{}), release: make(chan struct{})}
}

// Read blocks until release is closed, then returns io.EOF.
func (b *gatedBody) Read([]byte) (int, error) {
	b.once.Do(func() { close(b.entered) })
	<-b.release

	return 0, io.EOF
}

// slowBody is an endless body that yields a byte at a time, slowly.
type slowBody struct {
	// entered is closed when the first Read starts.
	entered chan struct{}
	// once closes entered once.
	once sync.Once
}

// newSlowBody returns a slowBody that has not been read.
func newSlowBody() *slowBody { return &slowBody{entered: make(chan struct{})} }

// Read waits a moment and yields one byte.
func (b *slowBody) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.entered) })
	time.Sleep(5 * time.Millisecond)

	if len(p) == 0 {
		return 0, nil
	}

	p[0] = 'x'

	return 1, nil
}

// Close waits for a PutObject in flight, which then completes, and
// returns nil.
func closeWaits(t *testing.T, s Subject) {
	t.Helper()

	st, bucket := setup(t, s)
	body := newGatedBody()
	done := make(chan error, 1)

	go func() {
		in := putIn(bucket, "k", "")

		in.Body, in.ContentLength = body, nil

		_, err := st.PutObject(context.Background(), in)
		done <- err
	}()

	<-body.entered

	closed := make(chan error, 1)

	go func() { closed <- st.Close(context.Background()) }()

	select {
	case err := <-closed:
		require.Failf(t, "Close returned while a call was in flight", "%v", err)
	case <-time.After(pause):
	}

	close(body.release)
	require.NoError(t, <-done)
	require.NoError(t, <-closed)
	require.NoError(t, st.Init(t.Context()))
	requireBody(t, st, bucket, "k", "")
}

// A Close whose context ends first cancels the PutObject in flight, which
// returns ErrClosed and stores nothing, and returns the context's error.
func closeCancels(t *testing.T, s Subject) {
	t.Helper()

	st, bucket := setup(t, s)
	body := newSlowBody()
	done := make(chan error, 1)

	go func() {
		in := putIn(bucket, "k", "")

		in.Body, in.ContentLength = body, nil

		_, err := st.PutObject(context.Background(), in)
		done <- err
	}()

	<-body.entered

	ctx, cancel := context.WithTimeout(t.Context(), pause)
	defer cancel()

	requireErr(
		t,
		st.Close(ctx),
		context.DeadlineExceeded,
		errText(s, opClose, "context deadline exceeded"),
	)

	select {
	case err := <-done:
		requireErr(t, err, storage.ErrClosed, errText(s, opPutObject, "service closed"))
	case <-time.After(prompt):
		require.Fail(t, "PutObject did not return after Close canceled it")
	}

	requireErr(
		t,
		st.CheckReady(t.Context()),
		storage.ErrClosed,
		errText(s, opReady, "service closed"),
	)
	require.NoError(t, st.Init(t.Context()))
	requireAbsent(t, st, bucket, "k")
}

// A PutObject whose context ends while it reads the body returns the
// context's error, stores nothing, and leaves the key free for the next
// write.
func putCanceled(t *testing.T, s Subject) {
	t.Helper()

	st, bucket := setup(t, s)
	body := newSlowBody()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)

	go func() {
		in := putIn(bucket, "k", "")

		in.Body, in.ContentLength = body, nil

		_, err := st.PutObject(ctx, in)
		done <- err
	}()

	<-body.entered
	time.Sleep(pause)
	cancel()

	select {
	case err := <-done:
		requireErr(t, err, context.Canceled, errText(s, opPutObject, "context canceled"))
	case <-time.After(prompt):
		require.Fail(t, "PutObject did not return after its context ended")
	}

	requireAbsent(t, st, bucket, "k")
	put(t, st, bucket, "k", "next")
	requireBody(t, st, bucket, "k", "next")
}

// Restarts racing calls, for -race: every call succeeds or returns
// ErrClosed.
func restartRace(t *testing.T, s Subject) {
	t.Helper()

	st, bucket := setup(t, s)
	stop := make(chan struct{})

	var wg sync.WaitGroup

	for range 4 {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}

				_, err := st.HeadBucket(
					context.Background(),
					&storage.HeadBucketInput{Bucket: bucket},
				)
				if err != nil && !errors.Is(err, storage.ErrClosed) {
					t.Errorf("HeadBucket during a restart: %v", err)

					return
				}
			}
		})
	}

	for range 20 {
		require.NoError(t, st.Close(context.Background()))
		require.NoError(t, st.Init(context.Background()))
	}

	close(stop)
	wg.Wait()
}

// Calls from many goroutines at once, for -race, each on its own key.
func concurrent(t *testing.T, s Subject) {
	t.Helper()

	st, bucket := setup(t, s)

	var wg sync.WaitGroup

	for i := range 8 {
		wg.Go(func() {
			key := "k" + strconv.Itoa(i)

			_, putErr := st.PutObject(context.Background(), putIn(bucket, key, key))
			_, headErr := st.HeadObject(context.Background(), headIn(bucket, key))
			_, listErr := st.ListObjects(
				context.Background(),
				&storage.ListObjectsInput{Bucket: &bucket},
			)

			err := errors.Join(putErr, headErr, listErr)
			if err != nil {
				t.Errorf("concurrent calls: %v", err)
			}
		})
	}

	wg.Wait()

	out, err := st.ListObjects(t.Context(), &storage.ListObjectsInput{Bucket: &bucket})
	require.NoError(t, err)
	require.Len(t, out.Objects, 8)
}
