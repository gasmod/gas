package files

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"slices"
	"strings"
	"syscall"

	"github.com/gasmod/gas/template"
)

// File is a file ReadAll read.
type File struct {
	// Name is the file's path in the file system, its template name.
	Name string
	// Content is the file's content, never nil.
	Content []byte
}

// errPathEscapes is the text of the error os.Root returns for a path or a
// symlink that leads out of the root, or is absolute. The os package does
// not export it.
const errPathEscapes = "path escapes from parent"

// chunk is how many directory entries Entry reads at a time.
const chunk = 128

// Read returns the content of the template name in fsys, at most maxSize
// bytes, as described in the package documentation. name must have
// passed template.CheckName.
func Read(fsys fs.FS, name string, maxSize int64) ([]byte, error) {
	found, err := Find(fsys, name)
	if err != nil {
		return nil, fmt.Errorf("%w: %q: %w", template.ErrRead, name, Cause(err))
	}

	if !found {
		return nil, fmt.Errorf("%w: %q", template.ErrTemplateNotFound, name)
	}

	f, err := fsys.Open(name)
	if Absent(err) {
		// The file went away, or was replaced by a symlink out of the
		// root, between Find and the open.
		return nil, fmt.Errorf("%w: %q", template.ErrTemplateNotFound, name)
	}

	if err != nil {
		return nil, fmt.Errorf("%w: %q: %w", template.ErrRead, name, Cause(err))
	}

	return ReadClose(f, name, maxSize)
}

// ReadClose reads the open file f of the template name, at most maxSize
// bytes, into one buffer sized by f's Stat, and closes f. More than
// maxSize is an error wrapping template.ErrTooLarge, told before the
// content is read when Stat gives the size; another failure is an error
// wrapping template.ErrRead. The content is never nil.
func ReadClose(f fs.File, name string, maxSize int64) ([]byte, error) {
	b, err := read(f, name, maxSize)

	closeErr := f.Close()
	if err == nil && closeErr != nil {
		err = fmt.Errorf("%w: %q: %w", template.ErrRead, name, Cause(closeErr))
	}

	if err != nil {
		return nil, err
	}

	return b, nil
}

// read is ReadClose without the close.
func read(f fs.File, name string, maxSize int64) ([]byte, error) {
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("%w: %q: %w", template.ErrRead, name, Cause(err))
	}

	b, tooLarge, err := ReadLimited(f, info.Size(), maxSize)
	if err != nil {
		return nil, fmt.Errorf("%w: %q: %w", template.ErrRead, name, Cause(err))
	}

	if tooLarge {
		//nolint:wrapcheck // TooLarge returns complete errors.
		return nil, template.TooLarge(name, maxSize)
	}

	return b, nil
}

// ReadLimited reads r to its end into one buffer of capacity sizeHint+1,
// grown only when r holds more than sizeHint, and reports tooLarge,
// without reading on, once it holds more than maxSize bytes or sizeHint
// says it does. The content is never nil.
func ReadLimited(r io.Reader, sizeHint, maxSize int64) (b []byte, tooLarge bool, err error) {
	if sizeHint > maxSize {
		return nil, true, nil
	}

	b = make([]byte, 0, max(sizeHint, 0)+1)

	lr := io.LimitReader(r, maxSize+1)

	for {
		if len(b) == cap(b) {
			b = append(b, 0)[:len(b)]
		}

		n, err := lr.Read(b[len(b):cap(b)])

		b = b[:len(b)+n]

		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return nil, false, err //nolint:wrapcheck // the callers wrap it with the name.
		}
	}

	if int64(len(b)) > maxSize {
		return nil, true, nil
	}

	return b, false, nil
}

// Find reports whether name is a template of fsys, as described in the
// package documentation: every directory on the way to it is a directory
// and not a symlink, every element matches an entry's name byte for byte,
// and the last is a regular file. It stats name first, so a name that is
// not there costs no directory read. An error is one of reading a
// directory, other than one that means nothing is there.
func Find(fsys fs.FS, name string) (bool, error) {
	_, err := fs.Stat(fsys, name)
	if Absent(err) {
		// Stat follows symlinks, which Find refuses, so nothing Stat
		// cannot reach is a template.
		return false, nil
	}

	dir := "."

	elems := strings.Split(name, "/")
	for i, elem := range elems {
		e, err := Entry(fsys, dir, elem)
		if err != nil || e == nil {
			return false, err
		}

		if i == len(elems)-1 {
			return e.Type().IsRegular(), nil
		}

		if !e.IsDir() {
			return false, nil
		}

		dir = path.Join(dir, elem)
	}

	return false, nil
}

