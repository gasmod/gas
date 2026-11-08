package memory

import (
	"context"
	"fmt"
	"io/fs"
	"maps"
	"slices"
	"sync"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/template"
	"github.com/gasmod/gas/template/internal/files"
	"github.com/gasmod/gas/template/internal/value"
)

// Store keeps templates in memory, as described in the package
// documentation. It is safe for concurrent use.
type Store struct {
	// templates holds each template's content by name.
	templates map[string][]byte
	// exts are the extensions a name must end in.
	exts []string
	// maxSize is the most bytes a template may hold.
	maxSize int64
	// mu guards templates.
	mu sync.RWMutex
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

// NewStore returns the constructor of an empty Store with opts applied,
// in the shape the gas container expects. The constructor returns an
// error wrapping template.ErrInvalidExtension or
// template.ErrInvalidMaxSize for a bad option.
func NewStore(opts ...Option) func() (*Store, error) {
	return func() (*Store, error) {
		s := &Store{
			templates: make(map[string][]byte),
			exts:      []string{template.DefaultExtension},
			maxSize:   template.DefaultMaxSize,
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

// Get returns a copy of the content of the template name, an error
// wrapping template.ErrTemplateNotFound when there is none, or one
// wrapping template.ErrInvalidName for a name the store does not accept.
func (s *Store) Get(_ context.Context, name string) ([]byte, error) {
	err := template.CheckName(name, s.exts)
	if err != nil {
		return nil, err //nolint:wrapcheck // CheckName returns complete errors.
	}

	s.mu.RLock()

	content, ok := s.templates[name]
	s.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("%w: %q", template.ErrTemplateNotFound, name)
	}

	return value.Clone(content), nil
}

// List returns the names of the templates in byte order.
func (s *Store) List(context.Context) ([]string, error) {
	s.mu.RLock()

	names := slices.Sorted(maps.Keys(s.templates))
	s.mu.RUnlock()

	if names == nil {
		names = []string{}
	}

	return names, nil
}

// Register stores a copy of content as the template name, replacing one
// of that name. Nil content is stored as empty. A name the store does not
// accept is an error wrapping template.ErrInvalidName, and content over
// the store's maximum size one wrapping template.ErrTooLarge.
func (s *Store) Register(_ context.Context, name string, content []byte) error {
	err := template.CheckName(name, s.exts)
	if err != nil {
		return err //nolint:wrapcheck // CheckName returns complete errors.
	}

	if int64(len(content)) > s.maxSize {
		//nolint:wrapcheck // TooLarge returns complete errors.
		return template.TooLarge(name, s.maxSize)
	}

	content = value.Clone(content)

	s.mu.Lock()

	s.templates[name] = content
	s.mu.Unlock()

	return nil
}

// RegisterFS reads the files of fsys with one of the store's extensions
// and stores them all, or none when reading fails, as described in the
// template package documentation.
func (s *Store) RegisterFS(ctx context.Context, fsys fs.FS) error {
	if value.IsNil(fsys) {
		return template.ErrNilFS
	}

	all, err := files.ReadAll(ctx, fsys, s.exts, s.maxSize)
	if err != nil {
		return err //nolint:wrapcheck // ReadAll returns complete errors.
	}

	s.mu.Lock()

	for _, f := range all {
		s.templates[f.Name] = f.Content
	}

	s.mu.Unlock()

	return nil
}
