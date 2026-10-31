package vfs

import (
	"os"
	"strings"
	"time"
	"uuid"
)

// Object is an opened object: its metadata, the size and modification
// time of its data file, and the data file open for reading. The caller
// closes File.
//
//nolint:govet // fieldalignment: the embedded fields come first, as embeddedstructfieldcheck wants.
type Object struct {
	ObjectStat

	// File is the data file, open for reading.
	File *os.File
}

// ObjectStat is an object's metadata with the size and the modification
// time of its data file.
//
//nolint:govet // fieldalignment: the embedded fields come first, as embeddedstructfieldcheck wants.
type ObjectStat struct {
	Metadata

	// LastModified is the modification time of the data file, in UTC to
	// the second.
	LastModified time.Time
	// Size is the size of the data file.
	Size int64
}

// Headers are the header fields stored with an object.
type Headers struct {
	// CacheControl is the Cache-Control.
	CacheControl string `json:"cacheControl,omitempty"`
	// ContentDisposition is the Content-Disposition.
	ContentDisposition string `json:"contentDisposition,omitempty"`
	// ContentEncoding is the Content-Encoding.
	ContentEncoding string `json:"contentEncoding,omitempty"`
	// ContentLanguage is the Content-Language.
	ContentLanguage string `json:"contentLanguage,omitempty"`
	// ContentType is the Content-Type.
	ContentType string `json:"contentType,omitempty"`
	// Expires is the Expires.
	Expires string `json:"expires,omitempty"`
}

// Metadata is what an object's metadata file holds, as JSON.
type Metadata struct {
	Headers

	// User is the user metadata.
	User map[string]string `json:"metadata,omitempty"`
	// Key is the object's key, which hashes to the file's name.
	Key string `json:"key"`
	// ETag is the hex MD5 of the data file, computed as it was written.
	ETag string `json:"etag"`
	// DataFile names the object's data file. Every write makes a new one,
	// so the rename of the metadata file alone decides which contents
	// belong to the object.
	DataFile string `json:"dataFile"`
}

// newDataFileName returns a new data file name for the object whose key
// hashes to name: the hash, `_`, and a new UUID.
func newDataFileName(name string) string {
	return name + "_" + uuid.New().String()
}

// isDataFileName reports whether file is a name newDataFileName could
// have returned for name. Metadata is read back from disk, so the name it
// holds is checked before it is opened or removed: it can then name
// neither another object's data file nor a path outside the objects
// directory.
func isDataFileName(name, file string) bool {
	id, ok := strings.CutPrefix(file, name+"_")

	return ok && isUUID(id)
}

// isUUID reports whether s is a UUID as uuid.UUID.String writes one.
func isUUID(s string) bool {
	u, err := uuid.Parse(s)

	return err == nil && u.String() == s
}
