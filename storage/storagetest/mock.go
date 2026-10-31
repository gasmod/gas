package storagetest

import (
	"context"
	"maps"
	"sync"

	"github.com/gasmod/gas/storage"
)

// Call is one call of a mock's method.
type Call struct {
	// Method is the name of the method called.
	Method string
	// Args are the method's arguments after the context: a copy of its
	// input.
	Args []any
}

// recorder records the calls of a mock.
type recorder struct {
	// calls holds the calls in the order they were made.
	calls []Call
	// mu guards calls, since a mock may be called from several goroutines.
	mu sync.Mutex
}

// Calls returns a deep copy of the calls, in the order they were made:
// changing an input it returns changes no recorded call.
func (r *recorder) Calls() []Call {
	r.mu.Lock()
	defer r.mu.Unlock()

	calls := make([]Call, 0, len(r.calls))

	for _, c := range r.calls {
		args := make([]any, 0, len(c.Args))

		for _, a := range c.Args {
			args = append(args, cloneArg(a))
		}

		calls = append(calls, Call{Method: c.Method, Args: args})
	}

	return calls
}

// CallCount returns how many calls were made to method.
func (r *recorder) CallCount(method string) int {
	r.mu.Lock()
	defer r.mu.Unlock()

	n := 0

	for _, c := range r.calls {
		if c.Method == method {
			n++
		}
	}

	return n
}

// Reset forgets every call. The fields are kept.
func (r *recorder) Reset() {
	r.mu.Lock()

	r.calls = nil
	r.mu.Unlock()
}

// record appends a call of method with a copy of in.
func (r *recorder) record(method string, in any) {
	r.mu.Lock()

	r.calls = append(r.calls, Call{Method: method, Args: []any{cloneArg(in)}})
	r.mu.Unlock()
}

// MockProvider is a storage.Provider that records its calls, as described
// in the package documentation. The zero MockProvider is ready to use.
//
//nolint:govet // fieldalignment: the embedded fields come first, as embeddedstructfieldcheck wants.
type MockProvider struct {
	recorder

	// CreateBucketFn is what CreateBucket returns. When it is nil,
	// CreateBucket returns nil.
	CreateBucketFn func(ctx context.Context, in *storage.CreateBucketInput) error
	// HeadBucketFn is what HeadBucket returns. When it is nil, HeadBucket
	// returns a Bucket with the name asked for.
	HeadBucketFn func(ctx context.Context, in *storage.HeadBucketInput) (*storage.Bucket, error)
	// DeleteBucketFn is what DeleteBucket returns. When it is nil,
	// DeleteBucket returns nil.
	DeleteBucketFn func(ctx context.Context, in *storage.DeleteBucketInput) error
	// ListBucketsFn is what ListBuckets returns. When it is nil,
	// ListBuckets returns an empty page.
	ListBucketsFn func(
		ctx context.Context,
		in *storage.ListBucketsInput,
	) (*storage.ListBucketsOutput, error)
	// HeadObjectFn is what HeadObject returns. When it is nil, HeadObject
	// returns storage.ErrObjectNotFound.
	HeadObjectFn func(ctx context.Context, in *storage.HeadObjectInput) (*storage.ObjectInfo, error)
	// PutObjectFn is what PutObject returns. When it is nil, PutObject
	// returns a zero PutObjectOutput, without reading the body.
	PutObjectFn func(ctx context.Context, in *storage.PutObjectInput) (*storage.PutObjectOutput, error)
	// GetObjectFn is what GetObject returns. When it is nil, GetObject
	// returns storage.ErrObjectNotFound.
	GetObjectFn func(ctx context.Context, in *storage.GetObjectInput) (*storage.Object, error)
	// DeleteObjectFn is what DeleteObject returns. When it is nil,
	// DeleteObject returns nil.
	DeleteObjectFn func(ctx context.Context, in *storage.DeleteObjectInput) error
	// ListObjectsFn is what ListObjects returns. When it is nil,
	// ListObjects returns an empty page.
	ListObjectsFn func(
		ctx context.Context,
		in *storage.ListObjectsInput,
	) (*storage.ListObjectsOutput, error)
}

var _ storage.Provider = (*MockProvider)(nil)

// CreateBucket records the call and returns CreateBucketFn's result.
func (m *MockProvider) CreateBucket(ctx context.Context, in *storage.CreateBucketInput) error {
	m.record("CreateBucket", in)

	if m.CreateBucketFn != nil {
		return m.CreateBucketFn(ctx, in)
	}

	return nil
}

// HeadBucket records the call and returns HeadBucketFn's result.
func (m *MockProvider) HeadBucket(
	ctx context.Context,
	in *storage.HeadBucketInput,
) (*storage.Bucket, error) {
	m.record("HeadBucket", in)

	if m.HeadBucketFn != nil {
		return m.HeadBucketFn(ctx, in)
	}

	b := &storage.Bucket{}
	if in != nil {
		b.Name = in.Bucket
	}

	return b, nil
}

