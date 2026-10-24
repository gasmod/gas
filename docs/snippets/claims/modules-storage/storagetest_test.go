package modulesstorage_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gasmod/gas/storage"
	"github.com/gasmod/gas/storage/storagetest"
)

// Docs: modules/storage.mdx#testing
// "`storagetest.MockProvider` and `storagetest.MockPresigner` implement the two
// interfaces. ... Each method records the call in `Calls` (the method name and the
// arguments after the context) and delegates to the matching `Fn` field, such
// as `GetObjectFn` or `PresignUploadURLFn`, when it is set"
func TestMocksRecordAndDelegate(t *testing.T) {
	t.Parallel()
	var _ storage.Provider = &storagetest.MockProvider{}
	var _ storage.Presigner = &storagetest.MockPresigner{}
	want := &storage.Object{ObjectInfo: storage.ObjectInfo{ETag: "e"}}
	type ctxKey struct{}
	ctx := context.WithValue(context.Background(), ctxKey{}, "v")
	var gotCtx context.Context
	mp := &storagetest.MockProvider{GetObjectFn: func(c context.Context, _ *storage.GetObjectInput) (*storage.Object, error) {
		gotCtx = c
		return want, nil
	}}
	in := getIn("k")
	got, err := mp.GetObject(ctx, in)
	if err != nil || got != want || gotCtx != ctx {
		t.Fatalf("GetObject() = %v, %v, want GetObjectFn's result with the caller's context", got, err)
	}
	if len(mp.Calls) != 1 || mp.Calls[0].Method != "GetObject" || len(mp.Calls[0].Args) != 1 || mp.Calls[0].Args[0] != any(in) {
		t.Fatalf("Calls = %+v, want one GetObject call with the input as its only argument", mp.Calls)
	}

	resp := &storage.PresignURLResponse{URL: "u"}
	ms := &storagetest.MockPresigner{PresignUploadURLFn: func(context.Context, *storage.PresignUploadURLInput) (*storage.PresignURLResponse, error) {
		return resp, nil
	}}
	pin := &storage.PresignUploadURLInput{}
	r, err := ms.PresignUploadURL(ctx, pin)
	if err != nil || r != resp {
		t.Fatalf("PresignUploadURL() = %v, %v, want PresignUploadURLFn's result", r, err)
	}
	if len(ms.Calls) != 1 || ms.Calls[0].Method != "PresignUploadURL" || ms.Calls[0].Args[0] != any(pin) {
		t.Fatalf("MockPresigner.Calls = %+v, want one PresignUploadURL call with the input", ms.Calls)
	}
}

// Docs: modules/storage.mdx#testing
// "otherwise it returns `nil, nil` (`nil` for `DeleteBucket`)."
// Docs: modules/storage.mdx#testing
// "The zero value is ready to use."
func TestMocksZeroValueReturnsNil(t *testing.T) {
	t.Parallel()
	var mp storagetest.MockProvider
	ctx := context.Background()
	check := func(m string, v any, err error) {
		t.Helper()
		if err != nil || !isNil(v) {
			t.Errorf("zero MockProvider %s = %v, %v, want nil, nil", m, v, err)
		}
	}
	v1, err := mp.CreateBucket(ctx, &storage.CreateBucketInput{})
	check("CreateBucket", v1, err)
	v2, err := mp.HeadBucket(ctx, &storage.HeadBucketInput{})
	check("HeadBucket", v2, err)
	if err := mp.DeleteBucket(ctx, &storage.DeleteBucketInput{}); err != nil {
		t.Errorf("zero MockProvider DeleteBucket = %v, want nil", err)
	}
	v3, err := mp.ListBuckets(ctx, &storage.ListBucketsInput{})
	check("ListBuckets", v3, err)
	v4, err := mp.HeadObject(ctx, headIn("k"))
	check("HeadObject", v4, err)
	v5, err := mp.PutObject(ctx, &storage.PutObjectInput{})
	check("PutObject", v5, err)
	v6, err := mp.GetObject(ctx, getIn("k"))
	check("GetObject", v6, err)
	v7, err := mp.DeleteObject(ctx, &storage.DeleteObjectInput{})
	check("DeleteObject", v7, err)
	v8, err := mp.ListObjects(ctx, &storage.ListObjectsInput{})
	check("ListObjects", v8, err)
	if len(mp.Calls) != 9 {
		t.Errorf("zero MockProvider recorded %d calls, want 9", len(mp.Calls))
	}

	var ms storagetest.MockPresigner
	u, err := ms.PresignUploadURL(ctx, &storage.PresignUploadURLInput{})
	check("PresignUploadURL", u, err)
	d, err := ms.PresignDownloadURL(ctx, &storage.PresignDownloadURLInput{})
	check("PresignDownloadURL", d, err)
}

