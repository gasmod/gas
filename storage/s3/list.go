package s3

import (
	"context"
	"errors"
	"net/url"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/gasmod/gas/storage"
	"github.com/gasmod/gas/storage/internal"
)

// ListBuckets returns one page of the buckets the caller owns, in name order,
// as S3 ListBuckets reports them. MaxBuckets is sent to S3 as given and
// defaults to 10000 there too.
func (p *Provider) ListBuckets(
	ctx context.Context,
	input *storage.ListBucketsInput,
) (*storage.ListBucketsOutput, error) {
	if err := p.checkReady(); err != nil {
		return nil, err
	}

	if input == nil {
		return nil, internal.Errorf("s3/provider", "list buckets: %w", storage.ErrInputRequired)
	}

	if _, err := internal.ValidateListBuckets(input); err != nil {
		return nil, internal.Errorf("s3/provider", "list buckets: %w", err)
	}

	res, err := p.client.ListBuckets(ctx, &s3.ListBucketsInput{
		ContinuationToken: input.ContinuationToken,
		MaxBuckets:        input.MaxBuckets,
		Prefix:            input.Prefix,
	})
	if err != nil {
		return nil, internal.Errorf("s3/provider", "list buckets: %w", wrapS3Error(err))
	}

	out := &storage.ListBucketsOutput{
		NextContinuationToken: valueOf(res.ContinuationToken),
		Buckets:               make([]storage.Bucket, 0, len(res.Buckets)),
	}
	for _, b := range res.Buckets {
		out.Buckets = append(out.Buckets, storage.Bucket{
			Name:         valueOf(b.Name),
			CreationDate: valueOf(b.CreationDate),
			ID:           valueOf(b.BucketArn),
			Region:       valueOf(b.BucketRegion),
		})
	}
	return out, nil
}

// ListObjects returns one page of a bucket's objects through S3
// ListObjectsV2, in key order. It asks S3 to URL-encode the keys and
// prefixes in the response, so keys holding characters XML cannot carry are
// listed, and decodes them. A missing bucket is storage.ErrBucketNotFound.
// S3 may reject a ContinuationToken it did not issue with an error that
// carries no sentinel.
func (p *Provider) ListObjects(
	ctx context.Context,
	input *storage.ListObjectsInput,
) (*storage.ListObjectsOutput, error) {
	if err := p.checkReady(); err != nil {
		return nil, err
	}

	if input == nil {
		return nil, internal.Errorf("s3/provider", "list: %w", storage.ErrInputRequired)
	}

	bucket, _, inErr := internal.ValidateListObjects(input, p.config.Storage.Bucket)
	if inErr != nil {
		return nil, internal.Errorf("s3/provider", "list: %w", inErr)
	}

	res, err := p.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket:            new(bucket),
		ContinuationToken: input.ContinuationToken,
		Delimiter:         input.Delimiter,
		EncodingType:      types.EncodingTypeUrl,
		MaxKeys:           input.MaxKeys,
		Prefix:            input.Prefix,
		StartAfter:        input.StartAfter,
	})
	if err != nil {
		return nil, internal.Errorf("s3/provider", "list: %w", wrapS3Error(err))
	}

	out, decErr := listObjectsOutput(res)
	if decErr != nil {
		return nil, internal.Errorf("s3/provider", "list: %w", decErr)
	}
	return out, nil
}

// listObjectsOutput converts a ListObjectsV2 response requested with
// EncodingType url, decoding its keys and common prefixes.
func listObjectsOutput(res *s3.ListObjectsV2Output) (*storage.ListObjectsOutput, error) {
	out := &storage.ListObjectsOutput{
		NextContinuationToken: valueOf(res.NextContinuationToken),
		IsTruncated:           valueOf(res.IsTruncated),
		Objects:               make([]storage.ObjectSummary, 0, len(res.Contents)),
		CommonPrefixes:        make([]string, 0, len(res.CommonPrefixes)),
	}
	for _, o := range res.Contents {
		k, err := url.QueryUnescape(valueOf(o.Key))
		if err != nil {
			// The cause quotes the bad escape, which is part of the key.
			return nil, errors.New("decode key: invalid URL encoding")
		}
		out.Objects = append(out.Objects, storage.ObjectSummary{
			Key:          k,
			LastModified: valueOf(o.LastModified),
			ETag:         unquoteETag(o.ETag),
			Size:         valueOf(o.Size),
		})
	}
	for _, cp := range res.CommonPrefixes {
		pfx, err := url.QueryUnescape(valueOf(cp.Prefix))
		if err != nil {
			return nil, errors.New("decode common prefix: invalid URL encoding")
		}
		out.CommonPrefixes = append(out.CommonPrefixes, pfx)
	}
	return out, nil
}
