package storage

import (
	"io"
	"time"
)

// ObjectInfo describes a stored object. HeadObject and GetObject fill every
// field the backend knows; PutObject and DeleteObject may fill only some and
// leave the rest at their zero values (see each backend's documentation).
// PutObject fills ETag on every backend.
type ObjectInfo struct {
	// LastModified is when the object was last written.
	LastModified time.Time
	// Metadata holds the user metadata stored with the object.
	Metadata map[string]string
	// CacheControl is the object's stored Cache-Control header.
	CacheControl string
	// ContentDisposition is the object's stored Content-Disposition header.
	ContentDisposition string
	// ContentEncoding is the object's stored Content-Encoding header.
	ContentEncoding string
	// ContentLanguage is the object's stored Content-Language header.
	ContentLanguage string
	// ContentType is the object's media type.
	ContentType string
	// Expires is the object's stored Expires header, as text.
	Expires string
	// ETag identifies the object's contents, unquoted. Compare it only for
	// equality, never as a digest: the dir backend gives the hex MD5 of the
	// body, but on S3 an object uploaded in parts has an ETag ending in "-"
	// and the part count, and, according to the AWS SDK's documentation of
	// types.Object, an object encrypted with SSE-KMS has one that is not an
	// MD5 at all. It is empty for an object stored by a backend version that
	// recorded none.
	ETag string
	// Version identifies this version of the object, or is empty when the
	// backend does not version objects.
	Version string
	// Size is the object's length in bytes.
	Size int64
}

// Object is an object returned by GetObject: its description and a reader
// for its contents. The caller must close Body.
type Object struct {
	// Body streams the object's contents. Closing it releases the
	// underlying connection or file. A backend may return a Body that does
	// more, such as seek; only its own documentation promises that.
	Body io.ReadCloser
	ObjectInfo
}

// ObjectSummary describes an object in a ListObjects page.
type ObjectSummary struct {
	// LastModified is when the object was last written.
	LastModified time.Time
	// Key is the object key.
	Key string
	// ETag identifies the object's contents, as ObjectInfo.ETag.
	ETag string
	// Size is the object's length in bytes.
	Size int64
}
