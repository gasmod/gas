package memory

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
	"sync"

	"github.com/gasmod/gas"
	template "github.com/gasmod/gas/template"
	"github.com/gasmod/gas/template/internal/util"
)

// Store is an in-memory template store implementing gas.TemplateProvider.
// Safe for concurrent use.
type Store struct {
	templates map[string][]byte
	mu        sync.RWMutex
}

var _ gas.TemplateProvider = (*Store)(nil)

// NewStore creates an empty in-memory template store.
func NewStore() *Store {
	return &Store{
		templates: make(map[string][]byte),
	}
}

// Get returns the raw template content by name.
func (s *Store) Get(_ context.Context, name string) ([]byte, error) {
	s.mu.RLock()
	content, ok := s.templates[name]
	s.mu.RUnlock()
	if !ok {
		return nil, template.ErrTemplateNotFound
	}
	return content, nil
}

// List returns all available template names in sorted order.
func (s *Store) List(_ context.Context) ([]string, error) {
	s.mu.RLock()
	names := make([]string, 0, len(s.templates))
	for name := range s.templates {
		names = append(names, name)
	}
	s.mu.RUnlock()
	sort.Strings(names)
	return names, nil
}

// Register adds or replaces a template by name and raw content.
func (s *Store) Register(_ context.Context, name string, content []byte) error {
	s.mu.Lock()
	s.templates[name] = content
	s.mu.Unlock()
	return nil
}

// RegisterFS walks an fs.FS and registers every .html file found.
// Names are relative paths with forward slashes (e.g. "layouts/base.html").
func (s *Store) RegisterFS(ctx context.Context, fsys fs.FS) error {
	if err := util.RegisterFS(ctx, s, fsys, ".html"); err != nil {
		return fmt.Errorf("template: register fs: %w", err)
	}
	return nil
}
