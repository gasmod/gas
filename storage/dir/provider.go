package dir

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/storage"
	"github.com/gasmod/gas/storage/dir/internal/vfs"
	"github.com/gasmod/gas/storage/internal"
)

// mod is the module path every provider error is prefixed with.
const mod = "dir/provider"

// Provider is a storage.Provider that stores buckets and objects in a
// directory, through an os.Root. Build it with NewProvider; a Provider that
// NewProvider did not build returns storage.ErrNotInitialized from every
// method.
type Provider struct {
	vfs    *vfs.VFS
	root   *os.Root
	config *Config
}

var _ storage.Provider = (*Provider)(nil)

// NewProvider returns a constructor for a Provider, for registration with the
// gas service container. The constructor takes the gas.ConfigProvider as its
// dependency, binds Config from it, and applies opts. A nil config provider
// returns an error. A configured bucket that is not a valid bucket name
// returns an error wrapping storage.ErrInvalidBucket; no root returns
// ErrRootRequired; a root the internal directories cannot be created in
// returns an error wrapping storage.ErrNotInitialized.
func NewProvider(opts ...Option) func(cfgProvider gas.ConfigProvider) (*Provider, error) {
	return func(cfgProvider gas.ConfigProvider) (*Provider, error) {
		p := &Provider{}

		cfg, err := internal.BindConfig[Config](cfgProvider)
		if err != nil {
			return nil, internal.Errorf(mod, "%w", err)
		}
		p.config = cfg

		if bk := p.config.Storage.Bucket; bk != "" && !internal.IsValidBucketName(bk) {
			return nil, internal.Errorf(mod, "%w: %s", storage.ErrInvalidBucket, bk)
		}

		for i, o := range opts {
			if o == nil {
				return nil, internal.Errorf(
					mod,
					"%w: option %d is nil",
					storage.ErrInvalidOption,
					i,
				)
			}
			o(p)
		}

		if p.root == nil {
			return nil, internal.Errorf(mod, "%w", ErrRootRequired)
		}

		v, vErr := vfs.NewVFS(p.root)
		if vErr != nil {
			return nil, internal.Errorf(mod, "%w: %w", storage.ErrNotInitialized, vErr)
		}
		p.vfs = v

		return p, nil
	}
}

// CreateBucket creates the bucket input.Bucket. It waits for a DeleteBucket
// of the same name in progress, until ctx is done. The returned Bucket has its
// Name and its CreationDate, when CreateBucket first ran for it; ID and
// Region are empty. Every bucket under the root
// belongs to the caller, so a bucket that already exists is success, as for a
// bucket the caller owns on S3, and its objects are kept; this backend never
// returns storage.ErrBucketExists. An invalid name returns an error wrapping
// storage.ErrInvalidBucket.
func (p *Provider) CreateBucket(
	ctx context.Context,
	input *storage.CreateBucketInput,
) (*storage.Bucket, error) {
	if err := p.checkReady(); err != nil {
		return nil, err
	}

	if input == nil {
		return nil, internal.Errorf(mod, "create bucket: %w", storage.ErrInputRequired)
	}

	if !internal.IsValidBucketName(input.Bucket) {
		return nil, internal.Errorf(mod, "create bucket: %w", storage.ErrInvalidBucket)
	}

	entry, err := p.vfs.CreateBucket(ctx, input.Bucket)
	if err != nil && !errors.Is(err, vfs.ErrBucketExists) {
		return nil, opError("create bucket", nil, err)
	}
	return &storage.Bucket{Name: input.Bucket, CreationDate: entry.Created}, nil
}

// HeadBucket reports whether the bucket input.Bucket exists. The returned
// Bucket has its Name and, when its name record names it, its CreationDate;
// ID and Region are empty. A bucket whose name record is missing or damaged,
// which ListBuckets leaves out, exists with a zero CreationDate. A missing
// bucket returns an error wrapping storage.ErrBucketNotFound, and an invalid
// name one wrapping storage.ErrInvalidBucket. This backend never returns
// storage.ErrAccessDenied.
func (p *Provider) HeadBucket(
	_ context.Context,
	input *storage.HeadBucketInput,
) (*storage.Bucket, error) {
	if err := p.checkReady(); err != nil {
		return nil, err
	}

	if input == nil {
		return nil, internal.Errorf(mod, "head bucket: %w", storage.ErrInputRequired)
	}

	if !internal.IsValidBucketName(input.Bucket) {
		return nil, internal.Errorf(mod, "head bucket: %w", storage.ErrInvalidBucket)
	}

	entry, err := p.vfs.HeadBucket(input.Bucket)
	if err != nil {
		return nil, opError("head bucket", nil, err)
	}
	return &storage.Bucket{Name: entry.Name, CreationDate: entry.Created}, nil
}

