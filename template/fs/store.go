package fs

import (
	"context"
	iofs "io/fs"
	"slices"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/template"
	"github.com/gasmod/gas/template/internal/files"
	"github.com/gasmod/gas/template/internal/value"
)

// Store reads templates from an fs.FS and cannot be written to, as
// described in the package documentation. It is safe for concurrent use
// when its fs.FS is.
type Store struct {
	// fsys is the file system the templates are read from.
	fsys iofs.FS
	// exts are the extensions a name must end in.
	exts []string
	// maxSize is the most bytes a template may hold.
	maxSize int64
}

var _ gas.TemplateProvider = (*Store)(nil)

// Option configures a Store.
type Option func(*Store)

// WithExtensions sets the extensions a template name must end in. The
// constructor checks them, as described in the template package
// documentation.
//
// Default: template.DefaultExtension.
func WithExtensions(exts ...string) Option {
	exts = slices.Clone(exts)

	return func(s *Store) { s.exts = exts }
}

// WithMaxSize sets the most bytes a template may hold. The constructor
// checks it with template.CheckMaxSize.
//
// Default: template.DefaultMaxSize.
func WithMaxSize(n int64) Option {
	return func(s *Store) { s.maxSize = n }
}

// NewStore returns the constructor of a Store over fsys with opts
// applied, in the shape the gas container expects. The constructor
// returns template.ErrNilFS for a nil fsys and an error wrapping
// template.ErrInvalidExtension or template.ErrInvalidMaxSize for a bad
// option.
func NewStore(fsys iofs.FS, opts ...Option) func() (*Store, error) {
	return func() (*Store, error) {
		if value.IsNil(fsys) {
			return nil, template.ErrNilFS
		}

		s := &Store{
			fsys:    fsys,
			exts:    []string{template.DefaultExtension},
			maxSize: template.DefaultMaxSize,
		}

		for _, opt := range opts {
			opt(s)
		}

		err := template.CheckExtensions(s.exts)
		if err != nil {
			return nil, err //nolint:wrapcheck // CheckExtensions returns complete errors.
		}

		err = template.CheckMaxSize(s.maxSize)
		if err != nil {
			return nil, err //nolint:wrapcheck // CheckMaxSize returns complete errors.
		}

		return s, nil
	}
}

// Get returns the content of the template name, read from the file
// system, as described in the package documentation. Its errors wrap
// template.ErrInvalidName, template.ErrTemplateNotFound, template.ErrRead
// or template.ErrTooLarge.
func (s *Store) Get(_ context.Context, name string) ([]byte, error) {
	err := template.CheckName(name, s.exts)
	if err != nil {
		return nil, err //nolint:wrapcheck // CheckName returns complete errors.
	}

	//nolint:wrapcheck // Read returns complete errors.
	return files.Read(s.fsys, name, s.maxSize)
}

// List returns the names of the templates of the file system in byte
// order, as described in the package documentation. A failure to walk it
// is an error wrapping template.ErrList.
func (s *Store) List(ctx context.Context) ([]string, error) {
	//nolint:wrapcheck // List returns complete errors.
	return files.List(ctx, s.fsys, s.exts)
}

// Register returns an error wrapping template.ErrInvalidName for a name
// the store does not accept, and template.ErrReadOnly for any other.
func (s *Store) Register(_ context.Context, name string, _ []byte) error {
	err := template.CheckName(name, s.exts)
	if err != nil {
		return err //nolint:wrapcheck // CheckName returns complete errors.
	}

	return template.ErrReadOnly
}

// RegisterFS returns template.ErrReadOnly.
func (*Store) RegisterFS(context.Context, iofs.FS) error {
	return template.ErrReadOnly
}