func isNil(v any) bool {
	switch x := v.(type) {
	case *storage.Bucket:
		return x == nil
	case *storage.ListBucketsOutput:
		return x == nil
	case *storage.ObjectInfo:
		return x == nil
	case *storage.Object:
		return x == nil
	case *storage.ListObjectsOutput:
		return x == nil
	case *storage.PresignURLResponse:
		return x == nil
	}
	return v == nil
}

// Docs: modules/storage.mdx#testing
// "`CallCount(method)` counts the calls to one method, and `Reset` clears
// `Calls` but keeps the `Fn` fields."
func TestMocksCallCountAndReset(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("from Fn")
	mp := &storagetest.MockProvider{HeadBucketFn: func(context.Context, *storage.HeadBucketInput) (*storage.Bucket, error) {
		return nil, sentinel
	}}
	ctx := context.Background()
	for range 3 {
		_, _ = mp.HeadBucket(ctx, &storage.HeadBucketInput{})
	}
	_ = mp.DeleteBucket(ctx, &storage.DeleteBucketInput{})
	if mp.CallCount("HeadBucket") != 3 || mp.CallCount("DeleteBucket") != 1 || mp.CallCount("GetObject") != 0 {
		t.Fatalf("CallCount = %d HeadBucket, %d DeleteBucket, %d GetObject, want 3, 1, 0",
			mp.CallCount("HeadBucket"), mp.CallCount("DeleteBucket"), mp.CallCount("GetObject"))
	}
	mp.Reset()
	if len(mp.Calls) != 0 || mp.CallCount("HeadBucket") != 0 {
		t.Fatalf("after Reset Calls = %v, want empty", mp.Calls)
	}
	if _, err := mp.HeadBucket(ctx, &storage.HeadBucketInput{}); !errors.Is(err, sentinel) {
		t.Fatalf("HeadBucket after Reset error = %v, want HeadBucketFn kept", err)
	}

	ms := &storagetest.MockPresigner{PresignDownloadURLFn: func(context.Context, *storage.PresignDownloadURLInput) (*storage.PresignURLResponse, error) {
		return nil, sentinel
	}}
	_, _ = ms.PresignDownloadURL(ctx, &storage.PresignDownloadURLInput{})
	_, _ = ms.PresignUploadURL(ctx, &storage.PresignUploadURLInput{})
	if ms.CallCount("PresignDownloadURL") != 1 || ms.CallCount("PresignUploadURL") != 1 {
		t.Fatalf("MockPresigner CallCount wrong")
	}
	ms.Reset()
	if len(ms.Calls) != 0 {
		t.Fatalf("MockPresigner after Reset Calls = %v, want empty", ms.Calls)
	}
	if _, err := ms.PresignDownloadURL(ctx, &storage.PresignDownloadURLInput{}); !errors.Is(err, sentinel) {
		t.Fatalf("PresignDownloadURL after Reset error = %v, want the Fn kept", err)
	}
}

// Docs: modules/storage.mdx#testing
// "Recording is safe for concurrent calls, but read `Calls` only after the calls
// under test have finished, or use `CallCount`, which locks."
// Run under -race: CallCount runs alongside the calls.
func TestMocksConcurrentRecording(t *testing.T) {
	t.Parallel()
	mp := &storagetest.MockProvider{}
	ms := &storagetest.MockPresigner{}
	const n = 50
	var wg sync.WaitGroup
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				_ = mp.CallCount("PutObject")
				_ = ms.CallCount("PresignUploadURL")
				time.Sleep(time.Microsecond)
			}
		}
	}()
	for range n {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _ = mp.PutObject(context.Background(), &storage.PutObjectInput{})
		}()
		go func() {
			defer wg.Done()
			_, _ = ms.PresignUploadURL(context.Background(), &storage.PresignUploadURLInput{})
		}()
	}
	wg.Wait()
	close(stop)
	if len(mp.Calls) != n || mp.CallCount("PutObject") != n || ms.CallCount("PresignUploadURL") != n {
		t.Fatalf("recorded %d / %d / %d calls, want %d each", len(mp.Calls), mp.CallCount("PutObject"), ms.CallCount("PresignUploadURL"), n)
	}
}
