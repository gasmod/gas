package s3

import (
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/gasmod/gas/storage"
)

// S3 error codes matched when the SDK does not return the modeled error type,
// for example when a HEAD response has no body or an S3-compatible backend
// returns a generic API error. NoSuchVersion has no modeled type in the SDK,
// so it is matched by code only.
const (
	errCodeNoSuchBucket            = "NoSuchBucket"
	errCodeNoSuchKey               = "NoSuchKey"
	errCodeNoSuchVersion           = "NoSuchVersion"
	errCodeNotFound                = "NotFound"
	errCodeBucketAlreadyOwnedByYou = "BucketAlreadyOwnedByYou"
	errCodeBucketAlreadyExists     = "BucketAlreadyExists"

	// A GET of an unsatisfiable Range answers 416 InvalidRange. A HEAD 416
	// has no body, and the SDK names it after the status text instead.
	errCodeInvalidRange        = "InvalidRange"
	errCodeRangeNotSatisfiable = "RequestedRangeNotSatisfiable"
)

// wrapS3Error maps an S3 error to the storage sentinel it stands for and
// returns an error that matches both the sentinel and the original error, so
// callers can use errors.Is with the sentinel and errors.As with the SDK
// types. The message reads "<sentinel>: <original>". An error with no
// matching sentinel, and nil, are returned unchanged.
func wrapS3Error(err error) error {
	var sentinel error
	switch {
	case isS3ErrorType[*types.NoSuchBucket](err, errCodeNoSuchBucket):
		sentinel = storage.ErrBucketNotFound
	case isS3ErrorType[*types.NoSuchKey](err, errCodeNoSuchKey):
		sentinel = storage.ErrKeyNotFound
	case isS3ErrorCode(err, errCodeNoSuchVersion):
		sentinel = storage.ErrVersionNotFound
	case isS3ErrorType[*types.NotFound](err, errCodeNotFound):
		sentinel = storage.ErrNotFound
	case isS3ErrorType[*types.BucketAlreadyExists](err, errCodeBucketAlreadyExists):
		sentinel = storage.ErrBucketExists
	case isS3ErrorCode(err, errCodeInvalidRange), isS3ErrorCode(err, errCodeRangeNotSatisfiable):
		sentinel = storage.ErrInvalidRange
	default:
		return err
	}
	return fmt.Errorf("%w: %w", sentinel, err)
}

// isBucketOwnedByCaller reports whether err is S3's BucketAlreadyOwnedByYou,
// which CreateBucket treats as success.
func isBucketOwnedByCaller(err error) bool {
	return isS3ErrorType[*types.BucketAlreadyOwnedByYou](err, errCodeBucketAlreadyOwnedByYou)
}

// Regions in which CreateBucket must not send a location constraint: S3
// rejects one naming us-east-1, and aws-global, which the SDK maps to the
// global us-east-1 endpoint, is not a valid constraint at all.
const (
	usEast1   = "us-east-1"
	awsGlobal = "aws-global"
)

// createBucketConfiguration returns the CreateBucketConfiguration for a
// bucket created by a client in region: none for us-east-1, aws-global, or an
// empty region, otherwise a LocationConstraint naming region, without which
// S3 rejects the request with IllegalLocationConstraintException.
func createBucketConfiguration(region string) *types.CreateBucketConfiguration {
	if region == "" || region == usEast1 || region == awsGlobal {
		return nil
	}
	return &types.CreateBucketConfiguration{
		LocationConstraint: types.BucketLocationConstraint(region),
	}
}

// isS3ErrorType reports whether err has an error of type T in its chain, or
// an smithy.APIError whose code is exactly code.
func isS3ErrorType[T error](err error, code string) bool {
	if _, ok := errors.AsType[T](err); ok {
		return true
	}
	return isS3ErrorCode(err, code)
}

// isS3ErrorCode reports whether err has an smithy.APIError in its chain whose
// code is exactly code.
func isS3ErrorCode(err error, code string) bool {
	apiErr, ok := errors.AsType[smithy.APIError](err)
	return ok && apiErr.ErrorCode() == code
}

// valueOf returns the value v points to, or the zero value of T when v is
// nil. It reads optional fields of SDK responses.
func valueOf[T any](v *T) T {
	if v == nil {
		var zero T
		return zero
	}
	return *v
}
