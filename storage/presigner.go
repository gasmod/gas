package storage

import (
	"context"
	"net/http"
	"time"
)

// Presigner issues presigned URLs: time-limited URLs that let a client that
// holds no credentials upload or download one object directly against the
// backend. Implementations validate the input like Provider does and return
// the same validation sentinels, and reject a TTL out of bounds with
// ErrInvalidTTL. The s3 implementation signs locally without contacting S3,
// so it never reports a missing key or bucket.
type Presigner interface {
	// PresignUploadURL returns a URL that uploads the object described by
	// input. The client must send the returned method and signed headers.
	PresignUploadURL(
		ctx context.Context,
		input *PresignUploadURLInput,
	) (*PresignURLResponse, error)

	// PresignDownloadURL returns a URL that downloads the object described
	// by input.
	PresignDownloadURL(
		ctx context.Context,
		input *PresignDownloadURLInput,
	) (*PresignURLResponse, error)
}

// PresignUploadURLInput describes the upload a presigned URL allows. The
// embedded PutObjectInput's Body is ignored; the client sends the body.
type PresignUploadURLInput struct {
	// TTL is how long the URL stays valid. Nil or zero selects the backend's
	// default (15 minutes on s3). A TTL that is negative, under one second,
	// or over seven days fails with ErrInvalidTTL and nothing is signed. The
	// s3 backend signs whole seconds, truncating any fraction: 1.9s signs as
	// one second.
	TTL *time.Duration
	PutObjectInput
}

// PresignDownloadURLInput describes the download a presigned URL allows.
type PresignDownloadURLInput struct {
	// TTL is how long the URL stays valid, with the same default, bounds,
	// and rounding as PresignUploadURLInput.TTL.
	TTL *time.Duration
	GetObjectInput
}

// PresignURLResponse is a presigned request: the URL, the HTTP method to use
// with it, and the headers that were signed and must be sent unchanged.
type PresignURLResponse struct {
	// SignedHeader holds the headers included in the signature.
	SignedHeader http.Header
	// URL is the presigned URL.
	URL string
	// Method is the HTTP method the URL was signed for.
	Method string
}
