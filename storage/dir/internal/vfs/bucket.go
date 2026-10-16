package vfs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"uuid"

	"github.com/gasmod/gas/storage/dir/internal/util"
)

// Bucket is an opened bucket: roots on its metadata, objects and locks
// directories. It serves the operations of one provider call and is closed
// by the caller. Every object operation holds the object's lock, so
// operations on one key are serialized across Buckets and processes.
type Bucket struct {
	metaRoot *os.Root
	objRoot  *os.Root
	lock     *ObjectsLock
}

var _ io.Closer = (*Bucket)(nil)

// NewBucket returns a Bucket on the given roots. Close closes all three.
func NewBucket(metaRoot, objRoot, locksRoot *os.Root) *Bucket {
	return &Bucket{
		metaRoot: metaRoot,
		objRoot:  objRoot,
		lock:     NewLock(locksRoot),
	}
}

// CreateObjectInput is the object CreateObject writes. A nil Body is an
// empty object. A nil or empty ContentType is sniffed from the body.
type CreateObjectInput struct {
	Body        io.Reader
	ContentType *string
	Metadata    map[string]string
	Key         string
}

// CreateObject writes the object: it writes a new data file, then renames a
// new metadata file naming it over the old one. That rename is the commit
// point. A failure before it leaves the previous object, if any, as it was,
// with nothing new left behind.
//
// After the commit the previous data file is removed. If that fails, out is
// the committed metadata and err wraps ErrStaleData. out is also non-nil
// when the commit succeeded but unlocking the object failed. On every other
// error out is nil.
//
// Metadata that is corrupt (ErrCorrupt) is replaced, so a corrupt key can be
// repaired; its data file cannot be trusted and is left behind. Metadata
// that exists but cannot be read fails the call before anything is written.
func (b *Bucket) CreateObject(
	ctx context.Context,
	input *CreateObjectInput,
) (out *ObjectMetadata, err error) {
	name := hash(input.Key)

	if lErr := b.lock.Lock(ctx, name); lErr != nil {
		return nil, fmt.Errorf("lock object: %w", lErr)
	}
	defer func() { err = b.finishCreate(name, out != nil, err) }()

	prevDataFile, pErr := b.previousDataFile(name)
	if pErr != nil {
		return nil, pErr
	}

	body := input.Body
	if body == nil {
		body = bytes.NewReader(nil)
	}

	contentType, body, ctErr := sniffContentType(body, input.ContentType)
	if ctErr != nil {
		return nil, fmt.Errorf("sniff content type: %w", ctErr)
	}

	meta := &ObjectMetadata{
		Key:         input.Key,
		ContentType: contentType,
		Metadata:    input.Metadata,
		DataFile:    newDataFileName(name),
	}

	if dErr := b.writeDataFile(meta.DataFile, body); dErr != nil {
		return nil, dErr
	}

	if wErr := b.writeMetaFile(name, meta); wErr != nil {
		return nil, discardTmp(b.objRoot, meta.DataFile, wErr)
	}

	out = &ObjectMetadata{
		Key:         meta.Key,
		ContentType: meta.ContentType,
		Metadata:    meta.Metadata,
		DataFile:    meta.DataFile,
	}

	if prevDataFile != "" && prevDataFile != meta.DataFile {
		if rmErr := rmInRoot(b.objRoot, prevDataFile, "data file"); rmErr != nil {
			return out, fmt.Errorf("%w: %w", ErrStaleData, rmErr)
		}
	}

	return out, nil
}

// writeDataFile writes body to the new data file name. The data file is
// invisible until metadata names it, so it is written in place and removed
// on failure.
func (b *Bucket) writeDataFile(name string, body io.Reader) error {
	f, fErr := openInRoot(b.objRoot, name, "data file")
	if fErr != nil {
		return fErr
	}

	if _, cpErr := io.Copy(f, body); cpErr != nil {
		return discardTmp(
			b.objRoot,
			name,
			fmt.Errorf("copy body to data file: %w", util.CloseWithErr(cpErr, f)),
		)
	}

	if cErr := f.Close(); cErr != nil {
		return discardTmp(b.objRoot, name, fmt.Errorf("close data file: %w", cErr))
	}

	return nil
}

