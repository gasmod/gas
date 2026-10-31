package dir

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/gasmod/gas/storage"
	"github.com/gasmod/gas/storage/dir/internal/vfs"
	"github.com/gasmod/gas/storage/internal/listing"
)

// objectScope is the first part of the scope of a ListObjects token, so
// that it never opens as a ListBuckets one.
const objectScope = "objects"

// ListObjects returns one page of the objects of a bucket, as described
// in the package documentation. It walks the key index from where the
// page starts and reads the metadata of the objects of the page only. An
// object that cannot be read is left out and passed to the skip handler.
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

	cctx, stop := callContext(ctx, sess)
	defer stop()

	var out *page

	err = withBucket(sess.Value, req.Bucket, func(bk *vfs.Bucket) error {
		var pErr error

		out, pErr = p.page(cctx, bk, q)

		return pErr
	})
	if err != nil {
		return nil, opError(opListObjects, failure(ctx, sess, err, !isRead))
	}

	if out.IsTruncated {
		// A key is shorter than listing.MaxCursorLength, so Seal fails
		// only if that rule changes.
		tok, sErr := codec.Seal(scope, out.last)
		if sErr != nil {
			return nil, opError(opListObjects, fmt.Errorf("%w: %w", storage.ErrBackend, sErr))
		}

		out.NextContinuationToken = tok
	}

	return &out.ListObjectsOutput, nil
}

// page is a page of ListObjects, and the entry it ends with.
//
//nolint:govet // fieldalignment: the embedded fields come first, as embeddedstructfieldcheck wants.
type page struct {
	storage.ListObjectsOutput

	// last is the last entry of the page.
	last listing.Cursor
}

// page returns the page q selects from bk.
func (p *Provider) page(ctx context.Context, bk *vfs.Bucket, q listing.Query) (*page, error) {
	var walkErr error

	keys := func(yield func(string) bool) {
		s := &passer{q: &q}
		bounds := vfs.Bounds{Prefix: q.Prefix, After: max(q.StartAfter, q.After.After)}

		walkErr = bk.ListKeys(ctx, bounds, s.pass, func(key string) bool {
			s.yielded(key)

			return yield(key)
		}, p.report)
	}

	entries, truncated := listing.Page(keys, q)

	if walkErr != nil {
		return nil, walkErr //nolint:wrapcheck // ListObjects classifies it.
	}

	out := &page{IsTruncated: truncated}

	for _, e := range entries {
		if e.Prefix {
			out.CommonPrefixes = append(out.CommonPrefixes, e.Name)

			continue
		}

		st, err := bk.Stat(ctx, e.Name)

		switch {
		case errors.Is(err, vfs.ErrObjectNotFound):
			// Deleted since its key was read.
			continue
		case errors.Is(err, vfs.ErrCorrupt):
			p.report(vfs.Skipped{Path: bk.MetaPath(e.Name), Err: err})

			continue
		case err != nil:
			return nil, err //nolint:wrapcheck // ListObjects classifies it.
		}

		out.Objects = append(out.Objects, storage.ObjectSummary{
			LastModified: st.LastModified,
			Key:          e.Name,
			ETag:         st.ETag,
			Size:         st.Size,
		})
	}

	if truncated {
		last := entries[len(entries)-1]

		out.last = listing.Cursor{After: last.Name, Prefix: last.Prefix}
	}

	return out, nil
}

// passer tells the keys a page of q passes over without checking that
// they hold an object: those after a common prefix the previous page
// ended with that roll up into it, and those that roll up into the common
// prefix of the last key yielded, which the page lists once. The keys of a
// walk come in byte order, so the keys of one common prefix come in a row.
type passer struct {
	// q is the page.
	q *listing.Query
	// last is the common prefix of the last key yielded, or "" when it
	// rolls up into none.
	last string
}

// pass reports whether the page passes over key.
func (s *passer) pass(key string) bool {
	return (s.q.After.Prefix && strings.HasPrefix(key, s.q.After.After)) ||
		(s.last != "" && strings.HasPrefix(key, s.last))
}

// yielded records key as yielded.
func (s *passer) yielded(key string) {
	s.last = ""

	if s.q.Delimiter == "" {
		return
	}

	i := strings.Index(key[len(s.q.Prefix):], s.q.Delimiter)
	if i >= 0 {
		s.last = key[:len(s.q.Prefix)+i+len(s.q.Delimiter)]
	}
}