// DeleteBucket deletes the bucket input.Bucket, which must hold no object: a
// bucket that does returns an error wrapping storage.ErrBucketNotEmpty and
// keeps everything. A missing bucket returns an error wrapping
// storage.ErrBucketNotFound, and an invalid name one wrapping
// storage.ErrInvalidBucket.
//
// Entries no read can use do not count as objects, so they never keep a
// bucket from being deleted: metadata that cannot be used, metadata whose
// data file is gone, and the temp files of crashed writes are removed with
// the bucket, but only once no object was found. Their contents could not
// be read through this backend anyway. A write that lands while the delete
// runs can still make it return storage.ErrBucketNotEmpty after that debris
// is gone. A file in the bucket's metadata directory that this backend did
// not write stops the delete with an error.
//
// A write of an existing key in progress is waited for, never cut short, so
// a slow PutObject holds DeleteBucket, and with it a CreateBucket of the
// same name, until it ends or ctx is done. A write of a new key still
// copying its body is not waited for: the bucket is deleted under it, and
// the PutObject returns an error wrapping storage.ErrBucketNotFound.
//
// A bucket created again afterwards gets a new CreationDate, except after a
// crash between the delete's commit and its removal of the bucket's name
// record, which leaves the old CreationDate in place.
func (p *Provider) DeleteBucket(ctx context.Context, input *storage.DeleteBucketInput) error {
	if err := p.checkReady(); err != nil {
		return err
	}

	if input == nil {
		return internal.Errorf(mod, "delete bucket: %w", storage.ErrInputRequired)
	}

	if !internal.IsValidBucketName(input.Bucket) {
		return internal.Errorf(mod, "delete bucket: %w", storage.ErrInvalidBucket)
	}

	if err := p.vfs.DeleteBucket(ctx, input.Bucket); err != nil {
		return opError("delete bucket", nil, err)
	}
	return nil
}

// HeadObject returns the info of the object. A missing object returns an
// error wrapping storage.ErrNotFound (and not storage.ErrKeyNotFound, as an
// S3 HEAD), and a missing bucket one wrapping storage.ErrBucketNotFound. A set
// Version returns an error wrapping storage.ErrNotSupported, after the key and
// bucket are validated and before anything is read. A Range is applied as by
// GetObject, so Size is the length of the selected range, and an
// unsatisfiable one returns an error wrapping storage.ErrInvalidRange. A set
// PartNumber returns an error wrapping storage.ErrNotSupported, as Version
// does. The Response* fields of input are ignored.
func (p *Provider) HeadObject(
	ctx context.Context,
	input *storage.HeadObjectInput,
) (*storage.ObjectInfo, error) {
	if err := p.checkReady(); err != nil {
		return nil, err
	}

	if input == nil {
		return nil, internal.Errorf(mod, "head: %w", storage.ErrInputRequired)
	}

	if err := p.rejectUnsupported("head", input); err != nil {
		return nil, err
	}

	obj, err := p.openObject(ctx, "head", storage.ErrNotFound, &input.BaseObjectInput)
	if err != nil {
		return nil, err
	}

	if cErr := obj.File.Close(); cErr != nil {
		return nil, internal.Errorf(mod, "head: close data file: %w", cErr)
	}

	info := objectInfo(&obj.ObjectStat)
	if input.Range != nil {
		_, n, rErr := parseByteRange(*input.Range, obj.Size)
		if rErr != nil {
			return nil, internal.Errorf(mod, "head: %w", rErr)
		}
		info.Size = n
	}
	return &info, nil
}

