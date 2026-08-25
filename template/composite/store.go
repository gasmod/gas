package composite

import (
	"context"
	"fmt"
	"io/fs"
	"sort"

	"github.com/gasmod/gas"
	template "github.com/gasmod/gas/template"
)

// Store chains multiple TemplateProviders. Get checks the writable
// provider first, then each reader in order. Register and RegisterFS
// delegate to the writable provider.
type Store struct {
	writable gas.TemplateProvider
	readers  []gas.TemplateProvider
}

// NewStore creates a composite store. The writable provider receives
// Register/RegisterFS calls and is checked first during Get. Readers
// are checked in order as fallbacks.
func NewStore(writable gas.TemplateProvider, readers ...gas.TemplateProvider) *Store {
	return &Store{writable: writable, readers: readers}
}

// Get tries the writable provider first, then each reader in order.
// Returns template.ErrTemplateNotFound if no provider has the template.
func (s *Store) Get(ctx context.Context, name string) ([]byte, error) {
	content, err := s.writable.Get(ctx, name)
	if err == nil {
		return content, nil
	}
	for _, r := range s.readers {
		content, err = r.Get(ctx, name)
		if err == nil {
			return content, nil
		}
	}
	return nil, template.ErrTemplateNotFound
}

// List merges template names from all providers, deduplicated and sorted.
func (s *Store) List(ctx context.Context) ([]string, error) {
	seen := make(map[string]struct{})
	var firstErr error

	names, err := s.writable.List(ctx)
	if err != nil {
		firstErr = err
	}
	for _, n := range names {
		seen[n] = struct{}{}
	}

	for _, r := range s.readers {
		names, err = r.List(ctx)
		if err != nil && firstErr == nil {
			firstErr = err
		}
		for _, n := range names {
			seen[n] = struct{}{}
		}
	}

	if len(seen) == 0 && firstErr != nil {
		return nil, firstErr
	}

	result := make([]string, 0, len(seen))
	for n := range seen {
		result = append(result, n)
	}
	sort.Strings(result)
	return result, nil
}

// Register delegates to the writable provider.
func (s *Store) Register(ctx context.Context, name string, content []byte) error {
	if err := s.writable.Register(ctx, name, content); err != nil {
		return fmt.Errorf("composite: register: %w", err)
	}
	return nil
}

// RegisterFS delegates to the writable provider.
func (s *Store) RegisterFS(ctx context.Context, fsys fs.FS) error {
	if err := s.writable.RegisterFS(ctx, fsys); err != nil {
		return fmt.Errorf("composite: register fs: %w", err)
	}
	return nil
}
