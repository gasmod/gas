package storagetest

import (
	"context"
	"sync"

	"github.com/gasmod/gas/storage"
)

// MockProvider is a recording mock of storage.Provider. Each method records
// its call and delegates to the matching Fn field, or returns nil, nil when
// that field is nil. The zero value is ready to use.
type MockProvider struct {
	CreateBucketFn func(ctx context.Context, input *storage.CreateBucketInput) (*storage.Bucket, error)
	HeadBucketFn   func(ctx context.Context, input *storage.HeadBucketInput) (*storage.Bucket, error)
	DeleteBucketFn func(ctx context.Context, input *storage.DeleteBucketInput) error
	ListBucketsFn  func(ctx context.Context, input *storage.ListBucketsInput) (*storage.ListBucketsOutput, error)

	HeadObjectFn   func(ctx context.Context, input *storage.HeadObjectInput) (*storage.ObjectInfo, error)
	PutObjectFn    func(ctx context.Context, input *storage.PutObjectInput) (*storage.ObjectInfo, error)
	GetObjectFn    func(ctx context.Context, input *storage.GetObjectInput) (*storage.Object, error)
	DeleteObjectFn func(ctx context.Context, input *storage.DeleteObjectInput) (*storage.ObjectInfo, error)
	ListObjectsFn  func(ctx context.Context, input *storage.ListObjectsInput) (*storage.ListObjectsOutput, error)

	Calls []Call
	mu    sync.Mutex
}

var _ storage.Provider = (*MockProvider)(nil)

// Call is one recorded call: the method name and the arguments after the
// context.
type Call struct {
	Method string
	Args   []any
}

// record appends a call to Calls under the mock's lock.
func (m *MockProvider) record(method string, args ...any) {
	m.mu.Lock()
	m.Calls = append(m.Calls, Call{Method: method, Args: args})
	m.mu.Unlock()
}

// CreateBucket records the call and delegates to CreateBucketFn.
func (m *MockProvider) CreateBucket(
	ctx context.Context,
	input *storage.CreateBucketInput,
) (*storage.Bucket, error) {
	m.record("CreateBucket", input)
	if m.CreateBucketFn != nil {
		return m.CreateBucketFn(ctx, input)
	}
	return nil, nil
}

// HeadBucket records the call and delegates to HeadBucketFn.
func (m *MockProvider) HeadBucket(
	ctx context.Context,
	input *storage.HeadBucketInput,
) (*storage.Bucket, error) {
	m.record("HeadBucket", input)
	if m.HeadBucketFn != nil {
		return m.HeadBucketFn(ctx, input)
	}
	return nil, nil
}

// DeleteBucket records the call and delegates to DeleteBucketFn, or returns
// nil when it is nil.
func (m *MockProvider) DeleteBucket(ctx context.Context, input *storage.DeleteBucketInput) error {
	m.record("DeleteBucket", input)
	if m.DeleteBucketFn != nil {
		return m.DeleteBucketFn(ctx, input)
	}
	return nil
}

// ListBuckets records the call and delegates to ListBucketsFn.
func (m *MockProvider) ListBuckets(
	ctx context.Context,
	input *storage.ListBucketsInput,
) (*storage.ListBucketsOutput, error) {
	m.record("ListBuckets", input)
	if m.ListBucketsFn != nil {
		return m.ListBucketsFn(ctx, input)
	}
	return nil, nil
}

// HeadObject records the call and delegates to HeadObjectFn.
func (m *MockProvider) HeadObject(
	ctx context.Context,
	input *storage.HeadObjectInput,
) (*storage.ObjectInfo, error) {
	m.record("HeadObject", input)
	if m.HeadObjectFn != nil {
		return m.HeadObjectFn(ctx, input)
	}
	return nil, nil
}

// PutObject records the call and delegates to PutObjectFn.
func (m *MockProvider) PutObject(
	ctx context.Context,
	input *storage.PutObjectInput,
) (*storage.ObjectInfo, error) {
	m.record("PutObject", input)
	if m.PutObjectFn != nil {
		return m.PutObjectFn(ctx, input)
	}
	return nil, nil
}

// GetObject records the call and delegates to GetObjectFn.
func (m *MockProvider) GetObject(
	ctx context.Context,
	input *storage.GetObjectInput,
) (*storage.Object, error) {
	m.record("GetObject", input)
	if m.GetObjectFn != nil {
		return m.GetObjectFn(ctx, input)
	}
	return nil, nil
}

// DeleteObject records the call and delegates to DeleteObjectFn.
func (m *MockProvider) DeleteObject(
	ctx context.Context,
	input *storage.DeleteObjectInput,
) (*storage.ObjectInfo, error) {
	m.record("DeleteObject", input)
	if m.DeleteObjectFn != nil {
		return m.DeleteObjectFn(ctx, input)
	}
	return nil, nil
}

// ListObjects records the call and delegates to ListObjectsFn.
func (m *MockProvider) ListObjects(
	ctx context.Context,
	input *storage.ListObjectsInput,
) (*storage.ListObjectsOutput, error) {
	m.record("ListObjects", input)
	if m.ListObjectsFn != nil {
		return m.ListObjectsFn(ctx, input)
	}
	return nil, nil
}

// Reset clears the recorded calls. The Fn fields are kept.
func (m *MockProvider) Reset() {
	m.mu.Lock()
	m.Calls = nil
	m.mu.Unlock()
}

// CallCount returns how many times the named method was called.
func (m *MockProvider) CallCount(method string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, c := range m.Calls {
		if c.Method == method {
			n++
		}
	}
	return n
}