// PutObject writes the object, replacing any object at its key. A nil Body
// is an empty object; a nil or empty ContentType is sniffed from the body.
// A set ContentLength must match the body: a shorter or longer body, or a
// negative length, returns an error wrapping storage.ErrContentLengthMismatch
// and stores nothing. The bucket is checked first, so a missing bucket is
// reported as such even when the length would also mismatch. Body is read to
// EOF, so a body that blocks after exactly ContentLength bytes without ending
// blocks PutObject. Every header of input is stored as given, Expires
// included, and the returned ObjectInfo is the object as HeadObject reads it,
// with its ETag, the hex MD5 of the body.
//
// A WriteCondition is checked under the object's lock, against the object
// the write would replace, so it holds across processes sharing the root. A
// condition that does not hold returns an error wrapping
// storage.ErrPreconditionFailed, and an IfMatch of a missing key one
// wrapping storage.ErrKeyNotFound, unless the key is missing because
// DeleteBucket removed the bucket during the call, which is
// storage.ErrBucketNotFound; either way nothing is written. An object
// stored by a version of this backend that recorded no ETag, or whose
// metadata is corrupt, matches no IfMatch, and corrupt metadata fails
// IfNoneMatch too. An invalid condition returns an error wrapping
// storage.ErrInvalidCondition before anything is read.
//
// The write is atomic: a failed PutObject leaves the previous object as it
// was and returns a nil ObjectInfo, with one exception. When the new object
// was written but the previous data file could not be removed, PutObject
// returns the ObjectInfo together with an error wrapping ErrStaleData: the
// object reads back as written.
//
// A missing bucket returns an error wrapping storage.ErrBucketNotFound, and
// so does a bucket that DeleteBucket removes while the body is being
// written: the delete does not wait for a write of a new key, since the
// bucket holds no object yet, and the write then finds no bucket to commit
// to and stores nothing, even if a bucket of the same name is created
// again before it ends.
func (p *Provider) PutObject(
	ctx context.Context,
	input *storage.PutObjectInput,
) (*storage.ObjectInfo, error) {
	if err := p.checkReady(); err != nil {
		return nil, err
	}

	if input == nil {
		return nil, internal.Errorf(mod, "put: %w", storage.ErrInputRequired)
	}

	bucket, inErr := internal.ValidateInput(&input.BaseObjectInput, p.config.Storage.Bucket)
	if inErr != nil {
		return nil, internal.Errorf(mod, "put: %w", inErr)
	}

	ifMatch, cErr := internal.ValidateCondition(&input.WriteCondition)
	if cErr != nil {
		return nil, internal.Errorf(mod, "put: %w", cErr)
	}

	bk, bkErr := p.vfs.OpenBucket(bucket)
	if bkErr != nil {
		return nil, opError("put", nil, bkErr)
	}

	// Checked after the bucket, so a missing bucket is reported first.
	body, lenErr := internal.CheckLength(input.Body, input.ContentLength)
	if lenErr != nil {
		return nil, internal.Errorf(mod, "put: %w", errors.Join(lenErr, bk.Close()))
	}

	out, err := bk.CreateObject(ctx, &vfs.CreateObjectInput{
		Key:         input.Key,
		Body:        body,
		ContentType: input.ContentType,
		Metadata:    input.Metadata,
		Headers: vfs.Headers{
			CacheControl:       valueOf(input.CacheControl),
			ContentDisposition: valueOf(input.ContentDisposition),
			ContentEncoding:    valueOf(input.ContentEncoding),
			ContentLanguage:    valueOf(input.ContentLanguage),
			Expires:            valueOf(input.Expires),
		},
		Condition: vfs.Condition{IfMatch: ifMatch, IfNoneMatch: input.IfNoneMatch != nil},
	})
	return p.putResult(bk, out, err)
}

// putResult closes bk and returns what PutObject reports for a write into
// it that returned out and err: the object, the ObjectInfo with an
// ErrStaleData error when the object was written but the old data file
// stayed, or a nil ObjectInfo. A write that failed for want of its files
// because DeleteBucket removed the bucket under it, even if a bucket of the
// same name was created since, reports the bucket missing.
func (p *Provider) putResult(
	bk *vfs.Bucket,
	out *vfs.ObjectStat,
	err error,
) (*storage.ObjectInfo, error) {
	if err = errors.Join(p.removedErr(bk, err), bk.Close()); err != nil {
		if out != nil && errors.Is(err, vfs.ErrStaleData) {
			info := objectInfo(out)
			return &info, opError("put", nil, err)
		}
		// Only an IfMatch of a missing key reports the object missing.
		return nil, opError("put", storage.ErrKeyNotFound, err)
	}
	info := objectInfo(out)
	return &info, nil
}

