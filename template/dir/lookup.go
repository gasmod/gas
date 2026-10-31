package dir

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"

	"github.com/gasmod/gas/template"
	"github.com/gasmod/gas/template/internal/files"
)

// maxHops is the most symlinks a name may lead through, as Linux's
// MAXSYMLINKS; a longer chain is taken as a loop.
const maxHops = 40

// rootFS is an os.Root as an fs.FS that opens every file with openFlags,
// so that a FIFO put in place of a directory cannot block a walk or a
// directory read.
type rootFS struct {
	// root is the open directory.
	root *os.Root
}

// Open opens name in the root with openFlags; the root refuses a name
// that leads out of it.
func (r rootFS) Open(name string) (fs.File, error) {
	//nolint:wrapcheck // the callers tell absence, then wrap it.
	return r.root.OpenFile(name, openFlags, 0)
}

// resolver looks names up in a root for one call of the store, as
// described in the package documentation.
type resolver struct {
	// root is the open directory.
	root *os.Root
	// dirs remembers, for this call only, whether a path in the root is a
	// directory and not a symlink.
	dirs map[string]bool
	// exts are the extensions a symlink's target must end in.
	exts []string
}

// newResolver returns a resolver over root for the extensions exts.
func newResolver(root *os.Root, exts []string) *resolver {
	return &resolver{root: root, exts: exts, dirs: make(map[string]bool)}
}

// lookup returns the path in the root of the regular file name leads to
// and the file's Lstat, or found false when there is none: name is a
// regular file, or a symlink that leads, through relative targets that
// each end in one of the extensions and stay in the root, to a regular
// file, never through a symlink to a directory. When exact is false the
// case of a symlink's name is checked before it is followed; the caller
// checks a regular file's own. An error is one other than absence.
func (r *resolver) lookup(name string, exact bool) (string, fs.FileInfo, bool, error) {
	info, err := r.lstat(name)
	if err != nil || info == nil {
		return "", nil, false, err
	}

	if info.Mode().IsRegular() {
		return name, info, true, nil
	}

	if info.Mode()&fs.ModeSymlink == 0 {
		return "", nil, false, nil
	}

	if !exact {
		ok, err := r.exact(name)
		if err != nil || !ok {
			return "", nil, false, err
		}
	}

	return r.chain(name)
}

// chain follows the symlink name, as lookup describes, to the regular
// file it leads to.
func (r *resolver) chain(name string) (string, fs.FileInfo, bool, error) {
	p := name

	for range maxHops {
		next, ok, err := r.follow(p)
		if err != nil || !ok {
			return "", nil, false, err
		}

		info, err := r.lstat(next)
		if err != nil || info == nil {
			return "", nil, false, err
		}

		if info.Mode().IsRegular() {
			return next, info, true, nil
		}

		if info.Mode()&fs.ModeSymlink == 0 {
			return "", nil, false, nil
		}

		p = next
	}

	return "", nil, false, nil
}

// follow returns the path in the root that the symlink p leads to, or ok
// false when its target is absolute, leads out of the root, or does not
// end in one of the extensions.
func (r *resolver) follow(p string) (string, bool, error) {
	target, err := r.root.Readlink(p)
	if files.Absent(err) {
		return "", false, nil
	}

	if err != nil {
		return "", false, err //nolint:wrapcheck // Get and List wrap it with the name.
	}

	if path.IsAbs(target) || strings.ContainsRune(target, '\\') {
		return "", false, nil
	}

	next := path.Join(path.Dir(p), target)
	if !fs.ValidPath(next) || !template.HasExtension(next, r.exts) {
		return "", false, nil
	}

	return next, true, nil
}

// lstat returns the Lstat of p when every directory on the way to it is
// a directory and not a symlink, and nil when nothing is there.
func (r *resolver) lstat(p string) (fs.FileInfo, error) {
	for i := range len(p) {
		if p[i] != '/' {
			continue
		}

		ok, err := r.isDir(p[:i])
		if err != nil || !ok {
			return nil, err
		}
	}

	info, err := r.root.Lstat(p)
	if files.Absent(err) {
		return nil, nil
	}

	if err != nil {
		return nil, err //nolint:wrapcheck // Get and List wrap it with the name.
	}

	return info, nil
}

// isDir reports whether dir is a directory and not a symlink, from the
// memo when this call asked before.
func (r *resolver) isDir(dir string) (bool, error) {
	if ok, seen := r.dirs[dir]; seen {
		return ok, nil
	}

	info, err := r.root.Lstat(dir)
	if files.Absent(err) {
		r.dirs[dir] = false

		return false, nil
	}

	if err != nil {
		return false, err //nolint:wrapcheck // Get and List wrap it with the name.
	}

	r.dirs[dir] = info.IsDir()

	return info.IsDir(), nil
}

// exact reports whether every element of name matches an entry of its
// directory byte for byte, reading each directory on the way until the
// entry.
func (r *resolver) exact(name string) (bool, error) {
	dir := "."

	for elem := range strings.SplitSeq(name, "/") {
		e, err := files.Entry(rootFS{root: r.root}, dir, elem)
		if err != nil || e == nil {
			return false, err //nolint:wrapcheck // Get wraps it with the name.
		}

		dir = path.Join(dir, elem)
	}

	return true, nil
}

// read returns the content of the template name, read from the root, as
// described in the package documentation.
func (s *Store) read(root *os.Root, name string) ([]byte, error) {
	r := newResolver(root, s.exts)

	p, info, found, err := r.lookup(name, false)
	if err != nil {
		return nil, fmt.Errorf("%w: %q: %w", template.ErrRead, name, files.Cause(err))
	}

	if !found {
		return nil, fmt.Errorf("%w: %q", template.ErrTemplateNotFound, name)
	}

	f, err := root.OpenFile(p, openFlags, 0)
	if files.Absent(err) {
		return nil, fmt.Errorf("%w: %q", template.ErrTemplateNotFound, name)
	}

	if err != nil {
		return nil, fmt.Errorf("%w: %q: %w", template.ErrRead, name, files.Cause(err))
	}

	err = s.check(r, f, name, p, info)
	if err != nil {
		closeErr := f.Close()
		if closeErr != nil {
			return nil, fmt.Errorf("%w: %q: %w", template.ErrRead, name, files.Cause(closeErr))
		}

		return nil, err
	}

	//nolint:wrapcheck // ReadClose returns complete errors.
	return files.ReadClose(f, name, s.maxSize)
}

// check returns nil when the open file f is the regular file lookup found
// at p for the template name, and name's case is exact.
func (*Store) check(r *resolver, f *os.File, name, p string, info fs.FileInfo) error {
	opened, err := f.Stat()
	if err != nil {
		return fmt.Errorf("%w: %q: %w", template.ErrRead, name, files.Cause(err))
	}

	if !opened.Mode().IsRegular() {
		return fmt.Errorf("%w: %q: not a regular file", template.ErrRead, name)
	}

	if !os.SameFile(opened, info) {
		return fmt.Errorf("%w: %q: replaced while read", template.ErrRead, name)
	}

	if p != name || exactPath(f, name) {
		// lookup checked a symlink's name before following it.
		return nil
	}

	ok, err := r.exact(name)
	if err != nil {
		return fmt.Errorf("%w: %q: %w", template.ErrRead, name, files.Cause(err))
	}

	if !ok {
		return fmt.Errorf("%w: %q", template.ErrTemplateNotFound, name)
	}

	return nil
}