// Entry returns the entry of the directory dir of fsys whose name is elem
// byte for byte, or nil when there is none or nothing is at dir. When dir
// opens as an fs.ReadDirFile, as os.Root's, os.DirFS's and embed.FS's
// directories do, it reads a chunk of entries at a time and stops at
// elem.
func Entry(fsys fs.FS, dir, elem string) (fs.DirEntry, error) {
	f, err := fsys.Open(dir)
	if Absent(err) {
		return nil, nil
	}

	if err != nil {
		return nil, err //nolint:wrapcheck // the callers wrap it with the name.
	}

	var e fs.DirEntry

	if rd, ok := f.(fs.ReadDirFile); ok {
		e, err = entryIn(rd, elem)
	} else {
		var entries []fs.DirEntry

		entries, err = fs.ReadDir(fsys, dir)
		if at := slices.IndexFunc(entries, named(elem)); at >= 0 {
			e = entries[at]
		}
	}

	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}

	if Absent(err) {
		return nil, nil
	}

	if err != nil {
		return nil, err
	}

	return e, nil
}

// entryIn reads the open directory rd a chunk at a time until an entry
// whose name is elem byte for byte, and returns it, or nil when there is
// none.
func entryIn(rd fs.ReadDirFile, elem string) (fs.DirEntry, error) {
	for {
		entries, err := rd.ReadDir(chunk)
		if at := slices.IndexFunc(entries, named(elem)); at >= 0 {
			return entries[at], nil
		}

		if errors.Is(err, io.EOF) || (err == nil && len(entries) == 0) {
			return nil, nil
		}

		if err != nil {
			return nil, err //nolint:wrapcheck // Entry's callers wrap it.
		}
	}
}

// named returns a test of an entry's name against elem.
func named(elem string) func(fs.DirEntry) bool {
	return func(e fs.DirEntry) bool { return e.Name() == elem }
}

// List returns the names of the regular files of fsys that
// template.CheckName accepts with exts, in byte order, as described in
// the package documentation, never nil. Its errors wrap template.ErrList.
func List(ctx context.Context, fsys fs.FS, exts []string) ([]string, error) {
	names := []string{}

	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("%q: %w", p, Cause(err))
		}

		ctxErr := ctx.Err()
		if ctxErr != nil {
			return ctxErr //nolint:wrapcheck // List wraps it with ErrList.
		}

		if d.Type().IsRegular() && Accepted(p, exts) {
			names = append(names, p)
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", template.ErrList, err)
	}

	slices.Sort(names)

	return names, nil
}

// Accepted reports whether template.CheckName accepts name with exts.
func Accepted(name string, exts []string) bool {
	return template.CheckName(name, exts) == nil
}

// ReadAll walks fsys and reads its regular files with one of exts, each
// at most maxSize bytes, in byte order of their names, as described in
// the package documentation. It checks ctx before each file and stops at
// the first failure.
func ReadAll(ctx context.Context, fsys fs.FS, exts []string, maxSize int64) ([]File, error) {
	var out []File

	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("%w: %q: %w", template.ErrRead, p, Cause(err))
		}

		if !d.Type().IsRegular() || !template.HasExtension(p, exts) {
			return nil
		}

		err = template.CheckName(p, exts)
		if err != nil {
			return err //nolint:wrapcheck // CheckName returns complete errors.
		}

		err = ctx.Err()
		if err != nil {
			return fmt.Errorf("%sregister files: %w", template.ErrorPrefix, err)
		}

		f, err := fsys.Open(p)
		if err != nil {
			return fmt.Errorf("%w: %q: %w", template.ErrRead, p, Cause(err))
		}

		b, err := ReadClose(f, p, maxSize)
		if err != nil {
			return err
		}

		out = append(out, File{Name: p, Content: b})

		return nil
	})
	if err != nil {
		return nil, err //nolint:wrapcheck // the walk function returns complete errors.
	}

	slices.SortFunc(out, func(a, b File) int { return strings.Compare(a.Name, b.Name) })

	return out, nil
}

// Cause returns the error a *fs.PathError holds, without the path, which
// for some file systems is absolute, or err itself.
func Cause(err error) error {
	if pe, ok := errors.AsType[*fs.PathError](err); ok {
		return pe.Err
	}

	return err
}

// Absent reports whether err means that nothing is at a path: it does not
// exist, an element of it is no directory, a symlink on it loops, or it
// leads out of an os.Root.
func Absent(err error) bool {
	if err == nil {
		return false
	}

	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) ||
		errors.Is(err, syscall.ELOOP) {
		return true
	}

	var pe *fs.PathError

	return errors.As(err, &pe) && pe.Err.Error() == errPathEscapes
}
