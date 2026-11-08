package dir

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/gasmod/gas/storage"
	"github.com/gasmod/gas/storage/dir/internal/vfs"
	"github.com/gasmod/gas/storage/internal/lifecycle"
)

// CopyObject copies the object in names, as described in the package
// documentation: it opens the source under its key's lock, as GetObject
// does, and gives the lock up, then writes the destination from the open
// data file under the destination key's lock, as PutObject does. It never
// holds two keys' locks at once, so copies that cross cannot deadlock,
// and the copy is written whole or not at all.
func (p *Provider) CopyObject(
	ctx context.Context,
	in *storage.CopyObjectInput,
) (*storage.CopyObjectOutput, error) {
	if in == nil {
		return nil, opError(opCopyObject, storage.ErrNilInput)
	}

	sess, err := p.begin(ctx)
	if err != nil {
		return nil, opError(opCopyObject, err)
	}
	defer sess.End()

	req, err := storage.CheckCopy(in, sess.Value.settings.Bucket)
	if err != nil {
		return nil, opError(opCopyObject, err)
	}

	cctx, stop := callContext(ctx, sess)
	defer stop()

	var src *vfs.Object

	err = withBucket(sess.Value, req.Source.Bucket, func(bk *vfs.Bucket) error {
		var oErr error

		src, oErr = bk.OpenObject(cctx, req.Source.Key)

		return oErr //nolint:wrapcheck // the method classifies it.
	})
	if err != nil {
		return nil, opError(opCopyObject, failure(ctx, sess, err, isRead))
	}

	out, err := p.writeCopy(ctx, cctx, sess, &req, src)
	if err != nil {
		return nil, opError(opCopyObject, err)
	}

	return out, nil
}

// writeCopy writes the copy req asks for from src, the source opened,
// within cctx, the context of the call that began on sess with ctx, and
// closes src's data file. It returns the error classified, as failure
// does.
func (p *Provider) writeCopy(
	ctx, cctx context.Context,
	sess *lifecycle.Session[*session],
	req *storage.CopyRequest,
	src *vfs.Object,
) (*storage.CopyObjectOutput, error) {
	body := &sourceBody{file: src.File}

	err := storage.CheckCopySize(src.Size)
	if err != nil {
		return nil, errors.Join(err, body.close())
	}

	w := &vfs.Write{
		Headers:  src.Headers,
		Body:     body,
		Done:     cctx.Err,
		Metadata: src.User,
		Key:      req.Destination.Key,
	}

	if req.Replace {
		w.Headers, w.Metadata = headers(&req.HeaderValues), req.Metadata
	}

	var out vfs.Written

	err = withBucket(sess.Value, req.Destination.Bucket, func(bk *vfs.Bucket) error {
		var wErr error

		out, wErr = bk.CreateObject(cctx, w)

		return wErr //nolint:wrapcheck // the method classifies it.
	})
	if err != nil {
		return nil, errors.Join(failure(ctx, sess, err, !isRead), body.close())
	}

	p.reportStale(opCopyObject, &out)

	return &storage.CopyObjectOutput{ETag: out.ETag, Size: out.Size}, nil
}

// sourceBody is the body of the write of a copy: the source's data file,
// closed as soon as it is read to its end, so that a copy of an object
// onto itself does not hold open, on Windows, the data file the write
// then removes.
type sourceBody struct {
	// file is the data file, or nil once it is closed.
	file *os.File
}

// Read reads the data file, and closes it at its end. A failure to read
// or close it is the read's error, wrapping storage.ErrBackend.
func (b *sourceBody) Read(p []byte) (int, error) {
	if b.file == nil {
		return 0, io.EOF
	}

	n, err := b.file.Read(p)

	switch {
	case err == nil:
		return n, nil
	case !errors.Is(err, io.EOF):
		return n, fmt.Errorf("%w: read data file: %w", storage.ErrBackend, pathFree(err))
	}

	cErr := b.close()
	if cErr != nil {
		return n, cErr
	}

	return n, io.EOF
}

// close closes the data file, unless it is closed, and returns the error
// of the close wrapping storage.ErrBackend.
func (b *sourceBody) close() error {
	if b.file == nil {
		return nil
	}

	err := b.file.Close()

	b.file = nil

	if err != nil {
		return fmt.Errorf("%w: close data file: %w", storage.ErrBackend, pathFree(err))
	}

	return nil
}
