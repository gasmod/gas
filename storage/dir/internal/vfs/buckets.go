package vfs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/gasmod/gas/storage"
)

// Skipped is an entry an operation passed over, removed or left behind:
// its path relative to the root, for an operator to inspect, and why. Err
// wraps ErrCorrupt, ErrUnnamed or ErrForeign where one applies.
type Skipped struct {
	// Err says why.
	Err error
	// Path is the entry's path relative to the root.
	Path string
}

// BucketEntry is a bucket ListBuckets found: its name, and when its name
// record was written, in UTC to the second, which CreateBucket does once,
// before the bucket exists.
type BucketEntry struct {
	// Created is the modification time of the name record.
	Created time.Time
	// Name is the bucket's name.
	Name string
}

// bucketNameFile returns the name record of the bucket name, relative to
// the root.
func bucketNameFile(name string) string {
	return join(bucketsPath, hash(name))
}

// writeBucketName writes the name record of the bucket name unless one
// that holds name is there. It writes a temp file, syncs it and renames it
// into place, so a reader sees no record or a whole one. A record that
// holds name is not rewritten, so its modification time stays when the
// bucket was first created; one that holds anything else, such as a
// record a crash left empty, is replaced.
func (v *VFS) writeBucketName(name string) error {
	file := bucketNameFile(name)

	fi, err := lstatIn(v.root, file)

	switch {
	case err == nil && !fi.Mode().IsRegular():
		return fmt.Errorf("bucket name record: %w", errNotRegular)
	case err == nil:
		held, rErr := readCapped(v.root, file, maxRecordSize)

		switch {
		case errors.Is(rErr, errTooLarge):
			// Holds something else: replaced below.
		case rErr != nil:
			return ioErr("read bucket name record", rErr)
		case string(held) == name:
			return nil
		}
	case !errors.Is(err, os.ErrNotExist):
		return ioErr("stat bucket name record", err)
	}

	tmp := file + newTempSuffix()

	err = writeSynced(v.root, tmp, []byte(name))
	if err != nil {
		return fmt.Errorf("write bucket name record: %w", err)
	}

	err = renameIn(v.root, tmp, file)
	if err != nil {
		return discard(v.root, tmp, ioErr("commit bucket name record", err))
	}

	return syncDir(v.root, bucketsPath)
}

// ListBuckets returns every bucket under the root, in no particular
// order, and the buckets it left out. A bucket is a metadata directory,
// named by a hash, whose name record holds a name with that hash.
//
// A bucket whose name record is missing, or holds a name that hashes to
// something else, cannot be named and is left out, with an ErrUnnamed
// Skipped naming its record: one such bucket must not make every other
// bucket unlistable. So is one whose record is longer than maxRecordSize
// or holds a name that hashes to the bucket but breaks the bucket rules
// of the storage package, with a Skipped wrapping ErrCorrupt. A bucket
// deleted while ListBuckets runs is left out with no Skipped. CreateBucket
// of that bucket rewrites the record. A record that cannot be read at
// all, or that is not a regular file, fails the call instead.
func (v *VFS) ListBuckets(ctx context.Context) ([]BucketEntry, []Skipped, error) {
	entries, err := readDirIn(v.root, ".")
	if err != nil {
		return nil, nil, ioErr("read root dir", err)
	}

	var (
		out     []BucketEntry
		skipped []Skipped
	)

	for _, e := range entries {
		err = ctx.Err()
		if err != nil {
			return nil, nil, err //nolint:wrapcheck // the backend adds the operation.
		}

		if !e.IsDir() || !isHash(e.Name()) {
			continue
		}

		b, ok, bErr := v.bucketEntry(e.Name())

		switch {
		case errors.Is(bErr, ErrCorrupt):
			skipped = append(skipped, Skipped{Path: join(bucketsPath, e.Name()), Err: bErr})

			continue
		case bErr != nil:
			return nil, nil, bErr
		}

		if ok {
			out = append(out, b)

			continue
		}

		_, sErr := retryPending(func() (os.FileInfo, error) { return lstatIn(v.root, e.Name()) })
		if errors.Is(sErr, os.ErrNotExist) {
			// Deleted since the root was read: gone, not unnamed.
			continue
		}

		skipped = append(skipped, Skipped{
			Path: join(bucketsPath, e.Name()),
			Err:  fmt.Errorf("%w: name record missing or holding another name", ErrUnnamed),
		})
	}

	return out, skipped, nil
}

// bucketEntry reads the name record of the bucket whose metadata directory
// is h and checks that the name it holds hashes to h. It returns false and
// no error for a bucket that cannot be named, and an error wrapping
// ErrCorrupt for a record longer than maxRecordSize or a name that breaks
// the bucket rules.
func (v *VFS) bucketEntry(h string) (BucketEntry, bool, error) {
	file := join(bucketsPath, h)

	fi, err := retryPending(func() (os.FileInfo, error) { return lstatIn(v.root, file) })

	switch {
	case errors.Is(err, os.ErrNotExist):
		return BucketEntry{}, false, nil
	case err != nil:
		return BucketEntry{}, false, ioErr("stat bucket name record", err)
	case !fi.Mode().IsRegular():
		return BucketEntry{}, false, fmt.Errorf("bucket name record: %w", errNotRegular)
	}

	name, err := retryPending(
		func() ([]byte, error) { return readCapped(v.root, file, maxRecordSize) },
	)

	switch {
	case errors.Is(err, os.ErrNotExist):
		// Removed since the Lstat, by a DeleteBucket.
		return BucketEntry{}, false, nil
	case errors.Is(err, errTooLarge):
		return BucketEntry{}, false, fmt.Errorf("%w: bucket name record: %w", ErrCorrupt, err)
	case err != nil:
		return BucketEntry{}, false, ioErr("read bucket name record", err)
	case hash(string(name)) != h:
		return BucketEntry{}, false, nil
	}

	cErr := storage.CheckBucket(string(name))
	if cErr != nil {
		return BucketEntry{}, false, fmt.Errorf("%w: bucket name record: %w", ErrCorrupt, cErr)
	}

	return BucketEntry{Name: string(name), Created: modTime(fi.ModTime())}, true, nil
}

// hashLen is the length of a name hash: hex SHA-256.
const hashLen = 64

// isHash reports whether s has the form of a name hash: hashLen lower
// case hex digits.
func isHash(s string) bool {
	return len(s) == hashLen && isLowerHex(s)
}

// isLowerHex reports whether s is lower case hex digits only.
func isLowerHex(s string) bool {
	for i := range len(s) {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}

	return true
}
