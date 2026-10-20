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

// Skipped is an entry an operation passed over or left behind: its path
// relative to the VFS root, for an operator to inspect, and why. Err wraps
// ErrCorrupt or ErrUnnamed where one applies.
type Skipped struct {
	Err  error
	Path string
}

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

// ListBuckets returns every bucket under the root, in no particular order,
// and the buckets it left out. A bucket is a metadata directory, named by a
// hash, whose name record holds a name with that hash.
//
// A bucket whose name record is missing, which a version that did not write
// one or a crash can leave, or whose record holds a name that hashes to
// something else, cannot be named and is left out, with an ErrUnnamed
// Skipped naming its record: one such bucket must not make every other
// bucket unlistable. A bucket deleted while ListBuckets runs is left out
// with no Skipped: it is gone, not unnamed. CreateBucket of that bucket rewrites
// the record and brings it back, and its objects stay reachable through the
// other methods meanwhile. A record that cannot be read at all, or that is
// not a regular file, fails the call instead: that is an I/O fault, not a
// bucket awaiting repair.
func (vfs *VFS) ListBuckets(ctx context.Context) ([]BucketEntry, []Skipped, error) {
	entries, err := fs.ReadDir(vfs.root.FS(), ".")
	if err != nil {
		return nil, nil, fmt.Errorf("read root dir: %w", err)
	}

	var out []BucketEntry
	var skipped []Skipped
	for _, e := range entries {
		if cErr := ctx.Err(); cErr != nil {
			return nil, nil, fmt.Errorf("list buckets: %w", cErr)
		}
		if !e.IsDir() || !isHash(e.Name()) {
			continue
		}
		b, ok, bErr := vfs.bucketEntry(e.Name())
		if bErr != nil {
			return nil, nil, bErr
		}
		if !ok {
			if _, sErr := vfs.root.Lstat(e.Name()); errors.Is(sErr, os.ErrNotExist) {
				// Deleted since the root was read: gone, not unnamed.
				continue
			}
			skipped = append(skipped, Skipped{
				Path: filepath.Join(bucketsPath, e.Name()),
				Err:  fmt.Errorf("%w: name record missing or holding another name", ErrUnnamed),
			})
			continue
		}
		out = append(out, b)
	}
	return out, skipped, nil
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
// order, and the objects it left out. It reads each metadata file without
// taking the object's lock: a key's metadata file only ever holds that key,
// and an object deleted while Keys runs is left out. An object no read can
// use, because its metadata cannot be used, holds a key that does not hash
// to the file's name, or names a data file that is gone or not a regular
// file, is left out with a Skipped wrapping ErrCorrupt. Any other error
// reading the metadata or stating the data file fails the call: that is an
// I/O fault, not a damaged object. Temp files of writes in progress are
// passed over.
func (b *Bucket) Keys(ctx context.Context) ([]string, []Skipped, error) {
	entries, err := fs.ReadDir(b.metaRoot.FS(), ".")
	if err != nil {
		return nil, nil, fmt.Errorf("read meta dir: %w", err)
	}

	var keys []string
	var skipped []Skipped
	for _, e := range entries {
		if cErr := ctx.Err(); cErr != nil {
			return nil, nil, fmt.Errorf("list keys: %w", cErr)
		}
		name, ok := strings.CutSuffix(e.Name(), metaFileExt)
		if !ok || !isHash(name) {
			continue
		}
		key, kErr := b.usableKey(ctx, name)
		switch {
		case errors.Is(kErr, ErrObjectNotFound):
			continue
		case errors.Is(kErr, ErrCorrupt):
			skipped = append(skipped, Skipped{Path: filepath.Join(b.dir, e.Name()), Err: kErr})
		case kErr != nil:
			return nil, nil, kErr
		default:
			keys = append(keys, key)
		}
	}
	return keys, skipped, nil
}

// errDataFileGone is the cause of the ErrCorrupt of an object whose
// metadata names a data file that is not there.
var errDataFileGone = errors.New("data file is gone")

// usableKey returns the key the metadata file of name holds, when the
// object can be read: its metadata decodes, holds a key that hashes to
// name, and names a data file that is a regular file. Otherwise the error
// wraps ErrCorrupt, or ErrObjectNotFound for an object deleted meanwhile.
//
// It reads without the key's lock, so a write can be halfway: an overwrite
// renames its metadata in and then removes the old data file, and a delete
// removes the metadata and then the data file, so metadata read just
// before either names a data file that is gone. That alone is no proof of
// damage, so a missing data file is looked at again under the key's lock,
// which every write holds until it is done.
func (b *Bucket) usableKey(ctx context.Context, name string) (string, error) {
	key, err := b.checkKey(name)
	if !errors.Is(err, errDataFileGone) {
		return key, err
	}
	if lErr := b.lock.Lock(ctx, name); lErr != nil {
		return "", fmt.Errorf("lock object: %w", lErr)
	}
	key, err = b.checkKey(name)
	if errors.Is(err, ErrObjectNotFound) {
		// No usable object, so the lock file need not stay, as in
		// OpenObject.
		return "", b.releaseWith(name, err)
	}
	return key, b.unlockWith(name, err)
}

// checkKey reads the metadata file of name and checks that the object can
// be read; see usableKey.
func (b *Bucket) checkKey(name string) (string, error) {
	meta, err := b.meta(name)
	if err != nil {
		return "", err
	}
	fi, err := b.objRoot.Lstat(meta.DataFile)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "", fmt.Errorf("%w: %w", ErrCorrupt, errDataFileGone)
	case err != nil:
		return "", fmt.Errorf("stat data file: %w", err)
	case !fi.Mode().IsRegular():
		return "", fmt.Errorf("%w: data file is not a regular file", ErrCorrupt)
	}
	return meta.Key, nil
}

// MetaPath returns the path of the metadata file of key, relative to the VFS
// root, for reporting it.
func (b *Bucket) MetaPath(key string) string {
	return filepath.Join(b.dir, hash(key)+metaFileExt)
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