// DeleteBucket records the call and returns DeleteBucketFn's result.
func (m *MockProvider) DeleteBucket(ctx context.Context, in *storage.DeleteBucketInput) error {
	m.record("DeleteBucket", in)

	if m.DeleteBucketFn != nil {
		return m.DeleteBucketFn(ctx, in)
	}

	return nil
}

// ListBuckets records the call and returns ListBucketsFn's result.
func (m *MockProvider) ListBuckets(
	ctx context.Context,
	in *storage.ListBucketsInput,
) (*storage.ListBucketsOutput, error) {
	m.record("ListBuckets", in)

	if m.ListBucketsFn != nil {
		return m.ListBucketsFn(ctx, in)
	}

	return &storage.ListBucketsOutput{}, nil
}

// HeadObject records the call and returns HeadObjectFn's result.
func (m *MockProvider) HeadObject(
	ctx context.Context,
	in *storage.HeadObjectInput,
) (*storage.ObjectInfo, error) {
	m.record("HeadObject", in)

	if m.HeadObjectFn != nil {
		return m.HeadObjectFn(ctx, in)
	}

	return nil, storage.ErrObjectNotFound
}

// PutObject records the call and returns PutObjectFn's result.
func (m *MockProvider) PutObject(
	ctx context.Context,
	in *storage.PutObjectInput,
) (*storage.PutObjectOutput, error) {
	m.record("PutObject", in)

	if m.PutObjectFn != nil {
		return m.PutObjectFn(ctx, in)
	}

	return &storage.PutObjectOutput{}, nil
}

// GetObject records the call and returns GetObjectFn's result.
func (m *MockProvider) GetObject(
	ctx context.Context,
	in *storage.GetObjectInput,
) (*storage.Object, error) {
	m.record("GetObject", in)

	if m.GetObjectFn != nil {
		return m.GetObjectFn(ctx, in)
	}

	return nil, storage.ErrObjectNotFound
}

// DeleteObject records the call and returns DeleteObjectFn's result.
func (m *MockProvider) DeleteObject(ctx context.Context, in *storage.DeleteObjectInput) error {
	m.record("DeleteObject", in)

	if m.DeleteObjectFn != nil {
		return m.DeleteObjectFn(ctx, in)
	}

	return nil
}

// ListObjects records the call and returns ListObjectsFn's result.
func (m *MockProvider) ListObjects(
	ctx context.Context,
	in *storage.ListObjectsInput,
) (*storage.ListObjectsOutput, error) {
	m.record("ListObjects", in)

	if m.ListObjectsFn != nil {
		return m.ListObjectsFn(ctx, in)
	}

	return &storage.ListObjectsOutput{}, nil
}

// MockPresigner is a storage.Presigner that records its calls, as
// described in the package documentation. The zero MockPresigner is ready
// to use.
//
//nolint:govet // fieldalignment: the embedded fields come first, as embeddedstructfieldcheck wants.
type MockPresigner struct {
	recorder

	// PresignUploadURLFn is what PresignUploadURL returns. When it is nil,
	// PresignUploadURL returns a zero PresignURLResponse with Method PUT.
	PresignUploadURLFn func(
		ctx context.Context,
		in *storage.PresignUploadURLInput,
	) (*storage.PresignURLResponse, error)
	// PresignDownloadURLFn is what PresignDownloadURL returns. When it is
	// nil, PresignDownloadURL returns a zero PresignURLResponse with
	// Method GET.
	PresignDownloadURLFn func(
		ctx context.Context,
		in *storage.PresignDownloadURLInput,
	) (*storage.PresignURLResponse, error)
}

var _ storage.Presigner = (*MockPresigner)(nil)

// PresignUploadURL records the call and returns PresignUploadURLFn's
// result.
func (m *MockPresigner) PresignUploadURL(
	ctx context.Context,
	in *storage.PresignUploadURLInput,
) (*storage.PresignURLResponse, error) {
	m.record("PresignUploadURL", in)

	if m.PresignUploadURLFn != nil {
		return m.PresignUploadURLFn(ctx, in)
	}

	return &storage.PresignURLResponse{Method: "PUT"}, nil
}

// PresignDownloadURL records the call and returns PresignDownloadURLFn's
// result.
func (m *MockPresigner) PresignDownloadURL(
	ctx context.Context,
	in *storage.PresignDownloadURLInput,
) (*storage.PresignURLResponse, error) {
	m.record("PresignDownloadURL", in)

	if m.PresignDownloadURLFn != nil {
		return m.PresignDownloadURLFn(ctx, in)
	}

	return &storage.PresignURLResponse{Method: "GET"}, nil
}

