package memory

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // G501: the ETag is an MD5 of the contents, as on S3, not a security check.
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/gasmod/gas/storage"
	"github.com/gasmod/gas/storage/internal/lifecycle"
	"github.com/gasmod/gas/storage/internal/listing"
)

// chunkSize is how many bytes PutObject reads from a body at a time,
// checking its context between reads.
const chunkSize = 32 << 10

// objectScope is the first part of the scope of a ListObjects token, so
// that it never opens as a ListBuckets one.
const objectScope = "objects"

// The reasons the store refuses what it does not keep.
var (
	// errNoVersions reports a Version on a store that keeps none.
	errNoVersions = fmt.Errorf("%w: the store keeps no versions", storage.ErrNotSupported)
	// errNoParts reports a PartNumber on a store that keeps no parts.
	errNoParts = fmt.Errorf("%w: the store keeps no parts", storage.ErrNotSupported)
	// errMissingBucket is a read of an object whose bucket does not
	// exist.
	errMissingBucket = fmt.Errorf("%w: %w", storage.ErrObjectNotFound, storage.ErrBucketNotFound)
)

// object is a stored object. Nothing changes it once it is stored: a
// write stores a new one.
type object struct {
	// modified is when the object was written, in UTC to the second.
	modified time.Time
	// metadata is the user metadata, which is never handed out.
	metadata map[string]string
	// etag is the hex MD5 of data.
	etag string
	// headers are the header fields.
	headers storage.HeaderValues
	// data is the contents.
	data []byte
	// size is what the object counts toward MaxBytes.
	size int64
}

// footprint returns what an object of data, metadata md and header fields
// h stored under key counts toward MaxBytes: the bytes of the key, of the
// contents, of the names and values of the metadata, and of the header
// values.
func footprint(key string, data []byte, md map[string]string, h *storage.HeaderValues) int64 {
	n := len(key) + len(data) + len(h.CacheControl) + len(h.ContentDisposition) +
		len(h.ContentEncoding) + len(h.ContentLanguage) + len(h.ContentType) + len(h.Expires)

	for k, v := range md {
		n += len(k) + len(v)
	}

	return int64(n)
}

// info returns the description of o, with a copy of its metadata, for
// size bytes selected.
func (o *object) info(size int64) storage.ObjectInfo {
	return storage.ObjectInfo{
		LastModified: o.modified,
		Metadata:     maps.Clone(o.metadata),
		ETag:         o.etag,
		HeaderValues: o.headers,
		Size:         size,
	}
}

// PutObject stores the object in, as described in the package
// documentation, once its body is read whole and its condition holds.
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

	err = p.bucketExists(req.Bucket)
	if err != nil {
		return nil, opError(opPutObject, err)
	}

	r := &reservation{p: p, limit: sess.Value.settings.MaxBytes}
	defer r.release()

	data, err := r.readBody(ctx, sess, req.Body)
	if err != nil {
		return nil, opError(opPutObject, err)
	}

	sum := md5.Sum(data) //nolint:gosec // G401: the ETag, as on S3.
	o := &object{
		modified: now(),
		metadata: req.Metadata,
		data:     data,
		etag:     hex.EncodeToString(sum[:]),
		headers:  req.HeaderValues,
		size:     footprint(req.Key, data, req.Metadata, &req.HeaderValues),
	}

	err = p.commit(ctx, sess, &req, o, r)
	if err != nil {
		return nil, opError(opPutObject, err)
	}

	return &storage.PutObjectOutput{ETag: o.etag, Size: int64(len(data))}, nil
}

// bucketExists returns storage.ErrBucketNotFound when the bucket name does
// not exist.
func (p *Provider) bucketExists(name string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if _, ok := p.buckets[name]; !ok {
		return storage.ErrBucketNotFound
	}

	return nil
}

// reservation is the room a PutObject holds in the store while it reads
// its body: the bytes read so far, counted in Provider.reserved, so that
// writes in flight never hold more than MaxBytes between them and the
// objects stored.
type reservation struct {
	// p is the store.
	p *Provider
	// limit is MaxBytes.
	limit int64
	// held is how many bytes the reservation holds; guarded by p.mu.
	held int64
}

// reserve takes n more bytes of room, or returns ErrStorageFull when the
// objects stored and the reservations of every write in flight leave
// less.
func (r *reservation) reserve(n int64) error {
	r.p.mu.Lock()
	defer r.p.mu.Unlock()

	if r.p.bytes+r.p.reserved+n > r.limit {
		return ErrStorageFull
	}

	r.p.reserved += n
	r.held += n

	return nil
}

