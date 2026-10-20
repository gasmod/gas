package storagetest

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/gasmod/gas/storage"
)

var (
	_ storage.Provider  = (*MockProvider)(nil)
	_ storage.Presigner = (*MockPresigner)(nil)
)

// TestMockProvider_Defaults checks that unset Fn fields return nil, nil and
// calls are recorded in order.
func TestMockProvider_Defaults(t *testing.T) {
	t.Parallel()

	m := &MockProvider{}
	ctx := context.Background()

	if info, err := m.CreateBucket(ctx, nil); info != nil || err != nil {
		t.Errorf("CreateBucket() = %v, %v, want nil, nil", info, err)
	}
	if info, err := m.HeadObject(ctx, nil); info != nil || err != nil {
		t.Errorf("HeadObject() = %v, %v, want nil, nil", info, err)
	}
	if info, err := m.PutObject(ctx, nil); info != nil || err != nil {
		t.Errorf("PutObject() = %v, %v, want nil, nil", info, err)
	}
	if obj, err := m.GetObject(ctx, nil); obj != nil || err != nil {
		t.Errorf("GetObject() = %v, %v, want nil, nil", obj, err)
	}
	if info, err := m.DeleteObject(ctx, nil); info != nil || err != nil {
		t.Errorf("DeleteObject() = %v, %v, want nil, nil", info, err)
	}
	if out, err := m.ListBuckets(ctx, nil); out != nil || err != nil {
		t.Errorf("ListBuckets() = %v, %v, want nil, nil", out, err)
	}
	if out, err := m.ListObjects(ctx, nil); out != nil || err != nil {
		t.Errorf("ListObjects() = %v, %v, want nil, nil", out, err)
	}

	order := []string{
		"CreateBucket", "HeadObject", "PutObject", "GetObject", "DeleteObject",
		"ListBuckets", "ListObjects",
	}
	for _, method := range order {
		if n := m.CallCount(method); n != 1 {
			t.Errorf("CallCount(%s) = %d, want 1", method, n)
		}
	}
	if n := m.CallCount("Unknown"); n != 0 {
		t.Errorf("CallCount(Unknown) = %d, want 0", n)
	}
	if len(m.Calls) != len(order) {
		t.Fatalf("len(Calls) = %d, want %d", len(m.Calls), len(order))
	}
	for i, c := range m.Calls {
		if c.Method != order[i] {
			t.Errorf("Calls[%d].Method = %q, want %q", i, c.Method, order[i])
		}
	}
}

// TestMockProvider_DelegatesAndRecords checks that each method delegates to
// its Fn field and records its input.
func TestMockProvider_DelegatesAndRecords(t *testing.T) {
	t.Parallel()

	errBoom := errors.New("boom")
	createIn := &storage.CreateBucketInput{Bucket: "b"}
	headIn := &storage.HeadObjectInput{BaseObjectInput: storage.BaseObjectInput{Key: "h"}}
	putIn := &storage.PutObjectInput{BaseObjectInput: storage.BaseObjectInput{Key: "p"}}
	getIn := &storage.GetObjectInput{HeadObjectInput: *headIn}
	delIn := &storage.DeleteObjectInput{BaseObjectInput: storage.BaseObjectInput{Key: "d"}}

	type ctxKey struct{}
	ctx := context.WithValue(context.Background(), ctxKey{}, "v")

	m := &MockProvider{
		CreateBucketFn: func(c context.Context, in *storage.CreateBucketInput) (*storage.Bucket, error) {
			if c.Value(ctxKey{}) != "v" || in != createIn {
				t.Error("CreateBucketFn received wrong arguments")
			}
			return nil, storage.ErrBucketExists
		},
		HeadObjectFn: func(c context.Context, in *storage.HeadObjectInput) (*storage.ObjectInfo, error) {
			if c.Value(ctxKey{}) != "v" || in != headIn {
				t.Error("HeadObjectFn received wrong arguments")
			}
			return &storage.ObjectInfo{Size: 1}, nil
		},
		PutObjectFn: func(c context.Context, in *storage.PutObjectInput) (*storage.ObjectInfo, error) {
			if c.Value(ctxKey{}) != "v" || in != putIn {
				t.Error("PutObjectFn received wrong arguments")
			}
			return &storage.ObjectInfo{Version: "v1"}, nil
		},
		GetObjectFn: func(c context.Context, in *storage.GetObjectInput) (*storage.Object, error) {
			if c.Value(ctxKey{}) != "v" || in != getIn {
				t.Error("GetObjectFn received wrong arguments")
			}
			return nil, errBoom
		},
		DeleteObjectFn: func(c context.Context, in *storage.DeleteObjectInput) (*storage.ObjectInfo, error) {
			if c.Value(ctxKey{}) != "v" || in != delIn {
				t.Error("DeleteObjectFn received wrong arguments")
			}
			return &storage.ObjectInfo{Version: "gone"}, nil
		},
	}

	if info, err := m.CreateBucket(ctx, createIn); !errors.Is(err, storage.ErrBucketExists) ||
		info != nil {
		t.Errorf("CreateBucket() = %+v, %v, want nil, ErrBucketExists", info, err)
	}
	if info, err := m.HeadObject(ctx, headIn); err != nil || info.Size != 1 {
		t.Errorf("HeadObject() = %+v, %v", info, err)
	}
	if info, err := m.PutObject(ctx, putIn); err != nil || info.Version != "v1" {
		t.Errorf("PutObject() = %+v, %v", info, err)
	}
	if _, err := m.GetObject(ctx, getIn); !errors.Is(err, errBoom) {
		t.Errorf("GetObject() error = %v, want %v", err, errBoom)
	}
	if info, err := m.DeleteObject(ctx, delIn); err != nil || info.Version != "gone" {
		t.Errorf("DeleteObject() = %+v, %v", info, err)
	}

	wantArgs := []any{createIn, headIn, putIn, getIn, delIn}
	if len(m.Calls) != len(wantArgs) {
		t.Fatalf("len(Calls) = %d, want %d", len(m.Calls), len(wantArgs))
	}
	for i, c := range m.Calls {
		if len(c.Args) != 1 || c.Args[0] != wantArgs[i] {
			t.Errorf("Calls[%d].Args = %v, want [%v]", i, c.Args, wantArgs[i])
		}
	}
}