// GetObject opens the object for reading. The caller closes Body, which keeps
// reading the body it opened even if the object is overwritten or deleted
// meanwhile. A missing object returns an error wrapping
// storage.ErrKeyNotFound, and a missing bucket one wrapping
// storage.ErrBucketNotFound. A set Version returns an error wrapping
// storage.ErrNotSupported, as for HeadObject.
//
// A Range selects part of the object with RFC 9110 single-range semantics,
// matching S3: Body reads only those bytes and Size is their count. A Range
// that is not a single valid byte range is ignored and the whole object is
// returned; one that cannot be satisfied (its first byte at or past the end,
// or "bytes=-0") returns an error wrapping storage.ErrInvalidRange. A set
// PartNumber returns an error wrapping storage.ErrNotSupported. The
// Response* fields of input are ignored.
func (p *Provider) GetObject(
	ctx context.Context,
	input *storage.GetObjectInput,
) (*storage.Object, error) {
	if err := p.checkReady(); err != nil {
		return nil, err
	}

	if input == nil {
		return nil, internal.Errorf(mod, "get: %w", storage.ErrInputRequired)
	}

	if err := p.rejectUnsupported("get", &input.HeadObjectInput); err != nil {
		return nil, err
	}

	obj, err := p.openObject(ctx, "get", storage.ErrKeyNotFound, &input.BaseObjectInput)
	if err != nil {
		return nil, err
	}

	out, rErr := rangedObject(obj, input.Range)
	if rErr != nil {
		if cErr := obj.File.Close(); cErr != nil {
			rErr = errors.Join(rErr, cErr)
		}
		return nil, internal.Errorf(mod, "get: %w", rErr)
	}
	return out, nil
}

// DeleteObject deletes the object. Deleting a missing object succeeds, as in
// S3; a missing bucket returns an error wrapping storage.ErrBucketNotFound.
// A set Version returns an error wrapping storage.ErrNotSupported and deletes
// nothing. The returned ObjectInfo carries no fields.
func (p *Provider) DeleteObject(
	ctx context.Context,
	input *storage.DeleteObjectInput,
) (*storage.ObjectInfo, error) {
	if err := p.checkReady(); err != nil {
		return nil, err
	}

	if input == nil {
		return nil, internal.Errorf(mod, "delete: %w", storage.ErrInputRequired)
	}

	bucket, inErr := internal.ValidateInput(&input.BaseObjectInput, p.config.Storage.Bucket)
	if inErr != nil {
		return nil, internal.Errorf(mod, "delete: %w", inErr)
	}

	if input.Version != nil {
		return nil, internal.Errorf(mod, "delete: version: %w", storage.ErrNotSupported)
	}

	bk, bkErr := p.vfs.OpenBucket(bucket)
	if bkErr != nil {
		return nil, opError("delete", nil, bkErr)
	}

	err := bk.DeleteObject(ctx, &vfs.DeleteObjectInput{Key: input.Key})
	if err = errors.Join(p.removedErr(bk, err), bk.Close()); err != nil {
		return nil, opError("delete", nil, err)
	}

	return &storage.ObjectInfo{}, nil
}

// removedErr returns err, an error from an operation on bk, marked as the
// bucket's absence when it is a missing file (see vfs.IsMissing, which on
// Windows includes a delete-pending directory) because DeleteBucket removed
// bk while the operation ran, even if a bucket of the same name was created
// since (see vfs.Removed). Without that, such an operation would report a
// bare missing file, or a missing key where the whole bucket is gone.
func (p *Provider) removedErr(bk *vfs.Bucket, err error) error {
	if vfs.IsMissing(err) && p.vfs.Removed(bk) {
		return fmt.Errorf("%w: deleted during the operation: %w", vfs.ErrBucketNotFound, err)
	}
	return err
}

// Root returns the root the provider stores into, or nil for a Provider that
// NewProvider did not build. The caller that opened the root closes it.
func (p *Provider) Root() *os.Root {
	return p.root
}

// checkReady returns an error wrapping storage.ErrNotInitialized unless
// NewProvider built p.
func (p *Provider) checkReady() error {
	if p.vfs == nil || p.config == nil {
		return internal.Errorf(mod, "%w", storage.ErrNotInitialized)
	}
	return nil
}