// release gives the room back. The caller does not hold p.mu.
func (r *reservation) release() {
	r.p.mu.Lock()
	defer r.p.mu.Unlock()

	r.releaseLocked()
}

// releaseLocked gives the room back. The caller holds p.mu.
func (r *reservation) releaseLocked() {
	r.p.reserved -= r.held

	r.held = 0
}

// readBody reads body whole, checking ctx and the session's context
// before each read and after a read that fails, and reserves room for
// each chunk before it keeps it, refusing what does not fit with
// ErrStorageFull. It returns a copy of what it read whose capacity is
// its length, so the store keeps no more than it counts.
func (r *reservation) readBody(
	ctx context.Context,
	sess *lifecycle.Session[*session],
	body io.Reader,
) ([]byte, error) {
	var buf bytes.Buffer

	chunk := make([]byte, chunkSize)

	for {
		err := callError(ctx, sess)
		if err != nil {
			return nil, err
		}

		n, err := body.Read(chunk)

		rErr := r.reserve(int64(n))
		if rErr != nil {
			return nil, rErr
		}

		buf.Write(chunk[:n])

		switch {
		case errors.Is(err, io.EOF):
			return bytes.Clone(buf.Bytes()), nil
		case err != nil:
			// A body that failed because the call ended, such as one that
			// reads from the same context, reports the end.
			endErr := callError(ctx, sess)
			if endErr != nil {
				return nil, endErr
			}

			return nil, err //nolint:wrapcheck // storage.CheckLength wraps it; PutObject adds the name.
		}
	}
}

// callError returns ctx's error when it has ended, storage.ErrClosed when
// a Close canceled the session, and else nil.
func callError(ctx context.Context, sess *lifecycle.Session[*session]) error {
	switch {
	case ctx.Err() != nil:
		return ctx.Err() //nolint:wrapcheck // the method adds the name and the operation.
	case sess.Context().Err() != nil:
		return storage.ErrClosed
	}

	return nil
}

// commit stores o under the bucket and key of req, when the call goes on,
// the bucket exists, req's condition holds and the limits allow it, with
// the room the other writes in flight hold. It gives back the room of r,
// the reservation o's body was read under, once o is stored.
func (p *Provider) commit(
	ctx context.Context,
	sess *lifecycle.Session[*session],
	req *storage.PutRequest,
	o *object,
	r *reservation,
) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	err := callError(ctx, sess)
	if err != nil {
		return err
	}

	b, ok := p.buckets[req.Bucket]
	if !ok {
		return storage.ErrBucketNotFound
	}

	old := b.objects[req.Key]

	err = condition(req, old)
	if err != nil {
		return err
	}

	objects, bytes := p.objects+1, p.bytes+o.size
	if old != nil {
		objects, bytes = p.objects, bytes-old.size
	}

	set := sess.Value.settings
	if objects > set.MaxObjects || bytes+p.reserved-r.held > set.MaxBytes {
		return ErrStorageFull
	}

	b.objects[req.Key] = o
	p.objects, p.bytes = objects, bytes

	r.releaseLocked()

	return nil
}

// condition returns why req's condition does not hold over old, the
// object stored under its key or nil, or nil when it holds.
func condition(req *storage.PutRequest, old *object) error {
	switch {
	case req.IfNoneMatch && old != nil:
		return storage.ErrPreconditionFailed
	case req.IfMatch != "" && old == nil:
		return storage.ErrObjectNotFound
	case req.IfMatch != "" && old.etag != req.IfMatch:
		return storage.ErrPreconditionFailed
	}

	return nil
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

	o, _, length, err := p.read(ctx, in)
	if err != nil {
		return nil, opError(opHeadObject, err)
	}

	info := o.info(length)

	return &info, nil
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

	o, offset, length, err := p.read(ctx, &in.HeadObjectInput)
	if err != nil {
		return nil, opError(opGetObject, err)
	}

	return &storage.Object{
		Body:       readSeekCloser{bytes.NewReader(o.data[offset : offset+length])},
		ObjectInfo: o.info(length),
	}, nil
}

// readSeekCloser is a GetObject Body: a reader of bytes no one changes,
// whose Close does nothing.
type readSeekCloser struct {
	*bytes.Reader
}

