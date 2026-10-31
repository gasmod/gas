package storage

import (
	"io"
	"time"
)

// ObjectHeaders are the header fields and the user metadata a write
// stores with an object, as described in the package documentation. A
// nil field is not stored, but for ContentType, which is then
// DefaultContentType.
type ObjectHeaders struct {
	// Metadata is the user metadata, by name. Names are stored in lower
	// case.
	Metadata map[string]string
	// CacheControl is stored as the object's Cache-Control.
	CacheControl *string
	// ContentDisposition is stored as the object's Content-Disposition.
	ContentDisposition *string
	// ContentEncoding is stored as the object's Content-Encoding. The body
	// is stored as given, neither decoded nor encoded.
	ContentEncoding *string
	// ContentLanguage is stored as the object's Content-Language.
	ContentLanguage *string
	// ContentType is stored as the object's Content-Type.
	ContentType *string
	// Expires is stored as the object's Expires, an IMF-fixdate such as
	// `Wed, 21 Oct 2015 07:28:00 GMT`.
	Expires *string
}

// HeaderValues are the header fields stored with an object, each empty
// when it is not stored.
type HeaderValues struct {
	// CacheControl is the object's Cache-Control.
	CacheControl string
	// ContentDisposition is the object's Content-Disposition.
	ContentDisposition string
	// ContentEncoding is the object's Content-Encoding.
	ContentEncoding string
	// ContentLanguage is the object's Content-Language.
	ContentLanguage string
	// ContentType is the object's Content-Type.
	ContentType string
	// Expires is the object's Expires, an IMF-fixdate.
	Expires string
}

// ObjectInfo describes a stored object, as HeadObject and GetObject
// return it.
type ObjectInfo struct {
	HeaderValues

	// LastModified is when the object was written, in UTC to the second.
	LastModified time.Time
	// Metadata is the user metadata, with names in lower case. It is
	// empty when the object has none. It is the caller's own copy.
	Metadata map[string]string
	// ETag identifies the object's contents, without quotes. Compare it
	// only for equality: on some backends it is not a digest of the
	// contents.
	ETag string
	// Version identifies the version of the object, or is empty on a
	// backend that does not keep versions.
	Version string
	// Size is the length of the bytes selected: the object's length, or
	// that of its Range.
	Size int64
}

// Object is an object GetObject returns: its description and a reader of
// the bytes selected.
//
//nolint:govet // fieldalignment: the embedded fields come first, as embeddedstructfieldcheck wants.
type Object struct {
	ObjectInfo

	// Body yields the bytes selected. The caller closes it, which frees the
	// file or the connection behind it.
	Body io.ReadCloser
}

// ObjectSummary describes an object in a page of ListObjects.
type ObjectSummary struct {
	// LastModified is when the object was written, in UTC to the second.
	LastModified time.Time
	// Key is the key of the object.
	Key string
	// ETag identifies the object's contents, as ObjectInfo.ETag.
	ETag string
	// Size is the length of the object, in bytes.
	Size int64
}
