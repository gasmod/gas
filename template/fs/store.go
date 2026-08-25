// Package fs provides a read-only template store backed by an fs.FS,
// suitable for use with embed.FS or any other fs.FS implementation.
// For mutable storage, wrap this in a composite.Store with a writable
// provider such as memory.Store.
package fs

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"

	"github.com/gasmod/gas"
	template "github.com/gasmod/gas/template"
)

// Store reads templates from an fs.FS. Register and RegisterFS return
// template.ErrReadOnly — use composite.Store for mutability.
type Store struct {
	fsys fs.FS
	ext  string
}

var _ gas.TemplateProvider = (*Store)(nil)

// NewStore returns a DI-injectable constructor for a read-only template
// store backed by fsys. Only files with the ".html" extension are recognized.
func NewStore(fsys fs.FS) func() *Store {
	return func() *Store {
		return &Store{fsys: fsys, ext: ".html"}
	}
}

// Get returns the raw template content by name. Returns
// template.ErrTemplateNotFound if the file is not present.
func (s *Store) Get(_ context.Context, name string) ([]byte, error) {
	data, err := fs.ReadFile(s.fsys, name)
	if err != nil {
		return nil, template.ErrTemplateNotFound
	}
	return data, nil
}

// List returns all template names from the fs.FS in sorted order.
func (s *Store) List(_ context.Context) ([]string, error) {
	var names []string
	err := fs.WalkDir(s.fsys, ".", func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || filepath.Ext(path) != s.ext {
			return nil
		}
		names = append(names, filepath.ToSlash(path))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walking fs: %w", err)
	}
	sort.Strings(names)
	return names, nil
}

// Register always returns template.ErrReadOnly.
func (s *Store) Register(_ context.Context, _ string, _ []byte) error {
	return template.ErrReadOnly
}

// RegisterFS always returns template.ErrReadOnly.
func (s *Store) RegisterFS(_ context.Context, _ fs.FS) error {
	return template.ErrReadOnly
}
