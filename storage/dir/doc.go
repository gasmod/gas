// Package dir is an object store in a directory on a local disk, as a
// storage.Provider. It keeps the contract of the storage package, and so
// the rules of Amazon S3, for development, tests and deployments on a
// single host.
//
// Processes on that host may share one directory. [Provider] implements
// storage.Provider, gas.Service and gas.ReadyReporter.
//
// # Usage
//
// In an app, the container builds the store from [New] and calls its
// Init. By hand, with a configuration given in code:
//
//	cfg := dir.DefaultConfig()
//	cfg.Storage.Dir = "/var/lib/app/objects"
//	cfg.Storage.Bucket = "photos"
//
//	store, err := dir.New(dir.WithConfig(cfg))(nil, logger)
//	if err != nil {
//		return err
//	}
//	if err := store.Init(ctx); err != nil {
//		return err
//	}
//	defer store.Close(ctx)
//
//	_, err = store.PutObject(ctx, &storage.PutObjectInput{
//		Key:  "cats/tom.txt",
//		Body: strings.NewReader("Tom"),
//	})
//	info, err := store.HeadObject(ctx, &storage.HeadObjectInput{Key: "cats/tom.txt"})
//
// After this the bucket `photos`, which Init created, holds `cats/tom.txt`,
// and:
//
//   - info.Size is 3.
//   - info.ContentType is `application/octet-stream`.
//   - info.ETag is the hex MD5 of `Tom`.
//
// # Configuration
//
// The settings live in the `storage` section, in [Settings]:
//
//   - `storage.dir`: the path of the directory the store keeps its
//     buckets in, which must exist. It is required, unless [WithRoot]
//     gives the directory, and must then be empty. Default: none.
//   - `storage.bucket`: the configured bucket, which an input with a nil
//     Bucket names, and which Init creates when it does not exist. It
//     keeps the bucket rules of the storage package. Default: none.
//   - `storage.sweeplimit`: the most entries of the directory that Init
//     looks at when it removes the leftovers of crashed writes, at least
//     1. Default: [DefaultSweepLimit] (100000).
//
// [Provider.Init] resolves the configuration each time it runs:
//
//   - Without [WithConfig], it binds [DefaultConfig] from the
//     gas.ConfigProvider, which checks the `validate` tags. A nil
//     ConfigProvider is the error storage.ErrNoConfigProvider.
//   - With WithConfig, it takes a copy of the given Config, never changing
//     the caller's struct, and checks the same tags.
//
// Then `bucket`, when set, must be a valid bucket name, and the directory
// must come from `dir` or WithRoot, one of them. A failure wraps
// storage.ErrInvalidConfig and names the setting, never its value.
//
// [Provider.Config] returns a copy of the configuration the last
// successful Init bound, defaults included: the zero Config before the
// first, and the last one bound after Close. A failed Init leaves it as
// it was, and changing the copy changes nothing in the store. It is safe
// to call at any time, while Init or Close runs included.
//
// # Directory
//
// Init opens the directory of `storage.dir` with os.OpenRoot, and Close
// closes it; Init after Close opens it again. With WithRoot, the store
// uses the os.Root the caller opened, and never closes it: the caller
// closes it after Close. Every access goes through the os.Root, so no
// symlink in the directory leads out of it.
//
// No key or bucket name becomes a path. Each is stored under the hex
// SHA-256 of its bytes, so keys that differ only in case or in Unicode
// normalization are distinct objects on every file system, and a key such
// as `../x` names no path. Inside the directory:
//
//	<bucket>/<key>.gas.json             the metadata of each object
//	.gas/objects/<bucket>/<key>_<uuid>  the data file of each object
//	.gas/keys/<bucket>/...              the key index of each bucket
//	.gas/locks/<bucket>/<key>           the lock file of each key
//	.gas/locks/<bucket>.bucket          the lifecycle lock of each bucket
//	.gas/buckets/<bucket>               the name record of each bucket
//	.gas/layout                         the layout marker
//
// where `<bucket>` and `<key>` are hashes.
//
// The metadata file holds the key, the header fields, the user metadata,
// the ETag and the name of the object's data file.
//
// The key index spells each key in hex, 120 bytes of the key per path
// element, as a file `<hex>.k` under directories `<hex>.d`. So a listing
// reads the keys in byte order without opening an object.
//
// Directories are created with mode 0o750 and files with 0o600, before
// the umask.
//
// The layout marker holds the version of the layout. Init writes it in a
// directory that holds no bucket. It refuses, with [ErrOldLayout], a
// directory that holds buckets but no marker, as one written by an
// earlier version of the store, whose layout it does not read: copy its
// objects with that version's store.
//
// # Writes
//
// PutObject runs these steps:
//
//  1. It takes the key's lock.
//  2. It adds the key to the index, syncing each directory it creates for
//     the entry and the directory each is created in.
//  3. It writes the body to a new data file, checking its context between
//     reads, and syncs the file and its directory.
//  4. It evaluates the condition against the object stored.
//  5. It writes a new metadata file to a temp file, syncs it, and renames
//     it over the old one, which is the commit, and syncs the directory.
//  6. Last, it removes the data file of the object replaced.
//
// A reader sees the old object or the new one, never a mix. A failed
// PutObject removes what it wrote and leaves the object stored, if any, as
// it was.
//
// Once PutObject returns, the object survives a crash of the process and
// a loss of power, on a file system that honors fsync; on darwin a sync is
// F_FULLFSYNC. Out of the usual order:
//
//   - A sync of the directory that fails after the commit fails PutObject
//     with storage.ErrBackend, though the object is written: it may not
//     survive a loss of power.
//   - A data file of the object replaced that cannot be removed does not
//     fail the write. It is logged at warn level and passed to the skip
//     handler, and the next Init removes it.
//
// DeleteObject runs these steps:
//
//  1. It takes the key's lock.
//  2. It removes the metadata file, which is the commit, and syncs the
//     directory.
//  3. It removes the data file and the index entry.
//
// A delete of an object whose metadata cannot be used is an error wrapping
// [ErrCorrupt], and deletes nothing. A PutObject of the key replaces it,
// but for a directory in place of its metadata file.
//
// CopyObject runs these steps:
//
//  1. It opens the source under its key's lock, as GetObject does, and
//     gives the lock up.
//  2. It checks the source's length.
//  3. It writes the destination as PutObject does, under the destination
//     key's lock, reading the open data file as the body.
//
// So the copy is written whole or not at all, and holds the contents the
// source had when it was opened.
//
// CopyObject never holds two keys' locks at once. So copies that cross, a
// to b and b to a, or that copy an object onto itself, cannot deadlock:
// each waits only for the one key it is about to lock. It closes the
// source's data file once it has read it, before the write removes the
// data file of the object it replaces. So a copy of an object onto itself
// leaves no data file behind, on Windows too.
//
// The ETag is the hex MD5 of the contents. The store keeps no versions
// and no parts: a Version or a PartNumber is storage.ErrNotSupported.
// LastModified is the modification time of the data file, in UTC to the
// second.
//
// # Reads
//
// HeadObject and GetObject take the key's lock while they read the
// metadata and open the data file. HeadObject and GetObject of an object
// in a missing bucket are storage.ErrObjectNotFound and
// storage.ErrBucketNotFound.
//
// GetObject's Body is the open data file. It keeps reading the contents it
// opened when the object is replaced or deleted, but on Windows, where an
// open file cannot be removed. It stays readable after Close. It is an
// io.ReadSeeker, and a ranged Body seeks within the bytes selected.
//
// An object is an error wrapping ErrCorrupt and storage.ErrBackend, never
// storage.ErrObjectNotFound, when its metadata file:
//
//   - is not a regular file, such as a FIFO, a device or a symlink;
//   - is longer than 64 KiB;
//   - does not decode;
//   - holds another key;
//   - breaks a rule of the storage package (see Security);
//   - names a data file that is gone or not a regular file.
//
// # Listing
//
// A page of ListObjects walks the key index from where the page starts. It
// passes over the directories of the index outside the Prefix and before
// the page, and reads the metadata of the objects of the page only.
//
// It reads and sorts every entry of each index directory it passes,
// though, so its cost grows with the keys under the same index directory.
// Keys that share their first 120 bytes, or that are at most 120 bytes
// long, all sit in one directory, and a page with a narrow Prefix among
// many such keys reads them all. For a bucket with very many keys, use the
// s3 backend.
//
// ListObjects leaves out:
//
//   - a key whose object is gone, such as one a crash left in the index;
//   - an object that cannot be read, and an index entry whose key breaks
//     the key rules (ErrCorrupt). Both are passed to the skip handler.
//
// ListBuckets reads the name record of every bucket. It leaves out, and
// passes to the skip handler:
//
//   - a bucket whose record is missing or holds another name, which cannot
//     be named, with an error wrapping [ErrUnnamedBucket], until
//     CreateBucket of it rewrites the record;
//   - a bucket whose record is longer than 64 KiB, or holds a name that
//     breaks the bucket rules, with an error wrapping ErrCorrupt.
//
// A continuation token holds its listing's last entry sealed with a key
// that Init draws at random, so it tells nothing of the key. A token is
// good only for the Provider that issued it, until its next Init. A token
// is storage.ErrInvalidToken when it is from before a restart, from
// another Provider, such as one in another process or replica on the same
// directory, or from another listing.
//
// # Buckets
//
// CreateBucket creates the bucket's directories and name record, then its
// metadata directory, which is the commit, and syncs them. Creating a
// bucket that exists is not an error, and keeps its objects. Every bucket
// of the directory belongs to the caller, so the store never returns
// storage.ErrBucketExists.
//
// HeadBucket returns the name only. ListBuckets also returns the creation
// date, when CreateBucket first wrote the name record.
//
// DeleteBucket classifies each entry of the metadata directory under its
// key's lock, so it waits for the writes in progress of the keys the
// bucket holds:
//
//   - An object makes it storage.ErrBucketNotEmpty, and it then removes
//     nothing.
//   - What no read can use, such as metadata that cannot be used and the
//     temp files of crashed writes, is not an object. Once no object was
//     found, it is removed with the bucket and passed to the skip handler.
//   - A file in the metadata directory that the store did not write stops
//     the delete.
//
// Removing the metadata directory is the commit. The objects, keys and
// locks directories then go as far as they can, and what stays is passed
// to the skip handler.
//
// A write of a new key still reading its body is not waited for. The
// bucket is deleted under it, and the write fails with
// storage.ErrBucketNotFound, even when a bucket of the same name is
// created meanwhile.
//
// CreateBucket and DeleteBucket of one name never interleave, across
// processes too: each holds the bucket's lifecycle lock.
//
// # Crashed writes
//
// A crash can leave a temp file of metadata or of a name record, a data
// file no metadata names, or an index entry of a key with no object.
// Deletes leave directories of the key index with no entry. No read or
// listing sees them. A loss of power on a file system that does not keep
// the order of its writes can also lose the index entry of an object it
// kept, which listings then leave out.
//
// Init repairs these:
//
//   - It removes the leftovers, each under the lock of its key or bucket
//     taken without waiting, so it never touches a write in progress in
//     another process. It removes an index directory only while it is
//     empty.
//   - It makes again the index entry of an object that has none, under the
//     key's lock likewise.
//
// It looks at no more than `storage.sweeplimit` entries of the directory,
// then stops. A bucket that another Provider or process deletes while Init
// sweeps it is passed over rather than failing Init.
//
// Each leftover removed and each object indexed again is passed to the
// skip handler. Init logs:
//
//   - `leftovers of crashed writes removed` at info level, with `removed`,
//     their count;
//   - `objects with no index entry re-indexed` at info level, with
//     `reindexed`, their count;
//   - `leftover sweep stopped at its limit` at warn level, with `limit`,
//     when it stopped.
//
// # Skip handler
//
// [WithSkipHandler] sets a [SkipHandler]. It is called with the path,
// relative to the directory, of each entry the store passes over, removes
// or leaves behind, and why:
//
//   - an object a listing leaves out (ErrCorrupt);
//   - a bucket ListBuckets leaves out (ErrUnnamedBucket);
//   - the debris and the leftovers of DeleteBucket;
//   - the leftovers Init removes, and the objects it indexes again;
//   - a data file PutObject or CopyObject could not remove;
//   - a file of the key index the store did not write.
//
// The path names hashes, never a key or a bucket name. They are SHA-256
// hashes with no salt, though, so one who can guess a key or a bucket name
// can confirm it from a path: keep the paths as private as the keys.
//
// The handler is called on the goroutine of the call, after the call gave
// up every lock it took, so it may call the store. A call during Init
// fails with storage.ErrNotInitialized or storage.ErrClosed. Calls in
// flight on several goroutines call it concurrently. A listing reports an
// entry each time it passes over it.
//
// # Lifecycle
//
// A successful Init logs `service initialized` at info level, with
// `bucket`, whether a bucket is configured, and `own_root`, whether the
// store opened the directory. It never logs a path or the bucket's name.
//
// [Provider.Close] makes the store closed, waits for the calls in flight,
// closes the directory it opened, and logs `service closed` at info
// level. When its context ends first, it cancels them, which return
// storage.ErrClosed, logs `service closed, calls in flight canceled` at
// warn level, and returns an error wrapping the context's error.
//
// Close on a store that is not initialized, or on a nil Provider, returns
// nil and logs nothing. The buckets and objects outlive Close, in the
// directory.
//
// [New]'s constructor fails with:
//
//   - storage.ErrNilLogger for a nil logger;
//   - storage.ErrNilOption for a nil option, naming its position;
//   - storage.ErrNilConfig for WithConfig(nil);
//   - [ErrNilRoot] for WithRoot(nil).
//
// A nil ConfigProvider is accepted, and Init then requires WithConfig.
//
// # Readiness
//
// [Provider.CheckReady] checks that the directory still holds the layout
// marker, which fails once the directory is removed.
//
// # Concurrency
//
// A Provider is safe for concurrent use, and so are several Providers on
// one directory, in one process or several.
//
// The operations of one key are serialized by a lock per key, made of:
//
//   - an OS file lock, flock or LockFileEx on Windows, on the key's lock
//     file, which excludes other processes;
//   - an in-process slot, shared by every call of the Provider, so that at
//     most one goroutine per key waits in the blocking lock call however
//     many calls wait for the key.
//
// A wait honors the call's context. When the context ends, the call
// returns, and the goroutine that waits in the lock call stays until the
// holder gives the lock up, then gives it up itself.
//
// The conditions of PutObject are evaluated under the key's lock, so they
// hold across processes. CopyObject takes the source's lock, then the
// destination's, never both at once.
//
// A PutObject holds its key's lock while it reads its body, and a
// CopyObject the destination's while it reads the source. So a slow body
// holds the other calls on its key, and a DeleteBucket of its bucket,
// until it ends or its context does.
//
// # Security
//
// No error the store returns and no log it writes holds a key, a bucket
// name, metadata, a header value, a path or a token. An error of a file
// operation gives its cause only, such as `no such file or directory`.
// The operating system words the cause, so on Windows the same failure
// reads `The system cannot find the file specified.`
//
// The files of the directory are read back as untrusted:
//
//   - A metadata file, a data file and a name record are opened only when
//     they are regular files, never through a symlink at their name, and
//     without blocking on a FIFO or a device.
//   - A metadata file or a name record is read no further than 64 KiB.
//   - A data file a metadata file names must be one the store could have
//     written for its key, and its key must hash to its name, so a forged
//     metadata file names no file outside its object's.
//   - Everything else a metadata file holds must keep the rules a write
//     keeps: the key rules, an ETag of 32 lower case hex digits, the header
//     rules and the metadata rules, with the names in lower case.
//   - A key in the key index must keep the key rules, and a name record a
//     valid bucket name.
//
// The directory, and every directory in it, must not be writable by anyone
// the app does not trust: a hard link in it cannot be told from a file.
//
// # Errors
//
// The sentinel errors of this package, to be tested with [errors.Is]:
//
//   - [ErrNilRoot]: WithRoot was given a nil root.
//   - [ErrCorrupt]: an object cannot be read, as under Reads.
//   - [ErrUnnamedBucket]: a bucket cannot be named, as under Listing.
//   - [ErrOldLayout]: the directory holds buckets of an earlier layout.
//
// The other errors wrap a sentinel of the storage package, as the storage
// package describes. Every error starts with [ServiceName] and the
// operation. A configuration that fails its tags gets the validator's
// error after `gas/storage/dir: init: invalid config: validate config: `.
// The other errors, as returned, with `put object`, `get object` and
// `delete bucket` standing for each operation that can fail the same way,
// `copy object` included:
//
//	gas/storage/dir: logger is nil
//	gas/storage/dir: option is nil: at position 0
//	gas/storage/dir: config is nil
//	gas/storage/dir: root is nil
//	gas/storage/dir: init: no config provider
//	gas/storage/dir: init: invalid config: Storage.Bucket must be a valid bucket name
//	gas/storage/dir: init: invalid config: Storage.Dir is required without WithRoot
//	gas/storage/dir: init: invalid config: Storage.Dir must be empty with WithRoot
//	gas/storage/dir: init: backend failed: open dir: no such file or directory
//	gas/storage/dir: init: backend failed: the root holds buckets of an earlier layout
//	gas/storage/dir: init: service already initialized
//	gas/storage/dir: init: context canceled
//	gas/storage/dir: ready: service not initialized
//	gas/storage/dir: ready: backend failed: stat layout marker: no such file or directory
//	gas/storage/dir: put object: input is nil
//	gas/storage/dir: put object: service not initialized
//	gas/storage/dir: put object: service closed
//	gas/storage/dir: put object: context canceled
//	gas/storage/dir: put object: invalid key: empty
//	gas/storage/dir: put object: bucket not found
//	gas/storage/dir: put object: precondition failed
//	gas/storage/dir: put object: object not found
//	gas/storage/dir: copy object: source: invalid key: empty
//	gas/storage/dir: copy object: invalid argument: copies an object onto itself without Replace
//	gas/storage/dir: get object: object not found
//	gas/storage/dir: get object: object not found: bucket not found
//	gas/storage/dir: get object: not supported: the store keeps no versions
//	gas/storage/dir: get object: not supported: the store keeps no parts
//	gas/storage/dir: get object: range not satisfiable
//	gas/storage/dir: get object: backend failed: object cannot be read: decode meta file: syntax error at offset 1
//	gas/storage/dir: get object: backend failed: object cannot be read: data file is gone
//	gas/storage/dir: delete bucket: bucket not empty
//	gas/storage/dir: delete bucket: backend failed: meta dir: file this backend did not write
//	gas/storage/dir: list objects: invalid continuation token: not one this backend issued for this listing
//	gas/storage/dir: close: context deadline exceeded
package dir
