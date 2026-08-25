package dir

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/gasmod/gas"
	template "github.com/gasmod/gas/template"
	"github.com/gasmod/gas/template/internal/util"
)

// Store reads templates from a directory on disk. File operations are
// sandboxed to the root directory via os.Root. An in-memory overlay
// handles programmatic registrations via Register and RegisterFS.
type Store struct {
	err     error
	overlay map[string][]byte
	root    *os.Root
	dir     string
	ext     string
	mu      sync.RWMutex
	once    sync.Once
}

var _ gas.TemplateProvider = (*Store)(nil)
var _ io.Closer = (*Store)(nil)

// NewStore returns a DI-injectable constructor for a filesystem-backed
// template store rooted at dir. Only files with the ".html" extension are
// recognized from disk.
func NewStore(dir string) func() *Store {
	return func() *Store {
		return &Store{
			dir:     dir,
			ext:     ".html",
			overlay: make(map[string][]byte),
		}
	}
}

func (s *Store) init() error {
	s.once.Do(func() {
		s.root, s.err = os.OpenRoot(s.dir)
	})
	return s.err
}

// Get returns the raw template content by name. Checks the in-memory
// overlay first, then falls back to disk.
func (s *Store) Get(_ context.Context, name string) ([]byte, error) {
	s.mu.RLock()
	if content, ok := s.overlay[name]; ok {
		s.mu.RUnlock()
		return content, nil
	}
	s.mu.RUnlock()

	if err := s.init(); err != nil {
		return nil, fmt.Errorf("template: opening root: %w", err)
	}
	data, err := fs.ReadFile(s.root.FS(), name)
	if err != nil {
		return nil, template.ErrTemplateNotFound
	}
	return data, nil
}

// List returns all available template names from both disk and the
// overlay, sorted and deduplicated.
func (s *Store) List(_ context.Context) ([]string, error) {
	if err := s.init(); err != nil {
		return nil, fmt.Errorf("template: opening root: %w", err)
	}

	seen := make(map[string]struct{})

	// Walk disk.
	_ = fs.WalkDir(s.root.FS(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if filepath.Ext(path) != s.ext {
			return nil
		}
		seen[filepath.ToSlash(path)] = struct{}{}
		return nil
	})

	// Merge overlay.
	s.mu.RLock()
	for name := range s.overlay {
		seen[name] = struct{}{}
	}
	s.mu.RUnlock()

	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// Register adds or replaces a template in the in-memory overlay.
func (s *Store) Register(_ context.Context, name string, content []byte) error {
	s.mu.Lock()
	s.overlay[name] = content
	s.mu.Unlock()
	return nil
}

// RegisterFS walks an fs.FS and registers every .html file found into
// the in-memory overlay.
func (s *Store) RegisterFS(ctx context.Context, fsys fs.FS) error {
	if err := util.RegisterFS(ctx, s, fsys, ".html"); err != nil {
		return fmt.Errorf("template: register fs: %w", err)
	}
	return nil
}

// Close releases the os.Root handle if it was opened.
func (s *Store) Close() error {
	if s.root != nil {
		if err := s.root.Close(); err != nil {
			return fmt.Errorf("template: closing root: %w", err)
		}
		s.root = nil
	}
	return nil
}
