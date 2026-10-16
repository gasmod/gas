// Package storage defines the object storage contract for the Gas ecosystem:
// the Provider and Presigner interfaces, their input and output types, and
// the sentinel errors every backend reports. It holds no implementation.
// Backends live in sub-packages: s3 (AWS S3 and S3-compatible services such
// as MinIO and LocalStack) and dir (a local directory). The storagetest
// sub-package provides recording mocks of both interfaces.
//
// # Role in the framework
//
// The interfaces live in this package, not in the gas core, because no other
// module uses them. An app registers a backend under the interface it wants
// and depends on the interface, so swapping S3 for a local directory in
// development is a registration change:
//
//	gas.WithSingletonService[storage.Provider](s3.NewProvider()),
//	gas.WithSingletonService[storage.Presigner](s3.NewPresigner()),
//
// Provider creates buckets and reads, writes, inspects, and deletes objects.
// Presigner issues time-limited URLs that let a client upload or download one
// object directly against the backend, without going through the app.
//
// # Inputs
//
// Object operations take an input that embeds BaseObjectInput. Its Key is the
// object key, a plain string; every backend rejects an empty key, a key
// longer than 1024 bytes, a key that is not valid UTF-8, and a key whose last
// path element is "." or that cleans to "." with ErrInvalidKey, and a backend
// may reject more. Only "/" separates path elements, on every platform, as
// in S3: a backslash is an ordinary byte, so "a\." is valid everywhere and
// "a/." is invalid everywhere.
// Bucket is optional: nil selects the bucket the backend was configured with,
// and a non-nil pointer must name a valid S3 bucket name (an empty string is
// not a fallback) or the call fails with ErrInvalidBucket. Optional fields
// are pointers so that "not set" differs from the zero value. A nil input
// fails with ErrInputRequired.
//
// # Errors
//
// Every error an operation returns names the backend and the operation, in the
// form "gas/storage/<backend>/<component>: <operation>: <cause>", and wraps
// one of the sentinels in this package when one applies, so callers branch
// with errors.Is. Backend-specific causes, such as an AWS SDK error, stay in
// the chain and are reachable with errors.As. The storage packages do not put
// caller-supplied object keys or metadata values into the messages they
// build; a cause produced by third-party code can still carry them (see the
// s3 package). An application must treat error text as possibly containing
// the bucket and key, for example before logging it or returning it to a
// client.
//
// HeadObject reports a missing object as ErrNotFound and GetObject reports it
// as ErrKeyNotFound; a missing bucket is ErrBucketNotFound, except that S3
// answers HEAD on a missing bucket exactly as on a missing key, so the s3
// backend's HeadObject reports ErrNotFound for both. DeleteObject of a missing
// key succeeds. On s3, GetObject of a Version that does not exist is
// ErrVersionNotFound, which matches neither ErrKeyNotFound nor ErrNotFound;
// HeadObject of one is ErrNotFound, because HEAD responses carry no error
// code; and a malformed version ID may be rejected by AWS with an error that
// carries no sentinel. The dir backend does not version objects and rejects
// any Version with ErrNotSupported.
//
// CreateBucket of a bucket the caller already owns succeeds in every backend
// and region. ErrBucketExists means the name is taken by someone else, which
// only the s3 backend can report.
//
// Presigner methods reject a TTL that is negative, under one second, or over
// seven days with ErrInvalidTTL. A GetObject or HeadObject Range selects part
// of the object, and Size is then the length of that part. A Range that cannot
// be satisfied (its first byte at or past the end, or "bytes=-0") is
// ErrInvalidRange in both backends. dir applies RFC 9110 single-range
// semantics and ignores any other header; the s3 backend passes the header to
// S3, which agrees on valid single ranges but may accept some malformed ones.
// An input that asks for a feature the backend does not implement, such as a
// Version or PartNumber on the dir backend, returns ErrNotSupported. A
// PutObject whose ContentLength is set and does not match the body returns
// ErrContentLengthMismatch in both backends and stores nothing. When the
// bucket is also missing, the error may be ErrBucketNotFound or
// ErrContentLengthMismatch depending on the backend and the object's size (see
// each backend); callers must not rely on the order. Backend constructors
// given a nil option return ErrInvalidOption instead of panicking.
//
// # Lifecycle
//
// The interfaces define no start or close step. A backend is ready when its
// constructor returns; a zero value that was not built by its constructor
// returns ErrNotInitialized from every method instead of panicking.
//
// SPDX-License-Identifier: MIT
package storage
