package vfs

import (
	"context"
	"crypto/md5" //nolint:gosec // G501: the ETag is an MD5 of the contents, as on S3, not a security check.
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	gohash "hash"
	"io"
	"os"
	"strings"
	"time"

	"github.com/gasmod/gas/storage"
)

// Bucket is an opened bucket: roots on its metadata, objects, locks and
// keys directories. It serves the operations of one call, and the caller
// closes it. Every object operation holds the key's lock, so the
// operations on one key are serialized across Buckets and processes.
type Bucket struct {
	// metaRoot is the metadata directory.
	metaRoot *os.Root
	// objRoot is the objects directory.
	objRoot *os.Root
	// keysRoot is the keys directory, which holds the key index.
	keysRoot *os.Root
	// lock locks the keys, with its files in the locks directory.
	lock *ObjectsLock
	// dir is the metadata directory, relative to the root.
	dir string
	// keysDir is the keys directory, relative to the root.
	keysDir string
	// objDir is the objects directory, relative to the root.
	objDir string
}

// Write is an object CreateObject writes.
type Write struct {
	Headers

	// Body yields the contents. CreateObject reads it to its end.
	Body io.Reader
	// Done is checked before each read of Body: once it returns an
	// error, CreateObject stops and returns it.
	Done func() error
	// Metadata is the user metadata.
	Metadata map[string]string
	// Key is the key of the object.
	Key string
	// IfMatch, when not empty, writes only over an object with this ETag.
	IfMatch string
	// IfNoneMatch writes only when no object is stored under the key.
	IfNoneMatch bool
}

// Written is what CreateObject wrote.
type Written struct {
	// StaleErr says why Stale stayed.
	StaleErr error
	// ETag is the hex MD5 of the contents.
	ETag string
	// Stale is the data file of the object replaced, when it could not be
	// removed, relative to the root, and else empty. The write is done.
	Stale string
	// Size is the length of the contents.
	Size int64
}

// Close closes the bucket's roots and its lock, giving up any key it
// holds.
func (b *Bucket) Close() error {
	err := closeAll(nil, b.metaRoot, b.objRoot, b.keysRoot, b.lock)
	if err != nil {
		return fmt.Errorf("close bucket: %w", err)
	}

	return nil
}

// CreateObject writes the object w, as described in the package
// documentation: under the key's lock, waiting for it until ctx ends, it
// adds the key to the index, writes a new data file, syncs it, then
// checks the condition against the object stored, and renames a new
// metadata file naming the data file over the old one, which is the
// commit, and syncs the directory. A failure before the commit leaves the
// object stored, if any, as it was, and removes what the write made; a
// failure to sync the directory after the commit is returned with the
// object written. An error of Body is a *BodyError; one of Done is
// returned as it is.
//
// The condition fails with ErrPreconditionFailed, and IfMatch of a key
// with no object with ErrObjectNotFound. Corrupt metadata meets neither
// condition, and is replaced by a write without one.
func (b *Bucket) CreateObject(ctx context.Context, w *Write) (out Written, err error) {
	name := hash(w.Key)

	err = b.lock.Lock(ctx, name)
	if err != nil {
		return Written{}, fmt.Errorf("lock object: %w", err)
	}

	committed := false

	defer func() { err = b.finishCreate(name, committed, err) }()

	indexed, err := b.addIndex(w.Key)
	if err != nil {
		return Written{}, b.unindex(w.Key, indexed, err)
	}

	meta := Metadata{
		User:     w.Metadata,
		Headers:  w.Headers,
		Key:      w.Key,
		DataFile: newDataFileName(name),
	}

	out, err = b.writeDataFile(meta.DataFile, w)
	if err != nil {
		return Written{}, b.unindex(w.Key, indexed, err)
	}

	meta.ETag = out.ETag

	prev, err := b.previousDataFile(name, w)
	if err == nil {
		committed, err = b.writeMetaFile(name, &meta)
	}

	if !committed {
		return Written{}, b.unindex(w.Key, indexed, discard(b.objRoot, meta.DataFile, err))
	}

	if prev != "" && prev != meta.DataFile {
		rmErr := rmInRoot(b.objRoot, prev, "data file")
		if rmErr != nil {
			out.Stale, out.StaleErr = join(b.objDir, prev), rmErr
		}
	}

	// A sync that failed after the commit leaves the object written, but
	// not known to survive a loss of power.
	return out, err
}

