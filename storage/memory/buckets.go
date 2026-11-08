package memory

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/gasmod/gas/storage"
	"github.com/gasmod/gas/storage/internal/listing"
)

// bucket is a bucket and the objects it holds.
type bucket struct {
	// created is when the bucket was created, in UTC to the second.
	created time.Time
	// objects holds the objects by key.
	objects map[string]*object
}

// bucketScope is the first part of the scope of a ListBuckets token, so
// that it never opens as a ListObjects one.
const bucketScope = "buckets"

// now returns the time objects and buckets are stamped with: the current
// time in UTC, to the second.
func now() time.Time { return time.Now().UTC().Truncate(time.Second) }

// CreateBucket creates the bucket in.Bucket, as described in the package
// documentation. Creating one that exists is not an error; creating one
// past MaxBuckets is an error wrapping ErrStorageFull.
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

	err = p.createBucket(in.Bucket)
	if err != nil {
		return opError(opCreateBucket, err)
	}

	return nil
}

// createBucket creates the bucket name, unless it exists.
func (p *Provider) createBucket(name string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if _, ok := p.buckets[name]; ok {
		return nil
	}

	if len(p.buckets) >= MaxBuckets {
		return ErrStorageFull
	}

	p.buckets[name] = &bucket{created: now(), objects: make(map[string]*object)}

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

	err = p.bucketExists(in.Bucket)
	if err != nil {
		return nil, opError(opHeadBucket, err)
	}

	return &storage.Bucket{Name: in.Bucket}, nil
}

// DeleteBucket deletes the bucket in.Bucket when it holds no object, as
// described in the package documentation.
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

	p.mu.Lock()
	defer p.mu.Unlock()

	b, ok := p.buckets[in.Bucket]

	switch {
	case !ok:
		return opError(opDeleteBucket, storage.ErrBucketNotFound)
	case len(b.objects) > 0:
		return opError(opDeleteBucket, storage.ErrBucketNotEmpty)
	}

	delete(p.buckets, in.Bucket)

	return nil
}

// ListBuckets returns one page of the buckets, as described in the
// package documentation.
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

	p.mu.Lock()
	defer p.mu.Unlock()

	entries, truncated := listing.Page(slices.Values(slices.Sorted(maps.Keys(p.buckets))), q)
	out := &storage.ListBucketsOutput{Buckets: make([]storage.Bucket, 0, len(entries))}

	for _, e := range entries {
		out.Buckets = append(
			out.Buckets,
			storage.Bucket{Name: e.Name, CreationDate: p.buckets[e.Name].created},
		)
	}

	if truncated {
		// A bucket name is shorter than listing.MaxCursorLength, so Seal
		// fails only if that rule changes.
		token, sErr := codec.Seal(scope, listing.Cursor{After: entries[len(entries)-1].Name})
		if sErr != nil {
			return nil, opError(opListBuckets, fmt.Errorf("%w: %w", storage.ErrBackend, sErr))
		}

		out.NextContinuationToken = token
	}

	return out, nil
}
