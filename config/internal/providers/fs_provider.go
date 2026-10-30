package providers

import (
	"io/fs"

	"github.com/gasmod/gas/config/internal/sysfs"
)

// FSProvider reads the file behind a file-based provider through an
// [fs.FS], as described in the package documentation. The zero value reads
// through [sysfs.SysFS].
type FSProvider struct {
	fsys fs.FS
}

// SetFS makes p read through fsys. A nil fsys restores the default,
// [sysfs.SysFS].
func (p *FSProvider) SetFS(fsys fs.FS) {
	p.fsys = fsys
}

// ReadFile reads the named file whole. On an error it returns nil data and
// the file system's error as it is.
func (p *FSProvider) ReadFile(name string) ([]byte, error) {
	fsys := p.fsys
	if fsys == nil {
		fsys = sysfs.SysFS{}
	}

	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		//nolint:wrapcheck // The provider wraps it with its sentinel and the file name.
		return nil, err
	}

	return data, nil
}