// OpenObject opens the object key for reading, under the key's lock,
// waiting for it until ctx ends. A missing object is an error wrapping
// ErrObjectNotFound; metadata that cannot be used, or that names a
// missing data file or one that is not a regular file, one wrapping
// ErrCorrupt. The caller closes the File, which keeps reading the
// contents it opened even if the object is replaced or deleted meanwhile.
func (b *Bucket) OpenObject(ctx context.Context, key string) (*Object, error) {
	name := hash(key)

	err := b.lock.Lock(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("lock object: %w", err)
	}

	meta, err := b.meta(name)
	if err != nil {
		// There is no usable object, so its lock file need not stay.
		return nil, b.releaseWith(name, err)
	}

	obj, err := b.openData(meta)

	uErr := b.lock.Unlock(name)
	if uErr != nil {
		uErr = fmt.Errorf("unlock object: %w", uErr)
		if err != nil {
			return nil, joinErrs(err, uErr)
		}

		return nil, closeAll(uErr, obj.File)
	}

	return obj, err
}

// DeleteObject deletes the object key, under the key's lock, waiting for
// it until ctx ends. A missing object is not an error. Removing the
// metadata file is the commit, which the directory sync makes durable;
// the data file and the index entry go next, and a failure to remove them
// is returned, though the object is deleted. Corrupt metadata is an error
// wrapping ErrCorrupt, and deletes nothing.
func (b *Bucket) DeleteObject(ctx context.Context, key string) error {
	name := hash(key)

	err := b.lock.Lock(ctx, name)
	if err != nil {
		return fmt.Errorf("lock object: %w", err)
	}

	meta, err := b.meta(name)
	if errors.Is(err, ErrObjectNotFound) {
		return b.releaseWith(name, b.removeIndex(key))
	}

	if err != nil {
		return b.unlockWith(name, err)
	}

	err = rmInRoot(b.metaRoot, name+metaFileExt, "meta file")
	if err != nil {
		return b.unlockWith(name, err)
	}

	err = joinErrs(
		syncDir(b.metaRoot, "."),
		rmInRoot(b.objRoot, meta.DataFile, "data file"),
		b.removeIndex(key),
	)

	return b.releaseWith(name, err)
}

// unindex removes the index entry of key when the failed write that
// returned err created it, and joins any removal error with err.
func (b *Bucket) unindex(key string, created bool, err error) error {
	if !created {
		return err
	}

	return joinErrs(err, b.removeIndex(key))
}

// writeDataFile writes the body of w to the new data file name, and syncs
// it and the objects directory. The data file is invisible until metadata
// names it, so it is written in place, and removed on failure.
func (b *Bucket) writeDataFile(name string, w *Write) (Written, error) {
	f, err := b.objRoot.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, filePerm)
	if err != nil {
		return Written{}, ioErr("create data file", err)
	}

	sum := md5.New() //nolint:gosec // G401: the ETag, as on S3.

	n, err := copyBody(f, sum, w)
	if err == nil {
		err = f.Sync()
		if err != nil {
			err = ioErr("sync data file", err)
		}
	}

	err = closeAll(err, f)
	if err == nil {
		err = syncDir(b.objRoot, ".")
	}

	if err != nil {
		return Written{}, discard(b.objRoot, name, err)
	}

	return Written{ETag: hex.EncodeToString(sum.Sum(nil)), Size: n}, nil
}

// previousDataFile checks the condition of w against the current metadata
// of name and returns the data file it names, or "" when there is none to
// remove: no metadata, or corrupt metadata whose data file name cannot be
// trusted.
func (b *Bucket) previousDataFile(name string, w *Write) (string, error) {
	prev, err := b.meta(name)

	switch {
	case errors.Is(err, ErrObjectNotFound) && w.IfMatch != "":
		return "", ErrObjectNotFound
	case errors.Is(err, ErrObjectNotFound):
		return "", nil
	case errors.Is(err, ErrCorrupt):
		// Corrupt metadata holds the key, by no ETag a condition knows.
		return "", conditionHolds(w.IfNoneMatch || w.IfMatch != "")
	case err != nil:
		return "", err
	}

	return prev.DataFile, conditionHolds(
		w.IfNoneMatch || (w.IfMatch != "" && prev.ETag != w.IfMatch),
	)
}

