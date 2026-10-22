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
//
// mu guards overlay, root, openErr, and closed. Disk reads hold the read
// lock, so Close waits for reads in flight before it closes the root.
type Store struct {
	openErr error
	overlay map[string][]byte
	root    *os.Root
	dir     string
	ext     string
	mu      sync.RWMutex
	closed  bool
}

var (
	_ gas.TemplateProvider = (*Store)(nil)
	_ io.Closer            = (*Store)(nil)
)

// NewStore returns a DI-injectable constructor for a filesystem-backed
// template store rooted at dir. Only files with the ".html" extension are
// recognized from disk.
func NewStore(dir string) func() (*Store, error) {
	return func() (*Store, error) {
		return &Store{
			dir:     dir,
			ext:     ".html",
			overlay: make(map[string][]byte),
		}, nil
	}
}

// open opens the root directory on first use. A failure to open it is
// kept and returned by every later call. It returns template.ErrClosed
// once the store is closed.
func (s *Store) open() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return template.ErrClosed
	}
	if s.root == nil && s.openErr == nil {
		s.root, s.openErr = os.OpenRoot(s.dir)
	}
	if s.openErr != nil {
		return fmt.Errorf("template: opening root: %w", s.openErr)
	}
	return nil
}

// withRoot opens the root if needed and runs fn with the read lock held,
// so Close cannot close the root while fn uses it.
func (s *Store) withRoot(fn func(root *os.Root) error) error {
	if err := s.open(); err != nil {
		return err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.closed {
		return template.ErrClosed
	}
	return fn(s.root)
}

// Get returns the raw template content by name. Checks the in-memory
// overlay first, then falls back to disk. It returns template.ErrClosed
// after Close.
func (s *Store) Get(_ context.Context, name string) ([]byte, error) {
	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		return nil, template.ErrClosed
	}
	if content, ok := s.overlay[name]; ok {
		s.mu.RUnlock()
		return content, nil
	}
	s.mu.RUnlock()

	var data []byte
	err := s.withRoot(func(root *os.Root) error {
		var readErr error
		data, readErr = fs.ReadFile(root.FS(), name)
		if readErr != nil {
			return template.ErrTemplateNotFound
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return data, nil
}

// List returns all available template names from both disk and the
// overlay, sorted and deduplicated. It returns template.ErrClosed after
// Close.
func (s *Store) List(context.Context) ([]string, error) {
	seen := make(map[string]struct{})

	err := s.withRoot(func(root *os.Root) error {
		// Walk disk.
		_ = fs.WalkDir(root.FS(), ".", func(path string, d fs.DirEntry, err error) error {
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
		for name := range s.overlay {
			seen[name] = struct{}{}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// Register adds or replaces a template in the in-memory overlay. It
// returns template.ErrClosed after Close.
func (s *Store) Register(_ context.Context, name string, content []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return template.ErrClosed
	}
	s.overlay[name] = content
	return nil
}

// RegisterFS walks an fs.FS and registers every .html file found into
// the in-memory overlay. It returns template.ErrClosed after Close.
func (s *Store) RegisterFS(ctx context.Context, fsys fs.FS) error {
	s.mu.RLock()
	closed := s.closed
	s.mu.RUnlock()
	if closed {
		return template.ErrClosed
	}

	if err := util.RegisterFS(ctx, s, fsys, ".html"); err != nil {
		return fmt.Errorf("template: register fs: %w", err)
	}
	return nil
}

// Close releases the os.Root handle if it was opened and drops the
// overlay. It waits for disk reads in flight. Every later operation
// returns template.ErrClosed, and a second Close returns nil.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil
	}
	s.closed = true
	s.overlay = nil

	if s.root != nil {
		root := s.root
		s.root = nil
		if err := root.Close(); err != nil {
			return fmt.Errorf("template: closing root: %w", err)
		}
	}
	return nil
}