// openObject validates in, then opens the object it names for reading. The
// bucket is closed before it returns; the caller closes the object's File.
// Errors are complete operation errors for op, with a missing key mapped to
// notFound.
func (p *Provider) openObject(
	ctx context.Context,
	op string,
	notFound error,
	in *storage.BaseObjectInput,
) (*vfs.Object, error) {
	bucket, inErr := internal.ValidateInput(in, p.config.Storage.Bucket)
	if inErr != nil {
		return nil, internal.Errorf(mod, "%s: %w", op, inErr)
	}

	bk, bkErr := p.vfs.OpenBucket(bucket)
	if bkErr != nil {
		return nil, opError(op, notFound, bkErr)
	}

	obj, oErr := bk.OpenObject(ctx, &vfs.OpenObjectInput{Key: in.Key})
	oErr = p.removedErr(bk, oErr)
	cErr := bk.Close()
	if oErr != nil {
		return nil, opError(op, notFound, errors.Join(oErr, cErr))
	}
	if cErr != nil {
		return nil, opError(op, notFound, errors.Join(cErr, obj.File.Close()))
	}

	return obj, nil
}

// objectInfo returns the storage view of an object.
func objectInfo(obj *vfs.ObjectStat) storage.ObjectInfo {
	return storage.ObjectInfo{
		Size:               obj.Size,
		LastModified:       obj.LastModified,
		ContentType:        obj.ContentType,
		Metadata:           obj.Metadata,
		CacheControl:       obj.CacheControl,
		ContentDisposition: obj.ContentDisposition,
		ContentEncoding:    obj.ContentEncoding,
		ContentLanguage:    obj.ContentLanguage,
		Expires:            obj.Expires,
		ETag:               obj.ETag,
	}
}

// rangedObject returns the Object for obj with header, the Range of a Get,
// applied: the body and Size of the selected bytes, the whole object when
// header is nil or ignored, or a bare storage.ErrInvalidRange. It does not
// close obj.File on error; the caller does.
func rangedObject(obj *vfs.Object, header *string) (*storage.Object, error) {
	out := &storage.Object{ObjectInfo: objectInfo(&obj.ObjectStat), Body: obj.File}
	if header == nil {
		return out, nil
	}
	off, n, err := parseByteRange(*header, obj.Size)
	if err != nil {
		return nil, err
	}
	if off != 0 || n != obj.Size {
		if out.Body, err = rangeBody(obj.File, off, n); err != nil {
			return nil, err
		}
	}
	out.Size = n
	return out, nil
}

// rangeBody returns a ReadCloser that reads n bytes of f from off and closes
// f. f must support io.ReaderAt, which every data file opened from the root
// does.
func rangeBody(f fs.File, off, n int64) (io.ReadCloser, error) {
	ra, ok := f.(io.ReaderAt)
	if !ok {
		return nil, errors.New("data file does not support ranged reads")
	}
	return struct {
		io.Reader
		io.Closer
	}{io.NewSectionReader(ra, off, n), f}, nil
}

// rejectUnsupported validates in's key and bucket, then reports a set
// Version or PartNumber as storage.ErrNotSupported: dir neither versions
// objects nor stores them in parts. It returns nil when the input is valid
// and asks for neither.
func (p *Provider) rejectUnsupported(op string, in *storage.HeadObjectInput) error {
	if _, err := internal.ValidateInput(&in.BaseObjectInput, p.config.Storage.Bucket); err != nil {
		return internal.Errorf(mod, "%s: %w", op, err)
	}
	if in.Version != nil {
		return internal.Errorf(mod, "%s: version: %w", op, storage.ErrNotSupported)
	}
	if in.PartNumber != nil {
		return internal.Errorf(mod, "%s: part number: %w", op, storage.ErrNotSupported)
	}
	return nil
}

// opError returns the error op reports for err, a vfs error: the provider
// prefix and op, then the storage sentinel its vfs sentinel maps to, if any,
// then err. This is the same shape as the s3 backend's errors. A missing
// object maps to notFound, which is nil for operations where a missing object
// is not an error.
func opError(op string, notFound, err error) error {
	var sentinel error
	switch {
	case errors.Is(err, vfs.ErrBucketNotFound):
		sentinel = storage.ErrBucketNotFound
	case errors.Is(err, vfs.ErrBucketNotEmpty):
		sentinel = storage.ErrBucketNotEmpty
	case errors.Is(err, vfs.ErrPreconditionFailed):
		sentinel = storage.ErrPreconditionFailed
	case errors.Is(err, vfs.ErrObjectNotFound):
		sentinel = notFound
	case errors.Is(err, vfs.ErrStaleData):
		sentinel = ErrStaleData
	}
	if sentinel != nil {
		return internal.Errorf(mod, "%s: %w: %w", op, sentinel, err)
	}
	return internal.Errorf(mod, "%s: %w", op, err)
}
