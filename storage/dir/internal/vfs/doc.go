// Package vfs stores buckets of objects under an os.Root for the dir
// provider. It is internal to the dir backend: its types and errors are not
// part of the public API, and the provider maps its errors to storage ones.
//
// # Layout
//
// Bucket names and keys are stored under the hex SHA-256 of their bytes:
//
//	/
//	├── .gas/
//	│   ├── objects/
//	│   │   └── <bucket-hash>/
//	│   │       └── <key-hash>_<uuid>     data file
//	│   └── locks/
//	│       └── <bucket-hash>/
//	│           └── <key-hash>            lock file
//	└── <bucket-hash>/
//	    └── <key-hash>.gas.json           metadata file
//
// A bucket exists when its metadata directory does; CreateBucket makes it
// last, so a half-created bucket reads as missing and is completed by the
// next CreateBucket.
//
// # Objects
//
// The metadata file is JSON (ObjectMetadata) and names the object's data
// file. CreateObject writes a new data file, then writes the metadata to a
// temp file and renames it over the old one. The rename is the commit point;
// the replaced data file is removed after it. DeleteObject removes the
// metadata file first (its commit point), then the data file. Metadata read
// back from disk is checked before it is used: the data file it names must
// be the key's hash, "_" and a canonical UUID, so it can name neither
// another object's data nor a path outside the objects directory.
//
// # Locking
//
// Every object operation holds the object's ObjectsLock entry for its whole
// duration. An entry is an OS file lock on the object's lock file, which
// excludes other processes, plus a per-name slot that admits one goroutine
// of this process at a time. Release removes the lock file while holding it;
// a waiter that then gets the removed file sees it is no longer at its name
// and retries. A wait that is canceled while blocked in the lock syscall
// leaves a goroutine that finishes the syscall, unlocks, and gives the slot
// back.
//
// # Errors
//
// ErrBucketNotFound, ErrBucketExists, ErrObjectNotFound, ErrCorrupt and
// ErrStaleData classify errors, each wrapped only where its condition holds.
// Corruption is never reported as ErrObjectNotFound. Errors name files by
// their hashed names and never quote a key, a bucket name or metadata.
package vfs
