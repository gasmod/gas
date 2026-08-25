package storage

import "errors"

// Sentinel errors returned by StorageProvider implementations.
var (
	// ErrKeyNotFound is returned by Download or Head when the
	// requested key does not exist.
	ErrKeyNotFound = errors.New("storage: key not found")

	// ErrClosed is returned when an operation is attempted on a closed
	// storage service.
	ErrClosed = errors.New("storage: service is closed")

	// ErrBucketRequired is returned when an operation is invoked without a
	// bucket configured on the service and without gas.InBucket() supplied
	// in the call options.
	ErrBucketRequired = errors.New("storage: bucket required (set Storage.Bucket or pass gas.InBucket())")
)
