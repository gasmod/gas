package memory

import (
	"context"

	"github.com/gasmod/gas/storage"
)

// CopyObject copies the object in names, as described in the package
// documentation: under the store's lock, it looks up the source and the
// destination's bucket, and stores the copy when the limits allow it.
// The copy shares the source's contents, which no write changes, but
// counts toward MaxBytes as an object of its own.
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

	p.mu.Lock()
	defer p.mu.Unlock()

	o, err := p.copyLocked(&req, sess.Value.settings)
	if err != nil {
		return nil, opError(opCopyObject, err)
	}

	return &storage.CopyObjectOutput{ETag: o.etag, Size: int64(len(o.data))}, nil
}

// copyLocked stores the copy req asks for, and returns it. The caller
// holds p.mu.
func (p *Provider) copyLocked(req *storage.CopyRequest, set Settings) (*object, error) {
	srcBucket, ok := p.buckets[req.Source.Bucket]
	if !ok {
		return nil, errMissingBucket
	}

	src, ok := srcBucket.objects[req.Source.Key]
	if !ok {
		return nil, storage.ErrObjectNotFound
	}

	err := storage.CheckCopySize(int64(len(src.data)))
	if err != nil {
		return nil, err //nolint:wrapcheck // CopyObject adds the name and the operation.
	}

	dstBucket, ok := p.buckets[req.Destination.Bucket]
	if !ok {
		return nil, storage.ErrBucketNotFound
	}

	o := &object{
		modified: now(),
		metadata: src.metadata,
		data:     src.data,
		etag:     src.etag,
		headers:  src.headers,
	}

	if req.Replace {
		o.metadata, o.headers = req.Metadata, req.HeaderValues
	}

	o.size = footprint(req.Destination.Key, o.data, o.metadata, &o.headers)

	err = p.place(dstBucket, req.Destination.Key, o, set, 0)
	if err != nil {
		return nil, err
	}

	return o, nil
}