// cloneArg returns a copy of a, a recorded input, that shares no pointer
// or map with it, but for the Body of a PutObjectInput, which is the same
// reader. A nil input stays nil, of its type.
func cloneArg(a any) any {
	switch v := a.(type) {
	case *storage.ListBucketsInput:
		return clonePtr(v, func(c *storage.ListBucketsInput) {
			c.ContinuationToken = cloneVal(c.ContinuationToken)
			c.MaxBuckets = cloneVal(c.MaxBuckets)
			c.Prefix = cloneVal(c.Prefix)
		})
	case *storage.HeadObjectInput:
		return clonePtr(v, cloneHead)
	case *storage.GetObjectInput:
		return clonePtr(v, func(c *storage.GetObjectInput) { cloneHead(&c.HeadObjectInput) })
	case *storage.PutObjectInput:
		return clonePtr(v, func(c *storage.PutObjectInput) {
			c.ContentLength = cloneVal(c.ContentLength)
			cloneWrite(&c.BaseObjectInput, &c.ObjectHeaders, &c.WriteCondition)
		})
	case *storage.DeleteObjectInput:
		return clonePtr(v, func(c *storage.DeleteObjectInput) {
			c.Bucket = cloneVal(c.Bucket)
			c.Version = cloneVal(c.Version)
		})
	case *storage.ListObjectsInput:
		return clonePtr(v, func(c *storage.ListObjectsInput) {
			c.Bucket = cloneVal(c.Bucket)
			c.ContinuationToken = cloneVal(c.ContinuationToken)
			c.Delimiter = cloneVal(c.Delimiter)
			c.MaxKeys = cloneVal(c.MaxKeys)
			c.Prefix = cloneVal(c.Prefix)
			c.StartAfter = cloneVal(c.StartAfter)
		})
	default:
		return cloneOther(a)
	}
}

// cloneOther returns a copy of a, a recorded input of a bucket method
// but ListBuckets or of a presigner, as cloneArg does.
func cloneOther(a any) any {
	switch v := a.(type) {
	case *storage.CreateBucketInput:
		return clonePtr(v, nil)
	case *storage.HeadBucketInput:
		return clonePtr(v, nil)
	case *storage.DeleteBucketInput:
		return clonePtr(v, nil)
	case *storage.PresignUploadURLInput:
		return clonePtr(v, func(c *storage.PresignUploadURLInput) {
			c.ContentLength = cloneVal(c.ContentLength)
			c.TTL = cloneVal(c.TTL)
			cloneWrite(&c.BaseObjectInput, &c.ObjectHeaders, &c.WriteCondition)
		})
	case *storage.PresignDownloadURLInput:
		return clonePtr(v, func(c *storage.PresignDownloadURLInput) {
			c.TTL = cloneVal(c.TTL)

			r := &c.ResponseHeaders

			r.ResponseCacheControl = cloneVal(r.ResponseCacheControl)
			r.ResponseContentDisposition = cloneVal(r.ResponseContentDisposition)
			r.ResponseContentEncoding = cloneVal(r.ResponseContentEncoding)
			r.ResponseContentLanguage = cloneVal(r.ResponseContentLanguage)
			r.ResponseContentType = cloneVal(r.ResponseContentType)
			r.ResponseExpires = cloneVal(r.ResponseExpires)

			cloneHead(&c.HeadObjectInput)
		})
	default:
		return a
	}
}

// clonePtr returns a copy of *v with deep applied to it, or nil for nil.
func clonePtr[T any](v *T, deep func(*T)) *T {
	if v == nil {
		return nil
	}

	c := *v
	if deep != nil {
		deep(&c)
	}

	return &c
}

// cloneVal returns a pointer to a copy of *v, or nil for nil.
func cloneVal[T any](v *T) *T {
	if v == nil {
		return nil
	}

	return new(*v)
}

// cloneHead makes the pointers of c its own.
func cloneHead(c *storage.HeadObjectInput) {
	c.Bucket = cloneVal(c.Bucket)
	c.PartNumber = cloneVal(c.PartNumber)
	c.Range = cloneVal(c.Range)
	c.Version = cloneVal(c.Version)
}

// cloneWrite makes the pointers and the map of the parts of a write its
// own.
func cloneWrite(b *storage.BaseObjectInput, h *storage.ObjectHeaders, w *storage.WriteCondition) {
	b.Bucket = cloneVal(b.Bucket)
	h.Metadata = maps.Clone(h.Metadata)
	h.CacheControl = cloneVal(h.CacheControl)
	h.ContentDisposition = cloneVal(h.ContentDisposition)
	h.ContentEncoding = cloneVal(h.ContentEncoding)
	h.ContentLanguage = cloneVal(h.ContentLanguage)
	h.ContentType = cloneVal(h.ContentType)
	h.Expires = cloneVal(h.Expires)
	w.IfMatch = cloneVal(w.IfMatch)
	w.IfNoneMatch = cloneVal(w.IfNoneMatch)
}
