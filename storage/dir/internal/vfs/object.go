package vfs

import (
	"io/fs"
	"strings"
	"time"
	"uuid"
)

// Object is an opened object: its metadata, the size and modification time of
// its data file, and the data file open for reading. The caller closes File.
type Object struct {
	LastModified time.Time
	File         fs.File
	ObjectMetadata
	Size int64
}

// ObjectMetadata is the content of an object's metadata file, stored as JSON.
type ObjectMetadata struct {
	Key         string
	ContentType string
	Metadata    map[string]string
	// DataFile names the object's data file. Every Put writes a new one, so
	// the metadata rename alone decides which body belongs to the object.
	DataFile string
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