// TestMockProvider_DelegatesList checks that the list methods delegate to
// their Fn fields and record their inputs.
func TestMockProvider_DelegatesList(t *testing.T) {
	t.Parallel()

	bucketsIn := &storage.ListBucketsInput{Prefix: new("b")}
	objectsIn := &storage.ListObjectsInput{Prefix: new("o")}

	type ctxKey struct{}
	ctx := context.WithValue(context.Background(), ctxKey{}, "v")

	m := &MockProvider{
		ListBucketsFn: func(
			c context.Context,
			in *storage.ListBucketsInput,
		) (*storage.ListBucketsOutput, error) {
			if c.Value(ctxKey{}) != "v" || in != bucketsIn {
				t.Error("ListBucketsFn received wrong arguments")
			}
			return &storage.ListBucketsOutput{NextContinuationToken: "next"}, nil
		},
		ListObjectsFn: func(
			c context.Context,
			in *storage.ListObjectsInput,
		) (*storage.ListObjectsOutput, error) {
			if c.Value(ctxKey{}) != "v" || in != objectsIn {
				t.Error("ListObjectsFn received wrong arguments")
			}
			return &storage.ListObjectsOutput{IsTruncated: true}, nil
		},
	}

	if out, err := m.ListBuckets(
		ctx,
		bucketsIn,
	); err != nil ||
		out.NextContinuationToken != "next" {
		t.Errorf("ListBuckets() = %+v, %v", out, err)
	}
	if out, err := m.ListObjects(ctx, objectsIn); err != nil || !out.IsTruncated {
		t.Errorf("ListObjects() = %+v, %v", out, err)
	}

	want := []Call{
		{Method: "ListBuckets", Args: []any{bucketsIn}},
		{Method: "ListObjects", Args: []any{objectsIn}},
	}
	if len(m.Calls) != len(want) {
		t.Fatalf("len(Calls) = %d, want %d", len(m.Calls), len(want))
	}
	for i, c := range m.Calls {
		if c.Method != want[i].Method || len(c.Args) != 1 || c.Args[0] != want[i].Args[0] {
			t.Errorf("Calls[%d] = %+v, want %+v", i, c, want[i])
		}
	}
}

// TestMockProvider_Reset checks that Reset clears the recorded calls.
func TestMockProvider_Reset(t *testing.T) {
	t.Parallel()

	m := &MockProvider{}
	_, _ = m.HeadObject(context.Background(), nil)
	_, _ = m.HeadObject(context.Background(), nil)
	if m.CallCount("HeadObject") != 2 {
		t.Fatalf("CallCount = %d, want 2", m.CallCount("HeadObject"))
	}
	m.Reset()
	if m.CallCount("HeadObject") != 0 || len(m.Calls) != 0 {
		t.Errorf(
			"after Reset: CallCount = %d, len(Calls) = %d",
			m.CallCount("HeadObject"),
			len(m.Calls),
		)
	}
	// Reset on a fresh mock is a no-op.
	(&MockProvider{}).Reset()
}

