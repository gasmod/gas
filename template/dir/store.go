package dir

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/template"
	"github.com/gasmod/gas/template/internal/files"
	"github.com/gasmod/gas/template/internal/value"
)

// ServiceName is the name of the store, returned by Store.Name.
const ServiceName = "gas/template/dir"

var (
	// ErrEmptyDir indicates that the constructor was given an empty
	// directory path.
	ErrEmptyDir = errors.New(template.ErrorPrefix + "directory path is empty")
	// ErrDirUnavailable indicates that Init could not open the directory.
	ErrDirUnavailable = errors.New(template.ErrorPrefix + "directory unavailable")
)

// state is where a Store is in its lifecycle.
type state int

const (
	// stateNew is a store Init never succeeded on.
	stateNew state = iota
	// stateOpen is an initialized store.
	stateOpen
	// stateClosed is a store closed after Init, until Init runs again.
	stateClosed
)

// Store reads templates from a directory on disk, through an os.Root, and
// keeps registered ones in memory over them, as described in the package
// documentation. It implements gas.Service and is safe for concurrent
// use.
type Store struct {
	// root is the open directory, or nil unless the store is open.
	root *os.Root
	// overlay holds the registered templates by name.
	overlay map[string][]byte
	// path is the directory's path, as given.
	path string
	// abs is the directory's absolute path, taken in Init, to tell
	// whether it is still there.
	abs string
	// exts are the extensions a name must end in.
	exts []string
	// maxSize is the most bytes a template may hold.
	maxSize int64
	// state is the store's lifecycle state.
	state state
	// rootMu guards root and state. Reads of the directory hold it for
	// reading, so that Close, which holds it for writing, waits for them
	// before it closes the root.
	rootMu sync.RWMutex
	// mu guards overlay, so that Register does not wait for reads of the
	// directory.
	mu sync.RWMutex
}

var (
	_ gas.TemplateProvider = (*Store)(nil)
	_ gas.Service          = (*Store)(nil)
)

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

// NewStore returns the constructor of a Store over the directory at path
// with opts applied, in the shape the gas container expects. The
// constructor opens nothing; Init does. It returns ErrEmptyDir for an
// empty path and an error wrapping template.ErrInvalidExtension for a bad
// extension set.
func NewStore(path string, opts ...Option) func() (*Store, error) {
	return func() (*Store, error) {
		if path == "" {
			return nil, ErrEmptyDir
		}

		s := &Store{
			path:    path,
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

// Name returns ServiceName.
func (*Store) Name() string { return ServiceName }

// Init opens the directory, as described in the package documentation. A
// failure is an error wrapping ErrDirUnavailable that gives the reason and
// never the path, and leaves the store as it was. On an open store Init
// returns template.ErrAlreadyInitialized and changes nothing.
func (s *Store) Init(context.Context) error {
	s.rootMu.Lock()
	defer s.rootMu.Unlock()

	if s.state == stateOpen {
		return template.ErrAlreadyInitialized
	}

	abs, err := filepath.Abs(s.path)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrDirUnavailable, files.Cause(err))
	}

	root, err := os.OpenRoot(abs)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrDirUnavailable, files.Cause(err))
	}

	s.abs = abs

	s.mu.Lock()

	s.overlay = make(map[string][]byte)
	s.mu.Unlock()

	s.root = root
	s.state = stateOpen

	return nil
}

// Close closes the directory and drops the registered templates, after
// the reads in flight end. Every later call but Init returns an error
// wrapping template.ErrClosed. Close on a store that is not open returns
// nil.
func (s *Store) Close(context.Context) error {
	s.rootMu.Lock()
	defer s.rootMu.Unlock()

	if s.state != stateOpen {
		return nil
	}

	root := s.root

	s.root = nil
	s.state = stateClosed

	s.mu.Lock()

	s.overlay = nil
	s.mu.Unlock()

	err := root.Close()
	if err != nil {
		return fmt.Errorf("%sclose directory: %w", template.ErrorPrefix, files.Cause(err))
	}

	return nil
}

// Get returns a copy of the registered template name, or else its content
// read from the directory, as described in the package documentation. Its
// errors wrap template.ErrInvalidName, template.ErrTemplateNotFound,
// template.ErrRead, template.ErrTooLarge, template.ErrNotInitialized,
// template.ErrClosed or ctx's error.
func (s *Store) Get(ctx context.Context, name string) ([]byte, error) {
	err := template.CheckName(name, s.exts)
	if err != nil {
		return nil, err //nolint:wrapcheck // CheckName returns complete errors.
	}

	err = ctx.Err()
	if err != nil {
		return nil, fmt.Errorf("%sget %q: %w", template.ErrorPrefix, name, err)
	}

	s.rootMu.RLock()
	defer s.rootMu.RUnlock()

	err = s.usable()
	if err != nil {
		return nil, err
	}

	s.mu.RLock()

	content, ok := s.overlay[name]
	s.mu.RUnlock()

	if ok {
		return value.Clone(content), nil
	}

	return s.read(s.root, name)
}