// writeMetaFile writes meta as the metadata file of name: to a temp file,
// synced, then renamed over the current one, so a reader sees the old
// metadata or the new, never a part, then syncs the directory. It
// reports whether the rename, the commit, was done, which a failure of
// the sync after it does not undo.
func (b *Bucket) writeMetaFile(name string, meta *Metadata) (bool, error) {
	data, err := json.Marshal(meta)
	if err != nil {
		return false, fmt.Errorf("encode meta file: %w", err)
	}

	file := name + metaFileExt
	tmp := file + newTempSuffix()

	err = writeSynced(b.metaRoot, tmp, data)
	if err != nil {
		return false, fmt.Errorf("write meta file: %w", err)
	}

	err = b.metaRoot.Rename(tmp, file)
	if err != nil {
		return false, discard(b.metaRoot, tmp, ioErr("commit meta file", err))
	}

	return true, syncDir(b.metaRoot, ".")
}

// openData opens the data file meta names and checks that it is a
// regular file.
func (b *Bucket) openData(meta *Metadata) (*Object, error) {
	var fi os.FileInfo

	f, err := retryPending(func() (*os.File, error) {
		f, info, oErr := openRegular(b.objRoot, meta.DataFile)

		fi = info

		return f, oErr
	})

	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil, fmt.Errorf("%w: %w", ErrCorrupt, errDataFileGone)
	case errors.Is(err, errNotRegular), errors.Is(err, errReplaced):
		// Under the key's lock, nothing replaces the data file.
		return nil, fmt.Errorf("%w: data file: %w", ErrCorrupt, err)
	case err != nil:
		return nil, ioErr("open data file", err)
	}

	return &Object{File: f, ObjectStat: stat(meta, fi)}, nil
}

// finishCreate gives up the lock CreateObject took on name, which
// committed a write when committed is set and returned err. A failed
// write of a key with no metadata leaves no object, so its lock file need
// not stay, as in OpenObject. Removing it is safe while the lock is held:
// a waiter on the removed file sees that it is no longer current and
// retries. A key whose metadata exists, even corrupt, keeps its lock
// file.
func (b *Bucket) finishCreate(name string, committed bool, err error) error {
	if err != nil && !committed && b.metaMissing(name) {
		return b.releaseWith(name, err)
	}

	return b.unlockWith(name, err)
}

// metaMissing reports whether name has no metadata file. Any other
// outcome of reading it, corrupt metadata or an I/O error included,
// counts as present, so the caller keeps the lock file.
func (b *Bucket) metaMissing(name string) bool {
	_, err := b.meta(name)

	return errors.Is(err, ErrObjectNotFound)
}

// unlockWith unlocks name, keeping its lock file, and returns err joined
// with any unlock error.
func (b *Bucket) unlockWith(name string, err error) error {
	uErr := b.lock.Unlock(name)
	if uErr != nil {
		return joinErrs(err, fmt.Errorf("unlock object: %w", uErr))
	}

	return err
}

// releaseWith unlocks name, removing its lock file, and returns err
// joined with any release error.
func (b *Bucket) releaseWith(name string, err error) error {
	rErr := b.lock.Release(name)
	if rErr != nil {
		return joinErrs(err, fmt.Errorf("release lock: %w", rErr))
	}

	return err
}

// meta reads and checks the metadata file of name. A missing file is an
// error wrapping ErrObjectNotFound. Anything but a regular file in its
// place, a file longer than maxRecordSize, or contents that decodeMeta
// refuses, is one wrapping ErrCorrupt. The errors never quote the file's contents, which hold the
// key and the metadata.
func (b *Bucket) meta(name string) (*Metadata, error) {
	file := name + metaFileExt

	data, err := retryPending(
		func() ([]byte, error) { return readCapped(b.metaRoot, file, maxRecordSize) },
	)

	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil, ErrObjectNotFound
	case errors.Is(err, errNotRegular), errors.Is(err, errTooLarge), errors.Is(err, errReplaced):
		// errReplaced is a write's rename when the caller does not hold
		// the key's lock, as Stat does not; it looks again under it.
		return nil, fmt.Errorf("%w: meta file: %w", ErrCorrupt, err)
	case err != nil:
		return nil, ioErr("read meta file", err)
	}

	return decodeMeta(name, data)
}

// copyChunk is how many bytes CreateObject reads from a body at a time.
const copyChunk = 32 << 10

// copyBody copies the body of w to f and sum, checking w.Done before each
// read, and returns the bytes copied.
func copyBody(f *os.File, sum gohash.Hash, w *Write) (int64, error) {
	buf := make([]byte, copyChunk)

	var n int64

	for {
		err := w.Done()
		if err != nil {
			return n, err
		}

		m, rErr := w.Body.Read(buf)
		if m > 0 {
			_, err = f.Write(buf[:m])
			if err != nil {
				return n, ioErr("write data file", err)
			}

			sum.Write(buf[:m])

			n += int64(m)
		}

		switch {
		case errors.Is(rErr, io.EOF):
			return n, nil
		case rErr != nil:
			// A body that failed because the call ended, such as one that
			// reads from the same context, reports the end.
			err = w.Done()
			if err != nil {
				return n, err
			}

			return n, &BodyError{Err: rErr}
		}
	}
}

