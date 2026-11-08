// Package vfs stores the buckets and objects of the dir backend under an
// os.Root: the layout, the commit of a write by rename, the key index, the
// locks, the deletion of buckets and the sweep of crashed writes. The dir
// package documentation describes what a caller sees; this one, how.
//
// # Usage
//
// The dir backend opens a [VFS] on a root per Init, and a [Bucket] per
// call:
//
//	v, err := vfs.Open(root)
//	res, err := v.Sweep(ctx, limit, report)
//	err = v.CreateBucket(ctx, "photos")
//	bk, err := v.OpenBucket("photos")
//	defer bk.Close()
//	out, err := bk.CreateObject(ctx, &vfs.Write{Key: "cats/tom.txt", Body: body, Done: ctx.Err})
//
// # Layout
//
// A bucket is a metadata directory at the top of the root, named by the
// hex SHA-256 of its name, with an objects, a locks and a keys directory
// of the same name under `.gas`, and a name record in `.gas/buckets`. An
// object is a metadata file `<key hash>.gas.json`, JSON of [Metadata],
// which names a data file `<key hash>_<uuid>` in the objects directory.
// `.gas/layout` holds the version of the layout; [Open] writes it in a
// root that holds no bucket, and refuses one that holds buckets without it
// ([ErrOldLayout]).
//
// # Reads
//
// Every file is read back as untrusted. A metadata file, a data file and
// a name record are opened without blocking and kept only when they are
// regular files at the name itself, not through a symlink; a metadata
// file or a name record is read no further than 64 KiB. A metadata file
// must keep every rule of the storage package that a write keeps, a key
// from the index the key rules, and a name record's name the bucket
// rules; anything else is [ErrCorrupt].
//
// # Commit
//
// [Bucket.CreateObject] holds the key's lock throughout. It adds the key
// to the index, writes the body to a new data file and syncs it and its
// directory, checks the condition against the metadata stored, writes the
// new metadata to a temp file `<key hash>.gas.json<uuid>`, syncs it,
// renames it over the metadata file, which is the commit, and syncs the
// directory. Then it removes the data file the old metadata named. A
// failure before the commit removes the data file, and the index entry
// when the write created it.
//
// # Key index
//
// The keys directory of a bucket holds an entry per key, whose path
// spells the key in lower case hex, 120 bytes of the key per element: a
// file `<hex>.k` for the last bytes, under a directory `<hex>.d` for each
// 120 bytes before them. A walk reads each directory, sorts its entries by
// the bytes they spell, an entry of a key before a directory that spells
// the same bytes, and so yields the keys in byte order. It passes over
// the entries outside the prefix and before the bound of a page, and stops
// at the first past the prefix. An entry is added before the commit and
// removed after a delete, so the index holds every key with an object,
// and may hold keys without one; a listing checks that the metadata file
// is there. A write syncs each directory it creates for its entry, and the
// directory each is created in, once the entry is there. The directories
// of the index stay when their keys are deleted, until a Sweep finds them
// empty or DeleteBucket; a write that sees a directory of its entry
// removed by a Sweep creates it again.
//
// # Locks
//
// An [ObjectsLock] locks a name with an exclusive OS file lock on a file
// of that name, flock or LockFileEx, which excludes other processes, and
// an in-process slot per name, so that one goroutine per name at most
// waits in the blocking lock call. The locks of every Bucket handle of a
// VFS, and its lifecycle locks, share one table of slots, keyed by the
// path of the lock file below `.gas/locks`, so this holds however many
// handles wait for a key. A wait honors its context; the goroutine in
// the lock call stays until it gets the lock, then gives it up. Release
// removes the file while it holds the lock, and a waiter that then gets
// the removed file sees that it is no longer the one at the name and
// tries again. Each key has a lock file in the bucket's locks
// directory; each bucket a lifecycle lock `<bucket hash>.bucket` in
// `.gas/locks`, which CreateBucket and DeleteBucket hold.
//
// # Sweep
//
// [VFS.Sweep] removes the temp files of name records and of metadata, the
// data files no metadata names, the index entries of keys with no
// metadata file, and the directories of the index with no entry; and it
// makes again the index entry of an object that has none. It acts on a
// key or a bucket only under its lock, taken as Lock takes it but without
// waiting: it opens the lock file, creating it when it is missing, locks
// it and checks that it is still the file at the name; a file released
// meanwhile counts as held. It removes an index directory without a lock,
// as the removal fails while the directory holds an entry. A bucket
// deleted while it is swept, whose open directories then fail to read
// with a not-exist error, as on Linux, is passed over without an error
// once it no longer opens.
//
// # Errors
//
// The errors hold no path: an error of an os call gives the call and its
// cause only, such as `open data file: no such file or directory`. They
// wrap [ErrBucketNotFound], [ErrBucketNotEmpty], [ErrPreconditionFailed],
// [ErrObjectNotFound], [ErrCorrupt], [ErrOldLayout] or [ErrForeign] where
// one applies; an error of a body is a [BodyError]; and a failure with
// errors of its cleanup is a [JoinedError], the failure first.
package vfs
