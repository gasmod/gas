package s3_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/storage"
	s3store "github.com/gasmod/gas/storage/s3"
)

// OpTimeout bounds a GetObject until its response headers arrive; the
// Body is then read whatever the time it takes, and after a Close.
func TestTimeout_GetObjectBody(t *testing.T) {
	t.Parallel()

	f := newFake(t)
	cfg := f.config("b-1")

	cfg.Storage.OpTimeout = 200 * time.Millisecond

	p := f.withBucket(t, "b-1")
	slow := open(t, cfg)

	putString(t, p, "k", "v")

	f.setHook(func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method != http.MethodGet {
			return false
		}

		w.Header().Set("Content-Length", "6")
		w.WriteHeader(http.StatusOK)

		_, _ = io.WriteString(w, "abc")

		w.(http.Flusher).Flush()
		time.Sleep(3 * cfg.Storage.OpTimeout)

		_, _ = io.WriteString(w, "def")

		return true
	})

	obj, err := slow.GetObject(t.Context(), &storage.GetObjectInput{
		BaseObjectInput: keyK,
	})
	require.NoError(t, err)
	require.NoError(t, slow.Close(t.Context()))

	got, err := io.ReadAll(obj.Body)
	require.NoError(t, err)
	require.NoError(t, obj.Body.Close())
	assert.Equal(t, "abcdef", string(got))
}

// OpTimeout bounds each request of a PutObject, each part, not the whole
// write.
func TestTimeout_PutObjectPerPart(t *testing.T) {
	t.Parallel()

	f := newFake(t)
	cfg := f.config("b-1")

	cfg.Storage.OpTimeout = time.Second

	f.withBucket(t, "b-1")

	p := open(t, cfg)

	// CreateMultipartUpload, the parts, which are sent at once, and
	// CompleteMultipartUpload each take most of OpTimeout, one after the
	// other.
	f.setHook(func(_ http.ResponseWriter, r *http.Request) bool {
		if r.Method == http.MethodPost || r.URL.Query().Has("partNumber") {
			time.Sleep(cfg.Storage.OpTimeout * 2 / 3)
		}

		return false
	})

	start := time.Now()

	_, err := p.PutObject(t.Context(), &storage.PutObjectInput{
		BaseObjectInput: keyK,
		Body:            bytes.NewReader(make([]byte, partBody)),
	})
	require.NoError(t, err)
	assert.Greater(t, time.Since(start), 2*cfg.Storage.OpTimeout)
}

// A call whose own context ends returns the context's error, and a Close
// whose context ends cancels the calls in flight, which return
// ErrClosed, and logs it.
func TestTimeout_CallerAndClose(t *testing.T) {
	t.Parallel()

	cfg := newFake(t).config("b-1")

	cfg.Storage.Endpoint, cfg.Storage.OpTimeout = "http://"+blackhole(t), time.Minute

	p, log := build(t, s3store.WithConfig(cfg))
	require.NoError(t, p.Init(t.Context()))

	ctx, cancel := context.WithCancel(t.Context())

	time.AfterFunc(100*time.Millisecond, cancel)

	_, err := p.ListObjects(ctx, &storage.ListObjectsInput{})
	require.ErrorIs(t, err, context.Canceled)
	require.EqualError(t, err, "gas/storage/s3: list objects: context canceled")

	got := make(chan error, 1)

	go func() {
		_, inFlight := p.ListBuckets(context.Background(), &storage.ListBucketsInput{})
		got <- inFlight
	}()

	time.Sleep(100 * time.Millisecond)

	closeCtx, cancelClose := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancelClose()

	err = p.Close(closeCtx)
	require.ErrorIs(t, err, context.DeadlineExceeded)

	select {
	case err := <-got:
		require.ErrorIs(t, err, storage.ErrClosed)
		require.EqualError(t, err, "gas/storage/s3: list buckets: service closed")
	case <-time.After(5 * time.Second):
		t.Fatal("the call in flight did not return")
	}

	entries := log.entries()
	assert.Equal(t, "warn", entries[len(entries)-1].level)
	assert.Equal(t, "service closed, calls in flight canceled", entries[len(entries)-1].msg)
}