// conditionHolds returns ErrPreconditionFailed when failed is set, and
// else nil.
func conditionHolds(failed bool) error {
	if failed {
		return ErrPreconditionFailed
	}

	return nil
}

// stat returns the ObjectStat of meta, whose data file fi describes.
func stat(meta *Metadata, fi os.FileInfo) ObjectStat {
	return ObjectStat{Metadata: *meta, Size: fi.Size(), LastModified: modTime(fi.ModTime())}
}

// decodeMeta decodes data, the metadata file of name, and checks all of
// it, as a file on disk may have been written by anyone: it must not
// decode to null, must name a data file that isDataFileName accepts, hold
// a key that hashes to name and keeps the key rules of the storage
// package, an ETag of 32 lower case hex digits, header fields that keep
// the header rules, and user metadata that keeps the metadata rules with
// its names in lower case. Anything else is an error wrapping ErrCorrupt.
func decodeMeta(name string, data []byte) (*Metadata, error) {
	var meta *Metadata

	err := json.Unmarshal(data, &meta)

	switch {
	case err != nil:
		return nil, fmt.Errorf("%w: decode meta file: %s", ErrCorrupt, describeJSONError(err))
	case meta == nil:
		return nil, fmt.Errorf("%w: meta file holds null", ErrCorrupt)
	case !isDataFileName(name, meta.DataFile):
		return nil, fmt.Errorf("%w: meta file names no valid data file", ErrCorrupt)
	case hash(meta.Key) != name:
		return nil, fmt.Errorf("%w: meta file holds another key", ErrCorrupt)
	}

	err = checkMeta(meta)
	if err != nil {
		return nil, fmt.Errorf("%w: meta file: %w", ErrCorrupt, err)
	}

	return meta, nil
}

// errBadETag reports metadata whose ETag is not 32 lower case hex digits.
var errBadETag = errors.New("ETag is not 32 lower case hex digits")

// errUpperName reports user metadata with a name not in lower case.
var errUpperName = errors.New("a metadata name is not in lower case")

// checkMeta checks the key, the ETag, the header fields and the user
// metadata of meta, as decodeMeta does. Its errors quote no value.
func checkMeta(meta *Metadata) error {
	err := storage.CheckKey(meta.Key)
	if err != nil {
		return err //nolint:wrapcheck // decodeMeta wraps it.
	}

	if len(meta.ETag) != md5.Size*2 || !isLowerHex(meta.ETag) {
		return errBadETag
	}

	_, err = storage.CheckHeaders(&storage.ObjectHeaders{
		ContentType:        setOrNil(meta.ContentType),
		CacheControl:       setOrNil(meta.CacheControl),
		ContentDisposition: setOrNil(meta.ContentDisposition),
		ContentEncoding:    setOrNil(meta.ContentEncoding),
		ContentLanguage:    setOrNil(meta.ContentLanguage),
		Expires:            setOrNil(meta.Expires),
	})
	if err != nil {
		return err //nolint:wrapcheck // decodeMeta wraps it.
	}

	_, err = storage.CheckMetadata(meta.User)
	if err != nil {
		return err //nolint:wrapcheck // decodeMeta wraps it.
	}

	for n := range meta.User {
		if n != strings.ToLower(n) {
			return errUpperName
		}
	}

	return nil
}

// setOrNil returns a pointer to s, or nil for an empty s: a header field
// the metadata file leaves out.
func setOrNil(s string) *string {
	if s == "" {
		return nil
	}

	return &s
}

// describeJSONError describes a decode error of a metadata file by its
// offset alone. The error's own text can quote a metadata name, in the
// field path of a type error, or bytes of the file, which are user data.
func describeJSONError(err error) string {
	if se, ok := errors.AsType[*json.SyntaxError](err); ok {
		return fmt.Sprintf("syntax error at offset %d", se.Offset)
	}

	if te, ok := errors.AsType[*json.UnmarshalTypeError](err); ok {
		return fmt.Sprintf("type mismatch at offset %d", te.Offset)
	}

	return "invalid JSON"
}

// modTime returns t as the package reports a modification time: in UTC,
// to the second.
func modTime(t time.Time) time.Time { return t.UTC().Truncate(time.Second) }
