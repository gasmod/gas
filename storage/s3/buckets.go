package s3

import (
	"context"
	"errors"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/gasmod/gas/storage"
	"github.com/gasmod/gas/storage/internal/listing"
)

// bucketScope is the first part of the scope of a ListBuckets token, so
// that it never opens as a ListObjects one.
const bucketScope = "buckets"

// The regions in which CreateBucket sends no location constraint: S3
// refuses one naming us-east-1, and aws-global, which the AWS SDK maps to
// us-east-1, is no region at all.
const (
	usEast1   = "us-east-1"
	awsGlobal = "aws-global"
)

// CreateBucket creates the bucket in.Bucket, in the store's region, as
// described in the package documentation. Creating one the caller owns
// already is not an error; a name another account owns is an error
// wrapping storage.ErrBucketExists.
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

	st := sess.Value

	return run(ctx, sess, opCreateBucket, func(ctx context.Context) error {
		_, err := st.api.CreateBucket(ctx, &s3.CreateBucketInput{
			Bucket:                    new(in.Bucket),
			CreateBucketConfiguration: bucketConfiguration(st.region),
		})

		apiErr, ok := errors.AsType[smithy.APIError](err)
		if ok && bareCode(apiErr.ErrorCode()) == "BucketAlreadyOwnedByYou" {
			return nil
		}

		return err //nolint:wrapcheck // run classifies it.
	})
}

// bucketConfiguration returns the configuration of a bucket created in
// region: none for us-east-1, aws-global or no region, else a
// LocationConstraint naming region, without which S3 refuses the
// request.
func bucketConfiguration(region string) *types.CreateBucketConfiguration {
	if region == "" || region == usEast1 || region == awsGlobal {
		return nil
	}

	return &types.CreateBucketConfiguration{
		LocationConstraint: types.BucketLocationConstraint(region),
	}
}

// HeadBucket returns the bucket in.Bucket with its name and the region S3
// reports, or an error wrapping storage.ErrBucketNotFound. A bucket of
// another account, or one the credentials may not read, is an error
// wrapping storage.ErrAccessDenied.
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

	out, err := call(ctx, sess, opHeadBucket,
		func(ctx context.Context) (*s3.HeadBucketOutput, error) {
			return sess.Value.api.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: new(in.Bucket)})
		})
	if err != nil {
		return nil, err
	}

	return &storage.Bucket{Name: in.Bucket, Region: aws.ToString(out.BucketRegion)}, nil
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

	return run(ctx, sess, opDeleteBucket, func(ctx context.Context) error {
		_, err := sess.Value.api.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: new(in.Bucket)})

		return err //nolint:wrapcheck // run classifies it.
	})
}

// ListBuckets returns one page of the buckets the credentials own, as
// described in the package documentation. S3's continuation token is
// sealed in the token the page returns.
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

	st := sess.Value
	scope := listing.Scope(bucketScope, req.Prefix)
	params := &s3.ListBucketsInput{
		//nolint:gosec // G115: CheckListBuckets bounds it to MaxListBuckets.
		MaxBuckets: new(int32(req.MaxBuckets)),
		Prefix:     nonEmpty(req.Prefix),
	}

	params.ContinuationToken, err = openToken(st.codec, scope, req.ContinuationToken)
	if err != nil {
		return nil, opError(opListBuckets, err)
	}

	res, err := call(ctx, sess, opListBuckets,
		func(ctx context.Context) (*s3.ListBucketsOutput, error) {
			return st.api.ListBuckets(ctx, params)
		})
	if err != nil {
		return nil, err
	}

	out := &storage.ListBucketsOutput{Buckets: make([]storage.Bucket, 0, len(res.Buckets))}

	for _, b := range res.Buckets {
		out.Buckets = append(out.Buckets, storage.Bucket{
			CreationDate: second(aws.ToTime(b.CreationDate)),
			Name:         aws.ToString(b.Name),
			Region:       aws.ToString(b.BucketRegion),
		})
	}

	if next := aws.ToString(res.ContinuationToken); next != "" {
		out.NextContinuationToken, err = sealToken(st.codec, scope, next)
		if err != nil {
			return nil, requestError(opListBuckets, err)
		}
	}

	return out, nil
}
