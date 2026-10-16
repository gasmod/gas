// Package dir provides a filesystem-backed implementation of
// storage.Provider: buckets and objects stored in a directory on local disk,
// for development, tests and single-host deployments.
//
// # Wiring
//
// NewProvider returns a constructor for the gas service container:
//
//	gas.WithSingletonService[storage.Provider](dir.NewProvider(dir.WithRoot(root)))
//
// The constructor has the shape func(gas.ConfigProvider) (*Provider, error):
// the container passes it the registered gas.ConfigProvider, and it binds
// Config from that, so the config
// key storage.bucket sets the default bucket used when an input's Bucket is
// nil. WithRoot is required and names the os.Root the provider stores into.
// The provider never closes the root: whoever opened it closes it once the
// provider is no longer used. NewProvider creates the internal directories
// in the root; it creates no bucket. Buckets are created with CreateBucket.
//
// # Layout
//
// Every bucket name and key is stored under the hex SHA-256 of its bytes, so
// a key is an opaque string, as in S3: "../x", "/abs", "a/b/" and keys that
// differ only in case or Unicode normalization are distinct objects on every
// filesystem, and no key can name a path. Key validation is the same on
// every platform: only "/" separates path elements, so a backslash is an
// ordinary byte on Windows too. Inside the root:
//
//	<bucket-hash>/<key-hash>.gas.json            metadata of each object
//	.gas/objects/<bucket-hash>/<key-hash>_<uuid> data file of each object
//	.gas/locks/<bucket-hash>/<key-hash>          lock file of each object
//
// The metadata file holds the key, content type, user metadata and the name
// of the object's current data file. A Put writes a new data file, then
// renames a new metadata file over the old one; that rename is the commit
// point, so readers see the old object or the new one, never a mix, and a
// failed Put leaves the previous object as it was. The replaced data file is
// removed after the commit. Directories are created with mode 0o750 and
// files with 0o600, before umask.
//
// All access goes through the os.Root, so symlinks in the storage directory
// cannot lead outside it.
//
// # Concurrency
//
// Operations on one key are serialized with a lock per key: an OS file lock
// (flock, or LockFileEx on Windows) on its lock file, which also excludes
// other processes sharing the directory, plus an in-process slot so that at
// most one goroutine per key waits in the blocking lock call. Waiting honors
// the context: a canceled wait returns an error wrapping ctx.Err(). When the
// lock is held by another process, the call returns on cancellation but its
// goroutine stays blocked in the lock system call until that process
// releases the lock; it then releases the lock itself and exits. A Get
// returns a body that keeps reading the data it opened even if the object
// is overwritten or deleted meanwhile (not on Windows, where an open file
// cannot be removed).
//
// # Lifecycle
//
// The provider holds no open files between calls and has nothing to close.
// Each call opens the bucket's directories and closes them before it
// returns, except the data file a Get hands to the caller as Body. The only
// goroutines it starts belong to a lock wait: one waits in the blocking lock
// call while the caller waits on its context, and when the context ends
// first it is left to finish the call and release the lock.
//
// # Errors
//
// Every error is prefixed "gas/storage/dir/provider: <op>: ", with op one of
// create bucket, head, put, get or delete, and wraps the storage sentinel a
// caller can act on:
//
//   - storage.ErrNotInitialized: a Provider that NewProvider did not build,
//     from every method. NewProvider returns ErrRootRequired, which wraps it,
//     when no root was given, including WithRoot(nil).
//   - storage.ErrInvalidOption: NewProvider given a nil Option, with its
//     zero-based position in the message
//     ("gas/storage/dir/provider: storage: invalid option: option 0 is nil").
//     Nothing is created in the root.
//   - storage.ErrInputRequired: a nil input.
//   - storage.ErrInvalidKey and storage.ErrInvalidBucket: a key or bucket
//     name that fails validation, checked before any disk access.
//   - storage.ErrBucketNotFound: a missing bucket, from Head, Put, Get and
//     Delete.
//   - storage.ErrNotFound from Head, and storage.ErrKeyNotFound from Get: a
//     missing object. Each matches only its own sentinel, as with s3.
//   - storage.ErrInvalidRange: a Head or Get whose Range cannot be satisfied.
//   - storage.ErrNotSupported: a Head, Get or Delete with Version set,
//     checked after the key and bucket and before any disk access. This
//     backend does not version objects.
//   - ErrStaleData: a Put that wrote the object but could not remove the data
//     file it replaced. Put returns the ObjectInfo with this error.
//
// CreateBucket of an existing bucket succeeds and keeps its objects: every
// bucket under the root belongs to the caller, so this backend never returns
// storage.ErrBucketExists. Deleting a missing object succeeds. Metadata that cannot be used (it does
// not decode, names no valid data file, or names a missing one) is reported
// as an error matching none of the not-found sentinels; a Put over it
// replaces it. Errors never quote a key, a bucket name given to an
// operation, or metadata; only hashes of them appear.
//
// # Differences from s3
//
// Objects are not versioned: Version on a Head, Get or Delete is
// storage.ErrNotSupported. Range is honored by Get and Head with RFC 9110
// single-range semantics: one "bytes=first-last", "bytes=first-" or
// "bytes=-suffix" range is selected, clamped to the object, and Size is its
// length; anything else is ignored and the whole object is returned; a first
// byte at or past the end, or "bytes=-0", is storage.ErrInvalidRange. S3 is
// more lenient than RFC 9110 on some malformed headers (LocalStack, for
// example, accepts a unit other than bytes, spaces around "=" or "-", and a
// "+" sign), and on an empty object it may answer 416 where this backend
// returns the empty body; dir follows RFC 9110. PartNumber and the Response*
// fields of reads, and ContentLength and Expires of Put, are ignored.
// ObjectInfo carries no Version, and Put and Delete return an empty
// ObjectInfo. A Put without a content type sniffs one from the body with
// http.DetectContentType. Metadata keys keep their case, and invalid UTF-8 in
// metadata is replaced with U+FFFD when it is stored.
//
// SPDX-License-Identifier: MIT
package dir
