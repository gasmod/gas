package dir

import (
	"context"
	"errors"
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

// CreateBucket creates the bucket input.Bucket. The returned BucketInfo has
// the bucket name as both ID and Location. Every bucket under the root
// belongs to the caller, so a bucket that already exists is success, as for a
// bucket the caller owns on S3, and its objects are kept; this backend never
// returns storage.ErrBucketExists. An invalid name returns an error wrapping
// storage.ErrInvalidBucket.
func (p *Provider) CreateBucket(
	_ context.Context,
	input *storage.CreateBucketInput,
) (*storage.BucketInfo, error) {
	if err := p.checkReady(); err != nil {
		return nil, err
	}

	if input == nil {
		return nil, internal.Errorf(mod, "create bucket: %w", storage.ErrInputRequired)
	}

	if !internal.IsValidBucketName(input.Bucket) {
		return nil, internal.Errorf(mod, "create bucket: %w", storage.ErrInvalidBucket)
	}

	if err := p.vfs.CreateBucket(input.Bucket); err != nil && !errors.Is(err, vfs.ErrBucketExists) {
		return nil, opError("create bucket", nil, err)
	}

	return &storage.BucketInfo{
		ID:       input.Bucket,
		Location: input.Bucket,
	}, nil
}

// HeadObject returns the info of the object. A missing object returns an
// error wrapping storage.ErrNotFound (and not storage.ErrKeyNotFound, as an
// S3 HEAD), and a missing bucket one wrapping storage.ErrBucketNotFound. A set
// Version returns an error wrapping storage.ErrNotSupported, after the key and
// bucket are validated and before anything is read. A Range is applied as by
// GetObject, so Size is the length of the selected range, and an
// unsatisfiable one returns an error wrapping storage.ErrInvalidRange. The
// PartNumber and Response* fields of input are ignored.
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

	if err := p.rejectVersion("head", &input.BaseObjectInput, input.Version); err != nil {
		return nil, err
	}

	obj, err := p.openObject(ctx, "head", storage.ErrNotFound, &input.BaseObjectInput)
	if err != nil {
		return nil, err
	}

	if cErr := obj.File.Close(); cErr != nil {
		return nil, internal.Errorf(mod, "head: close data file: %w", cErr)
	}

	info := objectInfo(obj)
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
// The ContentLength and Expires fields of input are ignored, and the
// returned ObjectInfo carries no fields.
//
// The write is atomic: a failed PutObject leaves the previous object as it
// was and returns a nil ObjectInfo, with one exception. When the new object
// was written but the previous data file could not be removed, PutObject
// returns the ObjectInfo together with an error wrapping ErrStaleData: the
// object reads back as written.
//
// A missing bucket returns an error wrapping storage.ErrBucketNotFound.
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

	bk, bkErr := p.vfs.OpenBucket(bucket)
	if bkErr != nil {
		return nil, opError("put", nil, bkErr)
	}

	out, err := bk.CreateObject(ctx, &vfs.CreateObjectInput{
		Key:         input.Key,
		Body:        input.Body,
		ContentType: input.ContentType,
		Metadata:    input.Metadata,
	})
	err = errors.Join(err, bk.Close())
	if err != nil {
		if out != nil && errors.Is(err, vfs.ErrStaleData) {
			return &storage.ObjectInfo{}, opError("put", nil, err)
		}
		return nil, opError("put", nil, err)
	}

	return &storage.ObjectInfo{}, nil
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
// or "bytes=-0") returns an error wrapping storage.ErrInvalidRange. The
// PartNumber and Response* fields of input are ignored.
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

	if err := p.rejectVersion("get", &input.BaseObjectInput, input.Version); err != nil {
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
	if err = errors.Join(err, bk.Close()); err != nil {
		return nil, opError("delete", nil, err)
	}

	return &storage.ObjectInfo{}, nil
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
	cErr := bk.Close()
	if oErr != nil {
		return nil, opError(op, notFound, errors.Join(oErr, cErr))
	}
	if cErr != nil {
		return nil, opError(op, notFound, errors.Join(cErr, obj.File.Close()))
	}

	return obj, nil
}

// objectInfo returns the storage view of an opened object.
func objectInfo(obj *vfs.Object) storage.ObjectInfo {
	return storage.ObjectInfo{
		Size:         obj.Size,
		LastModified: obj.LastModified,
		ContentType:  obj.ContentType,
		Metadata:     obj.Metadata,
	}
}

// rangedObject returns the Object for obj with header, the Range of a Get,
// applied: the body and Size of the selected bytes, the whole object when
// header is nil or ignored, or a bare storage.ErrInvalidRange. It does not
// close obj.File on error; the caller does.
func rangedObject(obj *vfs.Object, header *string) (*storage.Object, error) {
	out := &storage.Object{ObjectInfo: objectInfo(obj), Body: obj.File}
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

// rejectVersion validates in's key and bucket, then reports a set version as
// storage.ErrNotSupported: dir does not version objects. It returns nil when
// the input is valid and version is nil.
func (p *Provider) rejectVersion(op string, in *storage.BaseObjectInput, version *string) error {
	if _, err := internal.ValidateInput(in, p.config.Storage.Bucket); err != nil {
		return internal.Errorf(mod, "%s: %w", op, err)
	}
	if version != nil {
		return internal.Errorf(mod, "%s: version: %w", op, storage.ErrNotSupported)
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
