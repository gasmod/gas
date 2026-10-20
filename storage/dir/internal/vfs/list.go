package vfs

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
	"uuid"
)

// hashLen is the length of a name hash: hex SHA-256.
const hashLen = 64

// BucketEntry is a bucket ListBuckets found: its name and when its name
// record was written, which CreateBucket does once, before the bucket
// exists.
type BucketEntry struct {
	Created time.Time
	Name    string
}

// bucketNameFile returns the name record of the bucket name, relative to the
// VFS root.
func bucketNameFile(name string) string {
	return filepath.Join(bucketsPath, hash(name))
}

// writeBucketName writes the name record of the bucket name unless one that
// holds name is already there. It writes a temp file and renames it into
// place, so a reader sees no record or a whole one. A record that holds name
// is not rewritten, so its modification time stays when the bucket was
// first created; one that holds anything else, such as a record a crash left
// empty, is replaced.
func (vfs *VFS) writeBucketName(name string) error {
	file := bucketNameFile(name)

	fi, err := vfs.root.Lstat(file)
	switch {
	case err == nil && !fi.Mode().IsRegular():
		return errors.New("bucket name record is not a regular file")
	case err == nil:
		held, rErr := vfs.root.ReadFile(file)
		if rErr != nil {
			return fmt.Errorf("read bucket name record: %w", rErr)
		}
		if string(held) == name {
			return nil
		}
	case !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("stat bucket name record: %w", err)
	}

	tmp := file + uuid.New().String()
	if wErr := vfs.root.WriteFile(tmp, []byte(name), filePerm); wErr != nil {
		return discardTmp(vfs.root, tmp, fmt.Errorf("write bucket name record: %w", wErr))
	}
	if rErr := vfs.root.Rename(tmp, file); rErr != nil {
		return discardTmp(vfs.root, tmp, fmt.Errorf("commit bucket name record: %w", rErr))
	}
	return nil
}

// ListBuckets returns every bucket under the root, in no particular order. A
// bucket is a metadata directory, named by a hash, whose name record holds a
// name with that hash.
//
// A bucket whose name record is missing, which a version that did not write
// one or a crash can leave, or whose record holds a name that hashes to
// something else, cannot be named and is left out: one such bucket must not
// make every other bucket unlistable. CreateBucket of that bucket rewrites
// the record and brings it back, and its objects stay reachable through the
// other methods meanwhile. A record that cannot be read at all, or that is
// not a regular file, fails the call instead: that is an I/O fault, not a
// bucket awaiting repair.
func (vfs *VFS) ListBuckets(ctx context.Context) ([]BucketEntry, error) {
	entries, err := fs.ReadDir(vfs.root.FS(), ".")
	if err != nil {
		return nil, fmt.Errorf("read root dir: %w", err)
	}

	var out []BucketEntry
	for _, e := range entries {
		if cErr := ctx.Err(); cErr != nil {
			return nil, fmt.Errorf("list buckets: %w", cErr)
		}
		if !e.IsDir() || !isHash(e.Name()) {
			continue
		}
		b, ok, bErr := vfs.bucketEntry(e.Name())
		if bErr != nil {
			return nil, bErr
		}
		if ok {
			out = append(out, b)
		}
	}
	return out, nil
}

// bucketEntry reads the name record of the bucket whose metadata directory is
// h and checks that the name it holds hashes to h. It returns false with no
// error for a bucket that cannot be named, which ListBuckets leaves out. The
// errors name the record by its path, so an operator can inspect it, and
// never quote a bucket name: a mismatched record holds a name this bucket
// does not own.
func (vfs *VFS) bucketEntry(h string) (BucketEntry, bool, error) {
	file := filepath.Join(bucketsPath, h)

	fi, err := vfs.root.Lstat(file)
	if errors.Is(err, os.ErrNotExist) {
		return BucketEntry{}, false, nil
	}
	if err != nil {
		return BucketEntry{}, false, fmt.Errorf("stat bucket name record %s: %w", file, err)
	}
	if !fi.Mode().IsRegular() {
		return BucketEntry{}, false, fmt.Errorf(
			"bucket name record %s is not a regular file", file)
	}

	name, err := vfs.root.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		// Removed since the Lstat, by a DeleteBucket.
		return BucketEntry{}, false, nil
	}
	if err != nil {
		return BucketEntry{}, false, fmt.Errorf("read bucket name record %s: %w", file, err)
	}
	if hash(string(name)) != h {
		return BucketEntry{}, false, nil
	}
	return BucketEntry{Name: string(name), Created: fi.ModTime()}, true, nil
}

// Keys returns the key of every object in the bucket, in no particular
// order. It reads each metadata file without taking the object's lock: a
// key's metadata file only ever holds that key, and an object deleted while
// Keys runs is left out. Metadata that cannot be used, or that holds a key
// that does not hash to the file's name, returns an error wrapping
// ErrCorrupt. Temp files of writes in progress are skipped.
func (b *Bucket) Keys(ctx context.Context) ([]string, error) {
	entries, err := fs.ReadDir(b.metaRoot.FS(), ".")
	if err != nil {
		return nil, fmt.Errorf("read meta dir: %w", err)
	}

	var keys []string
	for _, e := range entries {
		if cErr := ctx.Err(); cErr != nil {
			return nil, fmt.Errorf("list keys: %w", cErr)
		}
		name, ok := strings.CutSuffix(e.Name(), metaFileExt)
		if !ok || !isHash(name) {
			continue
		}
		meta, mErr := b.meta(name)
		if errors.Is(mErr, ErrObjectNotFound) {
			continue
		}
		if mErr != nil {
			return nil, mErr
		}
		if hash(meta.Key) != name {
			return nil, fmt.Errorf("%w: meta file %s holds another key", ErrCorrupt, name)
		}
		keys = append(keys, meta.Key)
	}
	return keys, nil
}

// isHash reports whether s has the form of a name hash: hashLen lowercase hex
// digits.
func isHash(s string) bool {
	if len(s) != hashLen {
		return false
	}
	for i := range len(s) {
		if c := s[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
