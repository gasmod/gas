package dir

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/gasmod/gas/storage"
	"github.com/gasmod/gas/storage/dir/internal/vfs"
	"github.com/gasmod/gas/storage/internal/listing"
)

// bucketScope is the first part of the scope of a ListBuckets token, so
// that it never opens as a ListObjects one.
const bucketScope = "buckets"

// CreateBucket creates the bucket in.Bucket, as described in the package
// documentation. Creating one that exists is not an error. It waits for a
// DeleteBucket of the same name in progress, until ctx ends.
func (p *Provider) CreateBucket(ctx context.Context, in *storage.CreateBucketInput) error {
	if in == nil {
		return opError(opCreateBucket, storage.ErrNilInput)
	}

	sess, err := p.begin(ctx)
	if err != nil {
		return opError(opCreateBucket, err)
	}
	defer sess.End()

	err = storage.CheckBucket(in.Bucket)
	if err != nil {
		return opError(opCreateBucket, err)
	}

	cctx, stop := callContext(ctx, sess)
	defer stop()

	err = sess.Value.vfs.CreateBucket(cctx, in.Bucket)
	if err != nil {
		return opError(opCreateBucket, failure(ctx, sess, err, !isRead))
	}

	return nil
}

// HeadBucket returns the bucket in.Bucket, with its name only, or an error
// wrapping storage.ErrBucketNotFound.
func (p *Provider) HeadBucket(
	ctx context.Context,
	in *storage.HeadBucketInput,
) (*storage.Bucket, error) {
	if in == nil {
		return nil, opError(opHeadBucket, storage.ErrNilInput)
	}

	sess, err := p.begin(ctx)
	if err != nil {
		return nil, opError(opHeadBucket, err)
	}
	defer sess.End()

	err = storage.CheckBucket(in.Bucket)
	if err != nil {
		return nil, opError(opHeadBucket, err)
	}

	err = sess.Value.vfs.HeadBucket(in.Bucket)
	if err != nil {
		return nil, opError(opHeadBucket, failure(ctx, sess, err, !isRead))
	}

	return &storage.Bucket{Name: in.Bucket}, nil
}

// DeleteBucket deletes the bucket in.Bucket when it holds no object, as
// described in the package documentation. It waits for the writes in
// progress of the keys the bucket holds, and for a CreateBucket of the
// same name, until ctx ends.
func (p *Provider) DeleteBucket(ctx context.Context, in *storage.DeleteBucketInput) error {
	if in == nil {
		return opError(opDeleteBucket, storage.ErrNilInput)
	}

	sess, err := p.begin(ctx)
	if err != nil {
		return opError(opDeleteBucket, err)
	}
	defer sess.End()

	err = storage.CheckBucket(in.Bucket)
	if err != nil {
		return opError(opDeleteBucket, err)
	}

	cctx, stop := callContext(ctx, sess)
	defer stop()

	err = sess.Value.vfs.DeleteBucket(cctx, in.Bucket, p.report)
	if err != nil {
		return opError(opDeleteBucket, failure(ctx, sess, err, !isRead))
	}

	return nil
}

// ListBuckets returns one page of the buckets, as described in the
// package documentation. A bucket that cannot be named is left out and
// passed to the skip handler.
func (p *Provider) ListBuckets(
	ctx context.Context,
	in *storage.ListBucketsInput,
) (*storage.ListBucketsOutput, error) {
	if in == nil {
		return nil, opError(opListBuckets, storage.ErrNilInput)
	}

	sess, err := p.begin(ctx)
	if err != nil {
		return nil, opError(opListBuckets, err)
	}
	defer sess.End()

	req, err := storage.CheckListBuckets(in)
	if err != nil {
		return nil, opError(opListBuckets, err)
	}

	codec := sess.Value.codec
	scope := listing.Scope(bucketScope, req.Prefix)
	q := listing.Query{Prefix: req.Prefix, Max: req.MaxBuckets}

	if req.ContinuationToken != "" {
		q.After, err = codec.Open(scope, req.ContinuationToken)
		if err != nil {
			return nil, opError(opListBuckets, err)
		}
	}

	cctx, stop := callContext(ctx, sess)
	defer stop()

	found, skipped, err := sess.Value.vfs.ListBuckets(cctx)
	if err != nil {
		return nil, opError(opListBuckets, failure(ctx, sess, err, !isRead))
	}

	for _, s := range skipped {
		p.report(s)
	}

	out, last := bucketPage(found, q)
	if last != "" {
		// A bucket name is far shorter than listing.MaxCursorLength, so
		// Seal fails only if that rule changes.
		tok, sErr := codec.Seal(scope, listing.Cursor{After: last})
		if sErr != nil {
			return nil, opError(opListBuckets, fmt.Errorf("%w: %w", storage.ErrBackend, sErr))
		}

		out.NextContinuationToken = tok
	}

	return out, nil
}

// bucketPage returns the page q selects from found, and the name of its
// last bucket when another page follows, else "".
func bucketPage(found []vfs.BucketEntry, q listing.Query) (*storage.ListBucketsOutput, string) {
	byName := make(map[string]storage.Bucket, len(found))
	names := make([]string, 0, len(found))

	for _, b := range found {
		if strings.HasPrefix(b.Name, q.Prefix) {
			byName[b.Name] = storage.Bucket{Name: b.Name, CreationDate: b.Created}
			names = append(names, b.Name)
		}
	}

	slices.Sort(names)

	entries, truncated := listing.Page(slices.Values(names), q)
	out := &storage.ListBucketsOutput{Buckets: make([]storage.Bucket, 0, len(entries))}

	for _, e := range entries {
		out.Buckets = append(out.Buckets, byName[e.Name])
	}

	if !truncated {
		return out, ""
	}

	return out, entries[len(entries)-1].Name
}
