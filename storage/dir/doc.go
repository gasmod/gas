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
//	.gas/buckets/<bucket-hash>                   name of each bucket
//
// The metadata file holds the key, content type, user metadata and the name
// of the object's current data file. The bucket's name record holds its
// name, for ListBuckets; CreateBucket writes it once, before the bucket
// exists. A Put writes a new data file, then
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
// create bucket, head bucket, delete bucket, list buckets, head, put, get,
// delete or list, and wraps the storage sentinel a caller can act on:
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
//   - storage.ErrBucketNotFound: a missing bucket, from Head, Put, Get,
//     Delete, ListObjects, HeadBucket and DeleteBucket.
//   - storage.ErrPreconditionFailed: a Put whose WriteCondition does not
//     hold, checked under the object's lock, so across processes too. An
//     object with no recorded ETag (written by an earlier version) or with
//     corrupt metadata matches no IfMatch; corrupt metadata also fails
//     IfNoneMatch. storage.ErrKeyNotFound: a Put with IfMatch of a missing
//     key. storage.ErrInvalidCondition: a condition that cannot be
//     evaluated, before any disk access.
//   - storage.ErrBucketNotEmpty: a DeleteBucket of a bucket that holds an
//     object, which then removes nothing. Entries no read can use
//     (unusable metadata, metadata whose data file is gone, temp files of
//     crashed writes) are not objects: DeleteBucket removes them with the
//     bucket once it found no object, so they can never make a bucket
//     undeletable.
//   - storage.ErrBucketNotFound from Head, Put, Get, Delete and ListObjects,
//     too, when DeleteBucket removes the bucket while the call runs, such
//     as a Put of a new key still copying its body; it holds even if a
//     bucket of the same name is created again meanwhile.
//   - storage.ErrInvalidLimit and storage.ErrInvalidContinuationToken: a
//     list page size out of range, or a continuation token that is empty or
//     that this backend did not issue.
//   - storage.ErrNotFound from Head, and storage.ErrKeyNotFound from Get: a
//     missing object. Each matches only its own sentinel, as with s3.
//   - storage.ErrInvalidRange: a Head or Get whose Range cannot be satisfied.
//   - storage.ErrNotSupported: a Head, Get or Delete with Version set, or a
//     Head or Get with PartNumber set, checked after the key and bucket and
//     before any disk access. This backend neither versions objects nor
//     stores them in parts.
//   - storage.ErrContentLengthMismatch: a Put whose Body does not yield
//     exactly ContentLength bytes, or whose ContentLength is negative. Nothing
//     is stored and no data file is left behind.
//   - ErrStaleData: a Put that wrote the object but could not remove the data
//     file it replaced. Put returns the ObjectInfo with this error.
//
// CreateBucket of an existing bucket succeeds and keeps its objects: every
// bucket under the root belongs to the caller, so this backend never returns
// storage.ErrBucketExists. Deleting a missing object succeeds. An object
// whose metadata cannot be used (it does not decode, holds another key,
// names no valid data file, or names a missing one) is reported by Head and
// Get as an error wrapping ErrCorrupt, which matches none of the not-found
// sentinels; a Put over it replaces it. Errors never quote a key, a bucket name given to an
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
// returns the empty body; dir follows RFC 9110. PartNumber is
// storage.ErrNotSupported. The Response* fields of reads are ignored. Every
// header of a Put is stored as given and read back, Expires included, which
// may be any text, where s3 accepts only an IMF-fixdate. ObjectInfo carries no
// Version. Put returns the object as Head then reads it: its ETag, the hex
// MD5 of the body computed as it is written, its size and modification time,
// content type, metadata and headers. Delete returns an empty ObjectInfo. An
// object stored by a version of this backend that recorded no ETag reads
// back with an empty one, and with no headers. A Put without a content type
// sniffs one from the body with http.DetectContentType. Metadata keys keep
// their case, and invalid UTF-8 in metadata or a header is replaced with
// U+FFFD when it is stored.
//
// CreateBucket and DeleteBucket of the same name never interleave, across
// processes too: each holds a lock file named after the bucket under
// .gas/locks for its whole run.
//
// GetObject's Body implements io.ReadSeeker, for whole and ranged reads: a
// ranged Body seeks within the bytes it returns. The storage interface
// promises only io.ReadCloser, and s3 makes no such promise.
//
// Listing reads every name in the bucket (or every bucket's name record) on
// each call, so a page costs time in proportion to the bucket, not the page.
// Its continuation tokens hold the last name of the page, encoded; they are
// not interchangeable with S3's. A bucket's CreationDate is when CreateBucket
// first wrote its name record; CreateBucket and ListBuckets report it with
// the Name, and leave ID and Region empty. A bucket whose name record is missing or does
// not hold its name (one created by an earlier version, or a record a crash
// left empty) cannot be named and is left out of ListBuckets until
// CreateBucket of it runs again, which rewrites the record; its objects stay
// reachable through the other methods meanwhile. A record that cannot be
// read at all, or that is not a regular file, fails the call. Likewise an
// object that cannot be read (see ErrCorrupt) is left out of ListObjects,
// while an I/O error reading the metadata still fails it.
//
// What a listing leaves out is counted in its Skipped, which covers the
// whole bucket (or every bucket) and so repeats on every page; a
// ListObjects page also counts any of its own objects damaged between the
// scan and the read of its summary. To find
// what and where, give NewProvider WithSkipHandler: it gets each entry's
// path relative to the root and an error wrapping ErrCorrupt or
// ErrUnnamedBucket, and also hears from DeleteBucket about the debris it
// removes and anything it leaves behind. Finding an object that cannot be
// read costs one stat of its data file per object per page, on top of
// reading its metadata.
//
// SPDX-License-Identifier: MIT
package dir