// previousDataFile returns the data file the current metadata of name points
// to, or "" when there is none to remove: no metadata, or corrupt metadata
// whose data file name cannot be trusted.
func (b *Bucket) previousDataFile(name string) (string, error) {
	prev, err := b.meta(name)
	switch {
	case err == nil:
		return prev.DataFile, nil
	case errors.Is(err, ErrObjectNotFound), errors.Is(err, ErrCorrupt):
		return "", nil
	default:
		return "", err
	}
}

// OpenObjectInput names the object OpenObject opens.
type OpenObjectInput struct {
	Key string
}

// OpenObject opens the object for reading. A missing object returns an error
// wrapping ErrObjectNotFound; metadata that cannot be used, or that names a
// missing or non-regular data file, returns one wrapping ErrCorrupt. The
// caller closes the returned File; it keeps reading the body it opened even
// if the object is overwritten or deleted meanwhile.
func (b *Bucket) OpenObject(ctx context.Context, input *OpenObjectInput) (*Object, error) {
	name := hash(input.Key)

	if lErr := b.lock.Lock(ctx, name); lErr != nil {
		return nil, fmt.Errorf("lock object: %w", lErr)
	}

	meta, mErr := b.meta(name)
	if mErr != nil {
		// There is no usable object, so its lock file need not stay.
		return nil, b.releaseWith(name, mErr)
	}

	obj, oErr := b.openData(meta)
	if uErr := b.lock.Unlock(name); uErr != nil {
		uErr = fmt.Errorf("unlock object: %w", uErr)
		if oErr != nil {
			return nil, errors.Join(oErr, uErr)
		}
		return nil, errors.Join(uErr, obj.File.Close())
	}
	return obj, oErr
}

// openData opens the data file meta names and checks that it is a regular
// file.
func (b *Bucket) openData(meta *ObjectMetadata) (*Object, error) {
	f, err := b.objRoot.Open(meta.DataFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: open data file: %w", ErrCorrupt, err)
	}
	if err != nil {
		return nil, fmt.Errorf("open data file: %w", err)
	}

	fi, err := f.Stat()
	if err != nil {
		return nil, errors.Join(fmt.Errorf("stat data file: %w", err), f.Close())
	}
	if !fi.Mode().IsRegular() {
		return nil, errors.Join(
			fmt.Errorf("%w: data file is not a regular file", ErrCorrupt),
			f.Close(),
		)
	}

	return &Object{
		ObjectMetadata: *meta,
		File:           f,
		Size:           fi.Size(),
		LastModified:   fi.ModTime(),
	}, nil
}

// DeleteObjectInput names the object DeleteObject deletes.
type DeleteObjectInput struct {
	Key string
}

// DeleteObject deletes the object. A missing object is not an error.
// Removing the metadata file is the commit point: if it fails, the object is
// left whole. If the data file cannot be removed afterwards, the object is
// deleted and the error reports the data file left behind. Corrupt metadata
// returns an error wrapping ErrCorrupt and deletes nothing.
func (b *Bucket) DeleteObject(ctx context.Context, input *DeleteObjectInput) error {
	name := hash(input.Key)

	if lErr := b.lock.Lock(ctx, name); lErr != nil {
		return fmt.Errorf("lock object: %w", lErr)
	}

	meta, mErr := b.meta(name)
	if errors.Is(mErr, ErrObjectNotFound) {
		return b.releaseWith(name, nil)
	}
	if mErr != nil {
		return b.unlockWith(name, mErr)
	}

	if rmErr := rmInRoot(b.metaRoot, name+metaFileExt, "meta file"); rmErr != nil {
		return b.unlockWith(name, rmErr)
	}

	return b.releaseWith(name, rmInRoot(b.objRoot, meta.DataFile, "data file"))
}

// Close closes the bucket's roots and its lock, giving up any name it holds.
func (b *Bucket) Close() error {
	if err := util.CloseWithErr(nil, b.metaRoot, b.objRoot, b.lock); err != nil {
		return fmt.Errorf("close bucket: %w", err)
	}
	return nil
}

// finishCreate gives up the lock CreateObject took on name, which wrote an
// object when wrote is set and returned err. A failed write of a key with no
// metadata leaves no object, so its lock file need not stay, as in
// OpenObject. Removing it is safe while the lock is held: a waiter on the
// removed file sees it is no longer current and retries. A key whose
// metadata exists, even corrupt, keeps its lock file.
func (b *Bucket) finishCreate(name string, wrote bool, err error) error {
	if err != nil && !wrote && b.metaMissing(name) {
		return b.releaseWith(name, err)
	}
	return b.unlockWith(name, err)
}

