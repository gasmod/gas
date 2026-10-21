// Package storagetest provides recording mocks of storage.Provider and
// storage.Presigner for testing code that depends on object storage without
// a backend.
//
// Each mock method records the call in Calls and then delegates to the
// matching Fn field when it is set, or returns nil values (nil, nil, or nil
// for DeleteBucket) when it is not:
//
//	mock := &storagetest.MockProvider{
//		GetObjectFn: func(ctx context.Context, in *storage.GetObjectInput) (*storage.Object, error) {
//			return nil, storage.ErrKeyNotFound
//		},
//	}
//	gas.WithServiceInstance[storage.Provider](mock)
//
// A mock shows that the caller is wired correctly and handles what the
// backend returns; it does not show that a backend behaves that way. The
// storage sentinels a real backend returns are documented in package storage.
//
// Recording is safe for concurrent calls. Calls is read directly, so read it
// only after the calls under test have finished, or use CallCount, which
// locks. Reset clears the recorded calls but keeps the Fn fields. The mocks
// register nothing themselves and have no lifecycle.
//
// SPDX-License-Identifier: MIT
package storagetest
