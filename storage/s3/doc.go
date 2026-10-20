// Package s3 implements storage.Provider and storage.Presigner on AWS S3 and
// S3-compatible services such as MinIO and LocalStack, using the AWS SDK for
// Go v2.
//
// # Wiring
//
// NewProvider and NewPresigner return constructors for the DI container:
//
//	gas.WithSingletonService[storage.Provider](s3.NewProvider()),
//	gas.WithSingletonService[storage.Presigner](s3.NewPresigner()),
//
// Each constructor has the shape func(gas.ConfigProvider) (*T, error): the
// container passes it the registered gas.ConfigProvider, so one must be
// registered, and the constructor binds Config from it. The keys live under
// "storage":
//
//   - bucket: the bucket used when an input leaves Bucket nil. Optional; when
//     set it must be a valid S3 bucket name or the constructor fails with
//     storage.ErrInvalidBucket.
//   - region: the AWS region. Empty uses the AWS SDK's default resolution
//     (environment and shared config).
//   - accessKeyID, secretAccessKey, sessionToken: static credentials, used
//     when accessKeyID is set. Otherwise the SDK's default credential chain
//     applies.
//   - endpoint: a custom endpoint, for S3-compatible services. Setting it
//     also switches to path-style addressing.
//
// Options replace the clients built from configuration: WithClient and
// WithUploaderClient for the Provider, WithPresignerS3Client and
// WithPresignClient for the Presigner. Configuration is still bound and the
// configured bucket still applies when a client is injected. An option given
// a nil client is ignored, and the constructor builds that client as if the
// option were absent. A nil option itself makes the constructor return an
// error wrapping storage.ErrInvalidOption that names its zero-based position,
// for example "gas/storage/s3/provider: storage: invalid option: option 1 is
// nil".
//
// # Presigned URL expiry
//
// A nil or zero TTL signs a 15-minute expiry. The Presigner passes it to the
// SDK explicitly, so it also overrides a default expiry configured on a
// presign client given with WithPresignClient. A TTL that is negative, under
// one second, or over seven days (the range AWS SigV4 query-string
// authentication accepts) fails with storage.ErrInvalidTTL and nothing is
// signed. The SDK writes X-Amz-Expires as whole seconds, truncating toward
// zero, so 1.9s signs as 1 and 7 days minus 1ns as 604799.
//
// # Objects and buckets
//
// PutObject sends every header of ObjectHeaders and returns the ETag and the
// version S3 reports; its other ObjectInfo fields are empty. The SDK takes
// Expires only as a time and writes it as an IMF-fixdate ("Wed, 21 Oct 2026
// 07:28:00 GMT"), so PutObject and PresignUploadURL accept only text already
// in exactly that form, which is stored unchanged. Anything else, including
// the obsolete RFC 850 and asctime forms that HTTP allows and a date whose
// weekday is wrong, is storage.ErrInvalidHeader, and nothing is sent or
// signed. HeadObject and GetObject return Expires as the text S3
// sent, unparsed. ETags are returned without S3's quotes.
//
// CreateBucket returns the bucket's Name and the ARN S3 reports as ID. S3's
// Location response header is a path, not a region, so it is not reported,
// and CreationDate and Region are empty. ListBuckets returns the Name and
// CreationDate, and the ARN and region when S3 sends them.
//
// # Lifecycle
//
// Neither type has a start or close step and neither starts a goroutine that
// outlives a call. Constructors send no request to S3. PutObject uploads
// through the SDK's transfer manager, which accepts any reader, including one that
// cannot seek or has unknown length, and switches to a multipart upload for
// large bodies; the part uploads it runs concurrently end before PutObject
// returns, and a failed multipart upload is aborted. The transfer manager
// itself sends whatever the body yields, so PutObject enforces a set
// ContentLength: a body that turns out shorter or longer fails the upload
// (single part before anything is sent; multipart by aborting it) with
// storage.ErrContentLengthMismatch. If that abort fails, the incomplete
// multipart upload remains, stored and billed until a lifecycle rule removes
// it, and the abort's error is not reported. With a missing bucket and a
// mismatched body, a body below the multipart threshold reports the mismatch
// (nothing is sent); above it, S3 may answer NoSuchBucket first. A Provider
// or Presigner
// that was not built by its constructor, including a nil pointer, returns
// storage.ErrNotInitialized from every method. Both are safe for concurrent
// use.
//
// # Errors
//
// Input is validated before any request is sent: a nil input is
// storage.ErrInputRequired, a bad key storage.ErrInvalidKey, a bad or
// missing bucket storage.ErrInvalidBucket, and a presign TTL out of bounds
// storage.ErrInvalidTTL. Every error has the form
//
//	gas/storage/s3/provider: <op>: <cause>
//	gas/storage/s3/presigner: <op>: <cause>
//
// where <op> is one of "create bucket", "list buckets", "head", "put", "get",
// "delete", "list", "presign upload", or "presign download". A list page
// size out of range is storage.ErrInvalidLimit and an empty continuation
// token storage.ErrInvalidContinuationToken, before any request is sent; a
// token S3 did not issue may be rejected with an error that carries no
// sentinel. S3 errors are mapped to storage
// sentinels, and the SDK error stays in the chain for errors.As:
//
//   - NoSuchBucket: storage.ErrBucketNotFound
//   - NoSuchKey: storage.ErrKeyNotFound
//   - NoSuchVersion (GetObject of a Version that does not exist):
//     storage.ErrVersionNotFound. The SDK has no modeled type for it, so it
//     is matched by error code. HeadObject cannot see it: S3 answers HEAD of
//     an unknown version with a bodiless 404, which is storage.ErrNotFound.
//     A malformed version ID may get a 400 from AWS that maps to no
//     sentinel (LocalStack answers NoSuchVersion), so do not rely on
//     storage.ErrVersionNotFound for one.
//   - NotFound (a HEAD 404, which S3 also returns for a missing bucket):
//     storage.ErrNotFound
//   - InvalidRange (a GET 416), and RequestedRangeNotSatisfiable (the SDK's
//     name for a bodiless HEAD 416): storage.ErrInvalidRange
//   - BucketAlreadyExists (the name belongs to another account):
//     storage.ErrBucketExists. BucketAlreadyOwnedByYou is not an error:
//     CreateBucket of a bucket the caller owns succeeds, and its Bucket may
//     hold only the Name (us-east-1 is said to answer 200 with the ARN;
//     other regions answer BucketAlreadyOwnedByYou, which carries none; not
//     verified against AWS).
//
// Any other S3 error, such as AccessDenied or a redirect to another region,
// carries no sentinel. The package adds
// no object key or metadata value to an error message, but the SDK's own
// error for a failed request (connection refused, for example) includes the
// request URL, which contains the bucket and the key.
//
// ListObjects uses ListObjectsV2 with encoding-type url and decodes the keys
// and common prefixes in the response, so keys holding characters XML 1.0
// cannot carry are listed intact. ListBuckets lists the buckets the
// credentials own.
//
// CreateBucket creates the bucket in the client's region. Outside us-east-1
// and aws-global (the SDK's pseudo-region for the global endpoint)
// the request names that region as its location constraint, which S3
// requires there; with no configured region, the region the SDK resolved is
// used.
//
// SPDX-License-Identifier: MIT
package s3