// TestMockProvider_Concurrent checks concurrent calls under -race.
func TestMockProvider_Concurrent(t *testing.T) {
	t.Parallel()

	m := &MockProvider{
		PutObjectFn: func(context.Context, *storage.PutObjectInput) (*storage.ObjectInfo, error) {
			return &storage.ObjectInfo{}, nil
		},
	}
	const n = 100
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			_, _ = m.PutObject(context.Background(), nil)
			_ = m.CallCount("PutObject")
		})
		wg.Go(func() { _, _ = m.HeadObject(context.Background(), nil) })
	}
	wg.Wait()
	if m.CallCount("PutObject") != n || m.CallCount("HeadObject") != n {
		t.Errorf(
			"CallCount Put=%d Head=%d, want %d each",
			m.CallCount("PutObject"),
			m.CallCount("HeadObject"),
			n,
		)
	}
}

// TestMockPresigner_Defaults checks that unset Fn fields return nil, nil and
// calls are recorded.
func TestMockPresigner_Defaults(t *testing.T) {
	t.Parallel()

	m := &MockPresigner{}
	ctx := context.Background()
	if res, err := m.PresignUploadURL(ctx, nil); res != nil || err != nil {
		t.Errorf("PresignUploadURL() = %v, %v, want nil, nil", res, err)
	}
	if res, err := m.PresignDownloadURL(ctx, nil); res != nil || err != nil {
		t.Errorf("PresignDownloadURL() = %v, %v, want nil, nil", res, err)
	}
	if m.CallCount("PresignUploadURL") != 1 || m.CallCount("PresignDownloadURL") != 1 {
		t.Errorf("CallCount upload=%d download=%d, want 1 each",
			m.CallCount("PresignUploadURL"), m.CallCount("PresignDownloadURL"))
	}
}

// TestMockPresigner_DelegatesAndRecords checks delegation, recording, and
// Reset.
func TestMockPresigner_DelegatesAndRecords(t *testing.T) {
	t.Parallel()

	errBoom := errors.New("boom")
	upIn := &storage.PresignUploadURLInput{}
	downIn := &storage.PresignDownloadURLInput{}
	m := &MockPresigner{
		PresignUploadURLFn: func(_ context.Context, in *storage.PresignUploadURLInput) (*storage.PresignURLResponse, error) {
			if in != upIn {
				t.Error("PresignUploadURLFn received wrong input")
			}
			return &storage.PresignURLResponse{URL: "https://up", Method: "PUT"}, nil
		},
		PresignDownloadURLFn: func(_ context.Context, in *storage.PresignDownloadURLInput) (*storage.PresignURLResponse, error) {
			if in != downIn {
				t.Error("PresignDownloadURLFn received wrong input")
			}
			return nil, errBoom
		},
	}

	res, err := m.PresignUploadURL(context.Background(), upIn)
	if err != nil || res.URL != "https://up" || res.Method != "PUT" {
		t.Errorf("PresignUploadURL() = %+v, %v", res, err)
	}
	if _, err := m.PresignDownloadURL(context.Background(), downIn); !errors.Is(err, errBoom) {
		t.Errorf("PresignDownloadURL() error = %v, want %v", err, errBoom)
	}
	if len(m.Calls) != 2 || m.Calls[0].Args[0] != upIn || m.Calls[1].Args[0] != downIn {
		t.Errorf("Calls = %+v", m.Calls)
	}

	m.Reset()
	if len(m.Calls) != 0 || m.CallCount("PresignUploadURL") != 0 {
		t.Errorf("after Reset: %+v", m.Calls)
	}
}

// TestMockPresigner_Concurrent checks concurrent calls under -race.
func TestMockPresigner_Concurrent(t *testing.T) {
	t.Parallel()

	m := &MockPresigner{}
	const n = 100
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() { _, _ = m.PresignUploadURL(context.Background(), nil) })
		wg.Go(func() {
			_, _ = m.PresignDownloadURL(context.Background(), nil)
			m.CallCount("PresignDownloadURL")
		})
	}
	wg.Wait()
	if m.CallCount("PresignUploadURL") != n || m.CallCount("PresignDownloadURL") != n {
		t.Errorf("CallCount upload=%d download=%d, want %d each",
			m.CallCount("PresignUploadURL"), m.CallCount("PresignDownloadURL"), n)
	}
}
