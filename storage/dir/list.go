package dir

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/gasmod/gas/storage"
	"github.com/gasmod/gas/storage/dir/internal/vfs"
	"github.com/gasmod/gas/storage/internal"
)

// ListBuckets returns one page of the buckets under the root, in name order.
// CreationDate is when CreateBucket first ran for the bucket. Nil MaxBuckets
// is a page of 10000, as on S3. A ContinuationToken this backend did not
// issue returns an error wrapping storage.ErrInvalidContinuationToken.
//
// Each call reads every bucket's name record. A bucket whose record is
// missing (created by a version of this backend that kept none) or does not
// hold its name (such as one a crash left empty) cannot be named and is left
// out of the listing until CreateBucket of that bucket runs again, which
// rewrites it; its objects stay reachable through the other methods
// meanwhile. A record that cannot be read at all, or that is not a regular
// file, fails the call instead.
func (p *Provider) ListBuckets(
	ctx context.Context,
	input *storage.ListBucketsInput,
) (*storage.ListBucketsOutput, error) {
	if err := p.checkReady(); err != nil {
		return nil, err
	}

	if input == nil {
		return nil, internal.Errorf(mod, "list buckets: %w", storage.ErrInputRequired)
	}

	maxBuckets, inErr := internal.ValidateListBuckets(input)
	if inErr != nil {
		return nil, internal.Errorf(mod, "list buckets: %w", inErr)
	}

	after, tErr := decodeToken(input.ContinuationToken)
	if tErr != nil {
		return nil, internal.Errorf(mod, "list buckets: %w", tErr)
	}

	entries, err := p.vfs.ListBuckets(ctx)
	if err != nil {
		return nil, internal.Errorf(mod, "list buckets: %w", err)
	}

	prefix := valueOf(input.Prefix)
	created := make(map[string]vfs.BucketEntry, len(entries))
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if strings.HasPrefix(e.Name, prefix) {
			created[e.Name] = e
			names = append(names, e.Name)
		}
	}

	pg, truncated := page(names, after, maxBuckets)
	out := &storage.ListBucketsOutput{Buckets: make([]storage.Bucket, 0, len(pg))}
	for _, name := range pg {
		out.Buckets = append(out.Buckets, storage.Bucket{
			Name:         name,
			CreationDate: created[name].Created,
		})
	}
	if truncated {
		out.NextContinuationToken = encodeToken(pg[len(pg)-1])
	}
	return out, nil
}

// ListObjects returns one page of the bucket's objects, in key order, with
// the semantics of S3 ListObjectsV2: Prefix filters keys, StartAfter skips
// keys up to and including it, Delimiter rolls keys up into common prefixes,
// and objects and common prefixes together fill at most MaxKeys entries. A
// missing bucket returns an error wrapping storage.ErrBucketNotFound, and a
// ContinuationToken this backend did not issue one wrapping
// storage.ErrInvalidContinuationToken.
//
// Each call reads the metadata of every object in the bucket, so a page
// costs time in proportion to the bucket, not the page. An object deleted
// while the page is built is left out. Metadata that cannot be used fails
// the call.
func (p *Provider) ListObjects(
	ctx context.Context,
	input *storage.ListObjectsInput,
) (*storage.ListObjectsOutput, error) {
	if err := p.checkReady(); err != nil {
		return nil, err
	}

	if input == nil {
		return nil, internal.Errorf(mod, "list: %w", storage.ErrInputRequired)
	}

	bucket, maxKeys, inErr := internal.ValidateListObjects(input, p.config.Storage.Bucket)
	if inErr != nil {
		return nil, internal.Errorf(mod, "list: %w", inErr)
	}

	after, tErr := decodeToken(input.ContinuationToken)
	if tErr != nil {
		return nil, internal.Errorf(mod, "list: %w", tErr)
	}

	bk, bkErr := p.vfs.OpenBucket(bucket)
	if bkErr != nil {
		return nil, opError("list", nil, bkErr)
	}

	out, err := p.listObjects(ctx, bk, input, after, maxKeys)
	if err = errors.Join(p.removedErr(bk, err), bk.Close()); err != nil {
		return nil, opError("list", nil, err)
	}
	return out, nil
}