// Close does nothing, and returns nil.
func (readSeekCloser) Close() error { return nil }

// read checks in and returns the object it names, with the offset and
// the length of the bytes it selects.
func (p *Provider) read(
	ctx context.Context,
	in *storage.HeadObjectInput,
) (*object, int64, int64, error) {
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

	o, err := p.lookup(req.Bucket, req.Key)

	switch {
	case err != nil:
		return nil, 0, 0, err
	case req.Range == nil:
		return o, 0, int64(len(o.data)), nil
	}

	offset, length, err := req.Range.Resolve(int64(len(o.data)))
	if err != nil {
		return nil, 0, 0, err //nolint:wrapcheck // the method adds the name and the operation.
	}

	return o, offset, length, nil
}

// lookup returns the object under key in bucket.
func (p *Provider) lookup(bucket, key string) (*object, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	b, ok := p.buckets[bucket]
	if !ok {
		return nil, errMissingBucket
	}

	o, ok := b.objects[key]
	if !ok {
		return nil, storage.ErrObjectNotFound
	}

	return o, nil
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

	p.mu.Lock()
	defer p.mu.Unlock()

	b, ok := p.buckets[ref.Bucket]
	if !ok {
		return opError(opDeleteObject, storage.ErrBucketNotFound)
	}

	if o, ok := b.objects[ref.Key]; ok {
		delete(b.objects, ref.Key)

		p.objects--

		p.bytes -= o.size
	}

	return nil
}

// ListObjects returns one page of the objects of a bucket, as described
// in the package documentation.
func (p *Provider) ListObjects(
	ctx context.Context,
	in *storage.ListObjectsInput,
) (*storage.ListObjectsOutput, error) {
	if in == nil {
		return nil, opError(opListObjects, storage.ErrNilInput)
	}

	sess, err := p.begin(ctx)
	if err != nil {
		return nil, opError(opListObjects, err)
	}
	defer sess.End()

	req, err := storage.CheckListObjects(in, sess.Value.settings.Bucket)
	if err != nil {
		return nil, opError(opListObjects, err)
	}

	codec := sess.Value.codec
	scope := listing.Scope(objectScope, req.Bucket, req.Prefix, req.Delimiter, req.StartAfter)
	q := listing.Query{
		Prefix:     req.Prefix,
		Delimiter:  req.Delimiter,
		StartAfter: req.StartAfter,
		Max:        req.MaxKeys,
	}

	if req.ContinuationToken != "" {
		q.After, err = codec.Open(scope, req.ContinuationToken)
		if err != nil {
			return nil, opError(opListObjects, err)
		}
	}

	entries, truncated, out, err := p.page(req, q)
	if err != nil {
		return nil, opError(opListObjects, err)
	}

	if truncated {
		last := entries[len(entries)-1]

		// A key is shorter than listing.MaxCursorLength, so Seal fails
		// only if that rule changes.
		token, sErr := codec.Seal(scope, listing.Cursor{After: last.Name, Prefix: last.Prefix})
		if sErr != nil {
			return nil, opError(opListObjects, fmt.Errorf("%w: %w", storage.ErrBackend, sErr))
		}

		out.NextContinuationToken = token
	}

	return out, nil
}

// page returns the entries of the page q selects from the bucket of req,
// whether another page follows, and the page as ListObjects returns it,
// without its token.
func (p *Provider) page(
	req storage.ListObjectsRequest,
	q listing.Query,
) ([]listing.Entry, bool, *storage.ListObjectsOutput, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	b, ok := p.buckets[req.Bucket]
	if !ok {
		return nil, false, nil, storage.ErrBucketNotFound
	}

	keys := slices.Sorted(func(yield func(string) bool) {
		for k := range b.objects {
			if strings.HasPrefix(k, req.Prefix) && !yield(k) {
				return
			}
		}
	})

	entries, truncated := listing.Page(slices.Values(keys), q)
	out := &storage.ListObjectsOutput{IsTruncated: truncated}

	for _, e := range entries {
		if e.Prefix {
			out.CommonPrefixes = append(out.CommonPrefixes, e.Name)

			continue
		}

		o := b.objects[e.Name]

		out.Objects = append(out.Objects, storage.ObjectSummary{
			LastModified: o.modified,
			Key:          e.Name,
			ETag:         o.etag,
			Size:         int64(len(o.data)),
		})
	}

	return entries, truncated, out, nil
}
