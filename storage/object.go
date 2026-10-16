package storage

import (
	"io"
	"time"
)

// ObjectInfo describes a stored object. HeadObject and GetObject fill every
// field the backend knows; PutObject and DeleteObject may fill only some and
// leave the rest at their zero values (the s3 backend reports only Version
// from them).
type ObjectInfo struct {
	// LastModified is when the object was last written.
	LastModified time.Time
	// Metadata holds the user metadata stored with the object.
	Metadata map[string]string
	// ContentType is the object's media type.
	ContentType string
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
	// underlying connection or file.
	Body io.ReadCloser
	ObjectInfo
}
