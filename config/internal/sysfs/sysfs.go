// Package sysfs provides a file system implementation that ensures safe file operations.
package sysfs

import "io/fs"

// SysFS implements the fs.FS interface and provides safe file system operations.
type SysFS struct{}

var _ fs.FS = (*SysFS)(nil)

// NewSysFS creates and returns a new instance of SysFS.
func NewSysFS() *SysFS {
	return &SysFS{}
}

// Open safely opens the file at the given name using path validation.
// It implements the fs.FS interface Open method.
func (s SysFS) Open(name string) (fs.File, error) {
	return SafeOpen(name)
}
