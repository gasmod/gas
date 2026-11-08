// Package memory is an object store held in the process's memory, as a
// storage.Provider. It keeps the contract of the storage package, and so
// the rules of Amazon S3, without a server or a disk.
//
// It suits tests and local development. The store is local to one process
// and lost when the process stops. [Provider] implements storage.Provider,
// gas.Service and gas.ReadyReporter.
//
// # Usage
//
// In an app, the container builds the store from [New] and calls its
// Init. By hand, with a configuration given in code:
//
//	cfg := memory.DefaultConfig()
//	cfg.Storage.Bucket = "photos"
//
//	store, err := memory.New(memory.WithConfig(cfg))(nil, logger)
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
//   - `storage.bucket`: the configured bucket, which an input with a nil
//     Bucket names, and which Init creates when it does not exist. It
//     keeps the bucket rules of the storage package. Default: none.
//   - `storage.maxbytes`: the most bytes the objects of the store hold,
//     counting each object's key, contents, header values and metadata
//     names and values, at least [MinMaxBytes] (1 MiB). Default:
//     [DefaultMaxBytes] (256 MiB).
//   - `storage.maxobjects`: the most objects the store holds, across its
//     buckets, at least 1. Default: [DefaultMaxObjects] (100000).
//
// The store holds at most [MaxBuckets] buckets (10000), which is not a
// setting.
//
// [Provider.Init] resolves the configuration each time it runs:
//
//   - Without [WithConfig], it binds [DefaultConfig] from the
//     gas.ConfigProvider, which checks the `validate` tags. A nil
//     ConfigProvider is the error storage.ErrNoConfigProvider.
//   - With WithConfig, it takes a copy of the given Config, never changing
//     the caller's struct, and checks the same tags.
//
// Then `bucket`, when set, must be a valid bucket name. A failure wraps
// storage.ErrInvalidConfig and names the setting, never its value.
//
// [Provider.Config] returns a copy of the configuration the last
// successful Init bound, defaults included: the zero Config before the
// first, and the last one bound after Close. A failed Init leaves it as
// it was, and changing the copy changes nothing in the store. It is safe
// to call at any time, while Init or Close runs included.
//
// # Store
//
// The buckets and their objects belong to the Provider, not to one Init.
// Close keeps them, and Init after Close finds them as Close left them.
//
// These fail with [ErrStorageFull] and change nothing:
//
//   - a write that would take the store past `maxobjects` objects or
//     `maxbytes` bytes;
//   - a CreateBucket past MaxBuckets;
//   - an Init whose configured bucket would be one past MaxBuckets.
//
// The store keeps no versions and no parts: a Version or a PartNumber is
// storage.ErrNotSupported.
//
// # Writes
//
// PutObject reads the body whole, outside the store's lock. It then stores
// the object under the lock, where it evaluates the condition and the
// limits.
//
// As it reads, it reserves room for each chunk of the body under the lock.
// It fails with [ErrStorageFull] as soon as the objects stored and the
// bodies every PutObject in flight has read so far would hold more than
// `maxbytes` bytes, so that writes in flight cannot hold more memory than
// the limit between them. A write that replaces an object thus needs room
// for both until it is stored.
//
// The store keeps a copy of the body as long as the body, so it holds the
// bytes it counts. The ETag is the hex MD5 of the contents.
//
// CopyObject looks up the source, then the destination's bucket, and
// stores the copy, all under the store's lock. So it sees the source as
// one write stored it, and stores the copy whole. The copy shares the
// source's contents, which no write changes, but counts toward `maxbytes`
// and `maxobjects` as an object of its own. A copy past them fails with
// ErrStorageFull and writes nothing.
//
// # Reads
//
// HeadObject and GetObject of an object in a missing bucket are
// storage.ErrObjectNotFound and storage.ErrBucketNotFound. HeadBucket
// returns the name, and the region is always empty.
//
// GetObject's Body is also an io.Seeker over the bytes selected. It shares
// the stored bytes, which no write changes, so it stays readable:
//
//   - after Close;
//   - after the object is replaced or deleted;
//   - after Body is closed. Its Close does nothing.
//
// The metadata a call returns is a copy.
//
// # Listing
//
// A continuation token holds its listing's last entry sealed with a key
// that Init draws at random, so a token from before a restart is
// storage.ErrInvalidToken.
//
// A page of ListObjects sorts the keys of the bucket that start with
// Prefix, so it costs as much as the bucket holds under Prefix.
//
// # Lifecycle
//
// A successful Init logs `service initialized` at info level, with
// `bucket`, whether a bucket is configured, `max_bytes` and
// `max_objects`. It never logs the bucket's name.
//
// [Provider.Close] makes the store closed, waits for the calls in flight,
// such as a PutObject that reads its body, and logs `service closed` at
// info level. When its context ends first, it cancels them, logs
// `service closed, calls in flight canceled` at warn level, and returns
// an error wrapping the context's error. Close on a store that is not
// initialized, or on a nil Provider, returns nil and logs nothing.
//
// [New]'s constructor fails with:
//
//   - storage.ErrNilLogger for a nil logger;
//   - storage.ErrNilOption for a nil option, naming its position;
//   - storage.ErrNilConfig for WithConfig(nil).
//
// A nil ConfigProvider is accepted, and Init then requires WithConfig.
//
// # Readiness
//
// [Provider.CheckReady] does no I/O: it fails only when the store is not
// initialized or its context has ended.
//
// # Concurrency
//
// A Provider is safe for concurrent use. Init and Close run one at a
// time. One mutex guards the buckets and objects. A call holds it while it
// works, which is short and does no I/O, and PutObject does not hold it
// while it reads a body.
//
// # Errors
//
// The sentinel error of this package, to be tested with [errors.Is]:
//
//   - [ErrStorageFull]: a write would take the store past `maxobjects` or
//     `maxbytes`, or CreateBucket found MaxBuckets buckets.
//
// The other errors wrap a sentinel of the storage package, as the storage
// package describes. Every error starts with [ServiceName] and the
// operation. A configuration that fails its tags gets the validator's
// error after `gas/storage/memory: init: invalid config: validate config: `.
// The other errors, as returned, with `put object` and `get object`
// standing for each operation that can fail the same way, `copy object`
// included:
//
//	gas/storage/memory: logger is nil
//	gas/storage/memory: option is nil: at position 0
//	gas/storage/memory: config is nil
//	gas/storage/memory: init: no config provider
//	gas/storage/memory: init: invalid config: Storage.Bucket must be a valid bucket name
//	gas/storage/memory: init: service already initialized
//	gas/storage/memory: init: context canceled
//	gas/storage/memory: ready: service not initialized
//	gas/storage/memory: put object: input is nil
//	gas/storage/memory: put object: service not initialized
//	gas/storage/memory: put object: service closed
//	gas/storage/memory: put object: context canceled
//	gas/storage/memory: put object: invalid key: empty
//	gas/storage/memory: put object: bucket not found
//	gas/storage/memory: put object: precondition failed
//	gas/storage/memory: put object: object not found
//	gas/storage/memory: put object: storage full
//	gas/storage/memory: copy object: source: invalid key: empty
//	gas/storage/memory: copy object: invalid argument: copies an object onto itself without Replace
//	gas/storage/memory: create bucket: storage full
//	gas/storage/memory: get object: object not found
//	gas/storage/memory: get object: object not found: bucket not found
//	gas/storage/memory: get object: not supported: the store keeps no versions
//	gas/storage/memory: get object: not supported: the store keeps no parts
//	gas/storage/memory: get object: range not satisfiable
//	gas/storage/memory: delete bucket: bucket not empty
//	gas/storage/memory: list objects: invalid continuation token: not one this backend issued for this listing
//	gas/storage/memory: close: context deadline exceeded
package memory
