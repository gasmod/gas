package vfs

import (
	"io/fs"
	"strings"
	"time"
	"uuid"
)

// Object is an opened object: its metadata and data file stat, and the data
// file open for reading. The caller closes File.
type Object struct {
	File fs.File
	ObjectStat
}

// ObjectStat is an object's metadata with the size and modification time of
// its data file.
type ObjectStat struct {
	LastModified time.Time
	ObjectMetadata
	Size int64
}

// ObjectMetadata is the content of an object's metadata file, stored as JSON.
// A file written before ETag or Headers existed decodes with them empty.
type ObjectMetadata struct {
	Metadata map[string]string
	Headers
	Key         string
	ContentType string
	// ETag is the hex MD5 of the data file, computed as it was written.
	ETag string
	// DataFile names the object's data file. Every Put writes a new one, so
	// the metadata rename alone decides which body belongs to the object.
	DataFile string
}

// Headers are the system headers stored with an object, as given. Their
// JSON fields sit beside the other metadata fields.
type Headers struct {
	CacheControl       string
	ContentDisposition string
	ContentEncoding    string
	ContentLanguage    string
	Expires            string
}

// newDataFileName returns a fresh data file name for the object whose key
// hashes to name: the hash, "_", and a new UUID.
func newDataFileName(name string) string {
	return name + "_" + uuid.New().String()
}

// isDataFileName reports whether file is a data file name newDataFileName
// could have returned for name. Metadata is read back from disk, so the name
// it holds is checked before it is opened or removed: it can then name
// neither another object's data file nor a path outside the objects dir.
func isDataFileName(name, file string) bool {
	id, ok := strings.CutPrefix(file, name+"_")
	if !ok {
		return false
	}
	u, err := uuid.Parse(id)
	return err == nil && u.String() == id
}
