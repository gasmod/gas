// Package providers implements a base FS-based configuration provider.
package providers

import (
	"fmt"
	"io/fs"

	"github.com/gasmod/gas/config/internal/sysfs"
)

// FSProvider provides file system operations by wrapping an fs.FS implementation.
// It is used as a base provider for other file-based configuration providers.
type FSProvider struct {
	fileFS fs.FS
}

// NewFSProvider creates a new FSProvider with the given fs.FS implementation.
// If fileFS is nil, it defaults to using sysfs.NewSysFS().
func NewFSProvider(fileFS fs.FS) *FSProvider {
	fsOrDefault := fileFS
	if fileFS == nil {
		fsOrDefault = sysfs.NewSysFS()
	}

	return &FSProvider{
		fileFS: fsOrDefault,
	}
}

// SetFS sets the underlying fs.FS implementation.
func (p *FSProvider) SetFS(fileFS fs.FS) {
	p.fileFS = fileFS
}

// OpenFile opens the named file using the underlying fs.FS implementation.
func (p *FSProvider) OpenFile(name string) (fs.File, error) {
	f, err := p.fileFS.Open(name)
	if err != nil {
		return nil, fmt.Errorf("open file %s: %w", name, err)
	}

	return f, nil
}

// ReadFile reads the named file using the underlying fs.FS implementation.
func (p *FSProvider) ReadFile(name string) ([]byte, error) {
	data, err := fs.ReadFile(p.fileFS, name)
	if err != nil {
		return nil, fmt.Errorf("read file %s: %w", name, err)
	}

	return data, nil
}
