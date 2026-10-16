package storagetest

import (
	"context"
	"sync"

	"github.com/gasmod/gas/storage"
)

// MockPresigner is a recording mock of storage.Presigner. Each method records
// its call and delegates to the matching Fn field, or returns nil, nil when
// that field is nil. The zero value is ready to use.
type MockPresigner struct {
	PresignUploadURLFn func(
		ctx context.Context,
		input *storage.PresignUploadURLInput,
	) (*storage.PresignURLResponse, error)

	PresignDownloadURLFn func(
		ctx context.Context,
		input *storage.PresignDownloadURLInput,
	) (*storage.PresignURLResponse, error)

	Calls []Call
	mu    sync.Mutex
}

var _ storage.Presigner = (*MockPresigner)(nil)

// record appends a call to Calls under the mock's lock.
func (m *MockPresigner) record(method string, args ...any) {
	m.mu.Lock()
	m.Calls = append(m.Calls, Call{Method: method, Args: args})
	m.mu.Unlock()
}

// PresignUploadURL records the call and delegates to PresignUploadURLFn.
func (m *MockPresigner) PresignUploadURL(
	ctx context.Context,
	input *storage.PresignUploadURLInput,
) (*storage.PresignURLResponse, error) {
	m.record("PresignUploadURL", input)
	if m.PresignUploadURLFn != nil {
		return m.PresignUploadURLFn(ctx, input)
	}
	return nil, nil
}

// PresignDownloadURL records the call and delegates to PresignDownloadURLFn.
func (m *MockPresigner) PresignDownloadURL(
	ctx context.Context,
	input *storage.PresignDownloadURLInput,
) (*storage.PresignURLResponse, error) {
	m.record("PresignDownloadURL", input)
	if m.PresignDownloadURLFn != nil {
		return m.PresignDownloadURLFn(ctx, input)
	}
	return nil, nil
}

// Reset clears the recorded calls. The Fn fields are kept.
func (m *MockPresigner) Reset() {
	m.mu.Lock()
	m.Calls = nil
	m.mu.Unlock()
}

// CallCount returns how many times the named method was called.
func (m *MockPresigner) CallCount(method string) int {
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
