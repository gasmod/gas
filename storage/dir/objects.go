package dir

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"

	"github.com/gasmod/gas/storage"
	"github.com/gasmod/gas/storage/dir/internal/vfs"
)

// PutObject writes the object in, as described in the package
// documentation: it reads the body into a new file, checking its context
// between reads, then commits it under the key's lock once the condition
// holds.
func (p *Provider) PutObject(
	ctx context.Context,
	in *storage.PutObjectInput,
) (*storage.PutObjectOutput, error) {
	if in == nil {
		return nil, opError(opPutObject, storage.ErrNilInput)
	}

	sess, err := p.begin(ctx)
	if err != nil {
		return nil, opError(opPutObject, err)
	}
	defer sess.End()

	req, err := storage.CheckPut(in, sess.Value.settings.Bucket)
	if err != nil {
		return nil, opError(opPutObject, err)
	}

	cctx, stop := callContext(ctx, sess)
	defer stop()

	var out vfs.Written

	err = withBucket(sess.Value, req.Bucket, func(bk *vfs.Bucket) error {
		var wErr error

		out, wErr = bk.CreateObject(cctx, &vfs.Write{
			Headers: headers(&req.HeaderValues),
			Body:    req.Body,
			Done: func() error {
				return cctx.Err()
			},
			Metadata:    req.Metadata,
			Key:         req.Key,
			IfMatch:     req.IfMatch,
			IfNoneMatch: req.IfNoneMatch,
		})

		return wErr //nolint:wrapcheck // the method classifies it.
	})
	if err != nil {
		return nil, opError(opPutObject, failure(ctx, sess, err, !isRead))
	}

	p.reportStale(opPutObject, &out)

	return &storage.PutObjectOutput{ETag: out.ETag, Size: out.Size}, nil
}

// reportStale logs at warn level, and passes to the skip handler, the data
// file of the object a write of op replaced, when out says it stayed.
func (p *Provider) reportStale(op string, out *vfs.Written) {
	if out.Stale == "" {
		return
	}

	err := fmt.Errorf("data file of the object replaced left in place: %w", out.StaleErr)
	p.logger.Warn(op+": data file of the object replaced left in place").
		Err("error", err).
		Send()
	p.report(vfs.Skipped{Path: out.Stale, Err: err})
}

// headers returns the header fields of v as the vfs package stores them.
func headers(v *storage.HeaderValues) vfs.Headers {
	return vfs.Headers{
		CacheControl:       v.CacheControl,
		ContentDisposition: v.ContentDisposition,
		ContentEncoding:    v.ContentEncoding,
		ContentLanguage:    v.ContentLanguage,
		ContentType:        v.ContentType,
		Expires:            v.Expires,
	}
}

// info returns the description of st, with a copy of its metadata, for
// size bytes selected.
func info(st *vfs.ObjectStat, size int64) storage.ObjectInfo {
	return storage.ObjectInfo{
		CacheControl:       st.CacheControl,
		ContentDisposition: st.ContentDisposition,
		ContentEncoding:    st.ContentEncoding,
		ContentLanguage:    st.ContentLanguage,
		ContentType:        st.ContentType,
		Expires:            st.Expires,
		LastModified:       st.LastModified,
		Metadata:           maps.Clone(st.User),
		ETag:               st.ETag,
		Size:               size,
	}
}

// HeadObject describes the object in, as described in the package
// documentation.
func (p *Provider) HeadObject(
	ctx context.Context,
	in *storage.HeadObjectInput,
) (*storage.ObjectInfo, error) {
	if in == nil {
		return nil, opError(opHeadObject, storage.ErrNilInput)
	}

	obj, _, length, err := p.read(ctx, in)
	if err != nil {
		return nil, opError(opHeadObject, err)
	}

	err = obj.File.Close()
	if err != nil {
		return nil, opError(
			opHeadObject,
			fmt.Errorf("%w: close data file: %w", storage.ErrBackend, pathFree(err)),
		)
	}

	out := info(&obj.ObjectStat, length)

	return &out, nil
}