// listObjects builds the page of bk that input selects, starting after the
// entry after (a key or a common prefix) and holding at most maxKeys
// entries.
func (p *Provider) listObjects(
	ctx context.Context,
	bk *vfs.Bucket,
	input *storage.ListObjectsInput,
	after string,
	maxKeys int,
) (*storage.ListObjectsOutput, error) {
	keys, err := bk.Keys(ctx)
	if err != nil {
		return nil, fmt.Errorf("read keys: %w", err)
	}

	names, common := rollUp(keys, input)
	pg, truncated := page(names, after, maxKeys)
	out := &storage.ListObjectsOutput{
		IsTruncated:    truncated,
		Objects:        make([]storage.ObjectSummary, 0, len(pg)),
		CommonPrefixes: make([]string, 0),
	}
	for _, name := range pg {
		if common[name] {
			out.CommonPrefixes = append(out.CommonPrefixes, name)
			continue
		}
		sum, ok, sErr := summary(ctx, bk, name)
		if sErr != nil {
			return nil, sErr
		}
		if ok {
			out.Objects = append(out.Objects, sum)
		}
	}
	if truncated {
		out.NextContinuationToken = encodeToken(pg[len(pg)-1])
	}
	return out, nil
}

// rollUp returns the names input lists from keys, unsorted and possibly
// repeated: each key under Prefix and after StartAfter, or the common
// prefix Delimiter rolls it up into. common holds the names that are common
// prefixes. A common prefix ends in a delimiter that follows Prefix, so it
// never equals a key that is listed as itself.
func rollUp(keys []string, input *storage.ListObjectsInput) ([]string, map[string]bool) {
	prefix := valueOf(input.Prefix)
	delim := valueOf(input.Delimiter)
	startAfter := valueOf(input.StartAfter)

	common := map[string]bool{}
	names := make([]string, 0, len(keys))
	for _, k := range keys {
		if !strings.HasPrefix(k, prefix) || k <= startAfter {
			continue
		}
		if delim != "" {
			if i := strings.Index(k[len(prefix):], delim); i >= 0 {
				k = k[:len(prefix)+i+len(delim)]
				common[k] = true
			}
		}
		names = append(names, k)
	}
	return names, common
}

// summary returns the summary of the object key in bk, or false when it was
// deleted since its key was read.
func summary(ctx context.Context, bk *vfs.Bucket, key string) (storage.ObjectSummary, bool, error) {
	obj, err := bk.OpenObject(ctx, &vfs.OpenObjectInput{Key: key})
	if errors.Is(err, vfs.ErrObjectNotFound) {
		return storage.ObjectSummary{}, false, nil
	}
	if err != nil {
		return storage.ObjectSummary{}, false, fmt.Errorf("open object: %w", err)
	}
	if cErr := obj.File.Close(); cErr != nil {
		return storage.ObjectSummary{}, false, fmt.Errorf("close data file: %w", cErr)
	}
	return storage.ObjectSummary{
		Key:          key,
		LastModified: obj.LastModified,
		ETag:         obj.ETag,
		Size:         obj.Size,
	}, true, nil
}

// page sorts names, drops duplicates, and returns at most n of those after
// the name after, in order, with whether more follow.
func page(names []string, after string, n int) ([]string, bool) {
	slices.Sort(names)
	names = slices.Compact(names)
	i, found := slices.BinarySearch(names, after)
	if found {
		i++
	}
	names = names[i:]
	if len(names) > n {
		return names[:n], true
	}
	return names, false
}

// encodeToken returns the continuation token that resumes a listing after
// the entry name.
func encodeToken(name string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(name))
}

// decodeToken returns the entry a continuation token resumes after: "" for a
// nil token, or a bare storage.ErrInvalidContinuationToken for one that
// encodeToken did not return.
func decodeToken(token *string) (string, error) {
	if token == nil {
		return "", nil
	}
	b, err := base64.RawURLEncoding.DecodeString(*token)
	if err != nil || len(b) == 0 {
		return "", storage.ErrInvalidContinuationToken
	}
	return string(b), nil
}

// valueOf returns the string s points to, or "" when s is nil.
func valueOf(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