// metaMissing reports whether name has no metadata file. Any other outcome
// of reading it, including corrupt metadata or an I/O error, counts as
// present, so the caller keeps the lock file.
func (b *Bucket) metaMissing(name string) bool {
	_, err := b.meta(name)
	return errors.Is(err, ErrObjectNotFound)
}

// unlockWith unlocks name, keeping its lock file, and returns err joined
// with any unlock error.
func (b *Bucket) unlockWith(name string, err error) error {
	if uErr := b.lock.Unlock(name); uErr != nil {
		return errors.Join(err, fmt.Errorf("unlock object: %w", uErr))
	}
	return err
}

// releaseWith unlocks name, removing its lock file, and returns err joined
// with any release error.
func (b *Bucket) releaseWith(name string, err error) error {
	if rErr := b.lock.Release(name); rErr != nil {
		return errors.Join(err, fmt.Errorf("release lock: %w", rErr))
	}
	return err
}

// meta reads and checks the metadata file of name. A missing file returns an
// error wrapping ErrObjectNotFound. A directory in its place, content that
// does not decode, null, or a data file name that isDataFileName rejects
// returns one wrapping ErrCorrupt. Errors never quote the file's content,
// which holds the user's key and metadata.
func (b *Bucket) meta(name string) (*ObjectMetadata, error) {
	file := name + metaFileExt

	data, err := b.metaRoot.ReadFile(file)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: read meta file: %w", ErrObjectNotFound, err)
		}
		if fi, sErr := b.metaRoot.Stat(file); sErr == nil && fi.IsDir() {
			return nil, fmt.Errorf("%w: read meta file: %w", ErrCorrupt, err)
		}
		return nil, fmt.Errorf("read meta file: %w", err)
	}

	var meta *ObjectMetadata
	if jsErr := json.Unmarshal(data, &meta); jsErr != nil {
		return nil, fmt.Errorf("%w: decode meta file: %s", ErrCorrupt, describeJSONError(jsErr))
	}
	if meta == nil {
		return nil, fmt.Errorf("%w: meta file holds null", ErrCorrupt)
	}
	if !isDataFileName(name, meta.DataFile) {
		return nil, fmt.Errorf("%w: meta file names no valid data file", ErrCorrupt)
	}

	return meta, nil
}

// describeJSONError describes a metadata decode error by its offset alone.
// The error's own message can quote a metadata key (the field path of a type
// error) or bytes of the file, which are user data.
func describeJSONError(err error) string {
	if se, ok := errors.AsType[*json.SyntaxError](err); ok {
		return fmt.Sprintf("syntax error at offset %d", se.Offset)
	}
	if te, ok := errors.AsType[*json.UnmarshalTypeError](err); ok {
		return fmt.Sprintf("type mismatch at offset %d", te.Offset)
	}
	return "invalid JSON"
}

// sniffContentType returns contentType when it is set and not empty.
// Otherwise it detects the type from the first fileHeadSize bytes of body,
// and returns a reader that still yields all of body.
func sniffContentType(
	body io.Reader,
	contentType *string,
) (string, io.Reader, error) {
	if contentType != nil && *contentType != "" {
		return *contentType, body, nil
	}

	head, reader, hErr := fileHead(body)
	if hErr != nil {
		return "", nil, hErr
	}
	return http.DetectContentType(head), reader, nil
}

// writeMetaFile writes meta as the metadata file of name: to a temp file
// first, then renamed over the current one, so readers see the old metadata
// or the new, never a partial file. The temp file is removed on failure.
func (b *Bucket) writeMetaFile(name string, meta *ObjectMetadata) error {
	data, jsErr := json.Marshal(meta)
	if jsErr != nil {
		return fmt.Errorf("encode meta file: %w", jsErr)
	}

	file := name + metaFileExt
	tmp := file + uuid.New().String()

	if wErr := b.metaRoot.WriteFile(tmp, data, filePerm); wErr != nil {
		return discardTmp(b.metaRoot, tmp, fmt.Errorf("write meta file: %w", wErr))
	}

	if rErr := b.metaRoot.Rename(tmp, file); rErr != nil {
		return discardTmp(b.metaRoot, tmp, fmt.Errorf("commit meta file: %w", rErr))
	}

	return nil
}
