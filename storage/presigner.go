package storage

import (
	"context"
	"net/http"
	"time"
)

// Presigner issues presigned URLs: URLs that let a client holding no
// credentials upload or download one object, directly with the backend,
// until they expire, as described in the package documentation.
type Presigner interface {
	// PresignUploadURL returns a request that uploads the object in.
	PresignUploadURL(ctx context.Context, in *PresignUploadURLInput) (*PresignURLResponse, error)
	// PresignDownloadURL returns a request that downloads the object in.
	PresignDownloadURL(
		ctx context.Context,
		in *PresignDownloadURLInput,
	) (*PresignURLResponse, error)
}

// PresignUploadURLInput describes the upload a presigned URL allows.
//
//nolint:govet // fieldalignment: the embedded fields come first, as embeddedstructfieldcheck wants.
type PresignUploadURLInput struct {
	BaseObjectInput
	ObjectHeaders
	WriteCondition

	// ContentLength is the length of the body the client must send, 0 to
	// MaxPresignUploadSize. It is required.
	ContentLength *int64
	// TTL is how long the URL is valid, MinPresignTTL to MaxPresignTTL.
	// Nil or 0 is DefaultPresignTTL.
	TTL *time.Duration
}

// PresignDownloadURLInput describes the download a presigned URL allows.
type PresignDownloadURLInput struct {
	GetObjectInput
	// ResponseHeaders are the header fields the download's response
	// carries instead of the object's.
	ResponseHeaders

	// TTL is how long the URL is valid, as in PresignUploadURLInput.
	TTL *time.Duration
}

// ResponseHeaders override the header fields of a presigned download's
// response, such as a ContentDisposition that names the file a browser
// saves. A nil field keeps the object's. They follow the rules of
// ObjectHeaders.
type ResponseHeaders struct {
	// ResponseCacheControl is the response's Cache-Control.
	ResponseCacheControl *string
	// ResponseContentDisposition is the response's Content-Disposition.
	ResponseContentDisposition *string
	// ResponseContentEncoding is the response's Content-Encoding.
	ResponseContentEncoding *string
	// ResponseContentLanguage is the response's Content-Language.
	ResponseContentLanguage *string
	// ResponseContentType is the response's Content-Type.
	ResponseContentType *string
	// ResponseExpires is the response's Expires, an IMF-fixdate.
	ResponseExpires *string
}

// PresignURLResponse is a presigned request: the method, the URL and the
// header fields the client must send with it, unchanged.
type PresignURLResponse struct {
	// SignedHeader holds the header fields the signature covers, which the
	// client sends as they are.
	SignedHeader http.Header
	// Expires is when the URL stops being valid, at the latest.
	Expires time.Time
	// URL is the presigned URL. It is a credential until it expires.
	URL string
	// Method is the HTTP method the URL is signed for.
	Method string
}
