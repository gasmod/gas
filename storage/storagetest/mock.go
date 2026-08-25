// Package storagetest provides a mock implementation of gas.StorageProvider
// for use in tests. The mock records all calls and allows configuring
// per-method behavior via function fields.
//
//	mock := &storagetest.MockStorage{}
//	mock.UploadFn = func(ctx context.Context, key string, data io.Reader, opts ...gas.StorageOption) error {
//	    return nil
//	}
package storagetest

import (
	"context"
	"io"
	"sync"
	"time"

	"github.com/gasmod/gas"
)

// MockStorage is a configurable mock of gas.StorageProvider. Each method
// delegates to its corresponding Fn field if set, otherwise returns the
// zero value. All calls are recorded in the Calls slice for assertions.
type MockStorage struct {
	UploadFn             func(ctx context.Context, key string, data io.Reader, opts ...gas.StorageOption) error
	DownloadFn           func(ctx context.Context, key string, opts ...gas.StorageOption) (*gas.StorageObject, error)
	DeleteFn             func(ctx context.Context, key string, opts ...gas.StorageOption) error
	HeadFn               func(ctx context.Context, key string, opts ...gas.StorageOption) (*gas.ObjectInfo, error)
	PresignDownloadURLFn func(ctx context.Context, key string, expires time.Duration, opts ...gas.StorageOption) (string, error)
	PresignUploadURLFn   func(ctx context.Context, key string, expires time.Duration, opts ...gas.StorageOption) (string, error)
	CheckReadyFn         func(ctx context.Context) error
	Calls                []Call

	mu sync.Mutex
}

var _ gas.StorageProvider = (*MockStorage)(nil)
var _ gas.ReadyReporter = (*MockStorage)(nil)

// Call records a single method invocation on the mock.
type Call struct {
	Method string
	Args   []any
}

func (m *MockStorage) record(method string, args ...any) {
	m.mu.Lock()
	m.Calls = append(m.Calls, Call{Method: method, Args: args})
	m.mu.Unlock()
}

// Upload records the call and delegates to UploadFn if set.
func (m *MockStorage) Upload(ctx context.Context, key string, data io.Reader, opts ...gas.StorageOption) error {
	m.record("Upload", key, data)
	if m.UploadFn != nil {
		return m.UploadFn(ctx, key, data, opts...)
	}
	return nil
}

// Download records the call and delegates to DownloadFn if set.
func (m *MockStorage) Download(ctx context.Context, key string, opts ...gas.StorageOption) (*gas.StorageObject, error) {
	m.record("Download", key)
	if m.DownloadFn != nil {
		return m.DownloadFn(ctx, key, opts...)
	}
	return nil, nil
}

// Delete records the call and delegates to DeleteFn if set.
func (m *MockStorage) Delete(ctx context.Context, key string, opts ...gas.StorageOption) error {
	m.record("Delete", key)
	if m.DeleteFn != nil {
		return m.DeleteFn(ctx, key, opts...)
	}
	return nil
}

// Head records the call and delegates to HeadFn if set.
func (m *MockStorage) Head(ctx context.Context, key string, opts ...gas.StorageOption) (*gas.ObjectInfo, error) {
	m.record("Head", key)
	if m.HeadFn != nil {
		return m.HeadFn(ctx, key, opts...)
	}
	return nil, nil
}

// PresignDownloadURL records the call and delegates to PresignDownloadURLFn if set.
func (m *MockStorage) PresignDownloadURL(ctx context.Context, key string, expires time.Duration, opts ...gas.StorageOption) (string, error) {
	m.record("PresignDownloadURL", key, expires)
	if m.PresignDownloadURLFn != nil {
		return m.PresignDownloadURLFn(ctx, key, expires, opts...)
	}
	return "", nil
}

// PresignUploadURL records the call and delegates to PresignUploadURLFn if set.
func (m *MockStorage) PresignUploadURL(ctx context.Context, key string, expires time.Duration, opts ...gas.StorageOption) (string, error) {
	m.record("PresignUploadURL", key, expires)
	if m.PresignUploadURLFn != nil {
		return m.PresignUploadURLFn(ctx, key, expires, opts...)
	}
	return "", nil
}

// CheckReady records the call and delegates to CheckReadyFn if set.
func (m *MockStorage) CheckReady(ctx context.Context) error {
	m.record("CheckReady")
	if m.CheckReadyFn != nil {
		return m.CheckReadyFn(ctx)
	}
	return nil
}

// Reset clears all recorded calls.
func (m *MockStorage) Reset() {
	m.mu.Lock()
	m.Calls = nil
	m.mu.Unlock()
}

// CallCount returns the number of times the given method was called.
func (m *MockStorage) CallCount(method string) int {
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