// GetObject returns the object in, as described in the package
// documentation. Its Body is also an io.Seeker over the bytes selected,
// and stays readable after Close.
func (p *Provider) GetObject(
	ctx context.Context,
	in *storage.GetObjectInput,
) (*storage.Object, error) {
	if in == nil {
		return nil, opError(opGetObject, storage.ErrNilInput)
	}

	obj, offset, length, err := p.read(ctx, &in.HeadObjectInput)
	if err != nil {
		return nil, opError(opGetObject, err)
	}

	var body io.ReadCloser = obj.File

	if offset != 0 || length != obj.Size {
		body = &sectionBody{
			SectionReader: io.NewSectionReader(obj.File, offset, length),
			file:          obj.File,
		}
	}

	return &storage.Object{Body: body, ObjectInfo: info(&obj.ObjectStat, length)}, nil
}

// sectionBody is the Body of a ranged GetObject: an io.SectionReader over
// the bytes selected of the data file, so that it is an io.ReadSeeker as
// the *os.File of a whole read is, whose Close closes the data file.
type sectionBody struct {
	*io.SectionReader

	// file is the data file.
	file *os.File
}

// Close closes the data file the body reads.
func (b *sectionBody) Close() error {
	return b.file.Close() //nolint:wrapcheck // the caller's own file, as an *os.File's Close.
}

// read checks in and opens the object it names, and returns it with the
// offset and the length of the bytes it selects. The caller closes its
// File.
func (p *Provider) read(
	ctx context.Context,
	in *storage.HeadObjectInput,
) (*vfs.Object, int64, int64, error) {
	sess, err := p.begin(ctx)
	if err != nil {
		return nil, 0, 0, err
	}
	defer sess.End()

	req, err := storage.CheckRead(in, sess.Value.settings.Bucket)
	if err != nil {
		return nil, 0, 0, err //nolint:wrapcheck // the method adds the name and the operation.
	}

	switch {
	case req.Version != "":
		return nil, 0, 0, errNoVersions
	case req.PartNumber != 0:
		return nil, 0, 0, errNoParts
	}

	cctx, stop := callContext(ctx, sess)
	defer stop()

	var obj *vfs.Object

	err = withBucket(sess.Value, req.Bucket, func(bk *vfs.Bucket) error {
		var oErr error

		obj, oErr = bk.OpenObject(cctx, req.Key)

		return oErr //nolint:wrapcheck // the method classifies it.
	})
	if err != nil {
		return nil, 0, 0, failure(ctx, sess, err, isRead)
	}

	if req.Range == nil {
		return obj, 0, obj.Size, nil
	}

	offset, length, err := req.Range.Resolve(obj.Size)
	if err != nil {
		return nil, 0, 0, errors.Join(err, obj.File.Close())
	}

	return obj, offset, length, nil
}

// DeleteObject deletes the object in, as described in the package
// documentation. Deleting a key that holds no object is not an error.
func (p *Provider) DeleteObject(ctx context.Context, in *storage.DeleteObjectInput) error {
	if in == nil {
		return opError(opDeleteObject, storage.ErrNilInput)
	}

	sess, err := p.begin(ctx)
	if err != nil {
		return opError(opDeleteObject, err)
	}
	defer sess.End()

	ref, err := storage.CheckDelete(in, sess.Value.settings.Bucket)
	if err != nil {
		return opError(opDeleteObject, err)
	}

	if ref.Version != "" {
		return opError(opDeleteObject, errNoVersions)
	}

	cctx, stop := callContext(ctx, sess)
	defer stop()

	err = withBucket(sess.Value, ref.Bucket, func(bk *vfs.Bucket) error {
		return bk.DeleteObject(cctx, ref.Key)
	})
	if err != nil {
		return opError(opDeleteObject, failure(ctx, sess, err, !isRead))
	}

	return nil
}