// List returns the names of the registered templates and of the
// directory's templates, once each, in byte order, as described in the
// package documentation. A failure to walk the directory is an error
// wrapping template.ErrList.
func (s *Store) List(ctx context.Context) ([]string, error) {
	s.rootMu.RLock()
	defer s.rootMu.RUnlock()

	err := s.usable()
	if err != nil {
		return nil, err
	}

	names, err := s.list(ctx, s.root)
	if err != nil {
		return nil, err
	}

	err = s.present(s.root)
	if err != nil {
		return nil, err
	}

	s.mu.RLock()

	names = slices.AppendSeq(names, maps.Keys(s.overlay))
	s.mu.RUnlock()

	slices.Sort(names)

	return slices.Compact(names), nil
}

// Register keeps a copy of content as the template name, over any file of
// that name. Nil content is kept as empty. Its errors wrap
// template.ErrInvalidName, template.ErrTooLarge,
// template.ErrNotInitialized or template.ErrClosed.
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

	s.rootMu.RLock()
	defer s.rootMu.RUnlock()

	err = s.usable()
	if err != nil {
		return err
	}

	s.mu.Lock()

	s.overlay[name] = content
	s.mu.Unlock()

	return nil
}

// RegisterFS reads the files of fsys with one of the store's extensions
// and keeps them all, or none when reading fails, as described in the
// template package documentation. It reads fsys before it takes the
// store's lock, so that a slow fsys does not hold up Close.
func (s *Store) RegisterFS(ctx context.Context, fsys fs.FS) error {
	if value.IsNil(fsys) {
		return template.ErrNilFS
	}

	err := s.usableNow()
	if err != nil {
		return err
	}

	all, err := files.ReadAll(ctx, fsys, s.exts, s.maxSize)
	if err != nil {
		return err //nolint:wrapcheck // ReadAll returns complete errors.
	}

	s.rootMu.RLock()
	defer s.rootMu.RUnlock()

	// Close, and Init after it, may have run while fsys was read.
	err = s.usable()
	if err != nil {
		return err
	}

	s.mu.Lock()

	for _, f := range all {
		s.overlay[f.Name] = f.Content
	}

	s.mu.Unlock()

	return nil
}

// present returns nil when the directory the store opened is still at
// its path, and otherwise an error wrapping template.ErrList, which names
// no path: an open directory that was removed reads as empty, and a
// directory moved away is not the one the store was given.
func (s *Store) present(root *os.Root) error {
	opened, err := root.Stat(".")
	if err != nil {
		return fmt.Errorf("%w: %q: %w", template.ErrList, ".", files.Cause(err))
	}

	current, err := os.Stat(s.abs)
	if files.Absent(err) || (err == nil && !os.SameFile(opened, current)) {
		return fmt.Errorf("%w: directory removed or moved", template.ErrList)
	}

	if err != nil {
		return fmt.Errorf("%w: %q: %w", template.ErrList, ".", files.Cause(err))
	}

	return nil
}

// usableNow is usable, under rootMu.
func (s *Store) usableNow() error {
	s.rootMu.RLock()
	defer s.rootMu.RUnlock()

	return s.usable()
}

// list returns the names of the directory's templates, as described in
// the package documentation: its regular files and the symlinks that
// lead to one, whose names the store accepts, in walk order. A failure
// to walk is an error wrapping template.ErrList.
func (s *Store) list(ctx context.Context, root *os.Root) ([]string, error) {
	r := newResolver(root, s.exts)
	names := []string{}

	err := fs.WalkDir(rootFS{root: root}, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("%q: %w", p, files.Cause(err))
		}

		err = ctx.Err()
		if err != nil {
			return err //nolint:wrapcheck // list wraps it with ErrList.
		}

		if d.IsDir() || !files.Accepted(p, s.exts) {
			return nil
		}

		if d.Type().IsRegular() {
			names = append(names, p)

			return nil
		}

		if d.Type()&fs.ModeSymlink == 0 {
			return nil
		}

		_, _, found, err := r.lookup(p, true)
		if err != nil {
			return fmt.Errorf("%q: %w", p, files.Cause(err))
		}

		if found {
			names = append(names, p)
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", template.ErrList, err)
	}

	return names, nil
}

// usable returns nil when the store is open, and otherwise
// template.ErrNotInitialized or template.ErrClosed. The caller holds
// rootMu.
func (s *Store) usable() error {
	if s.state == stateNew {
		return template.ErrNotInitialized
	}

	if s.state == stateClosed {
		return template.ErrClosed
	}

	return nil
}
