// Package storagetest holds mocks of storage.Provider, [MockProvider], and
// of storage.Presigner, [MockPresigner], for tests of code that stores
// objects.
//
// # Usage
//
// A mock records each call, and returns what the function in the field
// named after the method returns:
//
//	m := &storagetest.MockProvider{
//		GetObjectFn: func(context.Context, *storage.GetObjectInput) (*storage.Object, error) {
//			return nil, errOffline
//		},
//	}
//	_, err := m.GetObject(ctx, &storage.GetObjectInput{})
//	// err is errOffline, and m.CallCount("GetObject") is 1
//
// A mock keeps none of the storage package's rules: it checks no key,
// stores nothing and reads no body. To test against those rules without a
// server, use gas/storage/memory.
//
// # Defaults
//
// With a field nil, its method succeeds with a zero output, never a nil
// one: CreateBucket, DeleteBucket and DeleteObject return nil; HeadBucket
// a Bucket with the name asked for; ListBuckets and ListObjects an empty
// page; PutObject a zero PutObjectOutput, without reading the body;
// CopyObject a zero CopyObjectOutput; PresignUploadURL and
// PresignDownloadURL a PresignURLResponse with only its Method, PUT or
// GET. HeadObject and GetObject return
// storage.ErrObjectNotFound, as a store that holds no object does, so that
// code under test never gets a nil object with a nil error.
//
// # Calls
//
// Each method appends a [Call] with its name and a copy of its input,
// before it calls its field. A copy shares no pointer or map with the
// caller's input, so a caller that reuses an input, as a listing loop
// does with its ContinuationToken, does not change a recorded call; the
// Body of a PutObjectInput is the same reader. A nil input is recorded as
// a nil pointer of its type. Calls returns a deep copy of the calls in the
// order they were made, so that changing what it returns does not change
// them either, CallCount counts those of one method, and Reset forgets
// them all and keeps the fields.
//
// The methods may be called from several goroutines at once, as the calls
// are kept under a lock, which Calls, CallCount and Reset take too.
// Setting a field while a method runs is a data race, and a nil mock
// panics.
package storagetest
