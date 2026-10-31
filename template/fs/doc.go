// Package fs reads templates from an fs.FS, such as an embed.FS, and
// cannot be written to. [Store] implements gas.TemplateProvider with the
// contract of the template package; Register and RegisterFS return
// template.ErrReadOnly.
//
// # Usage
//
//	//go:embed templates
//	var embedded embed.FS
//
//	sub, err := fs.Sub(embedded, "templates")
//	if err != nil {
//		return err
//	}
//	store, err := templatefs.NewStore(sub)()
//
// After this Get(ctx, "pages/home.html") returns the content of
// `templates/pages/home.html`. In an app, register the constructor:
//
//	gas.WithSingletonService[gas.TemplateProvider](templatefs.NewStore(sub))
//
// # Lookup
//
// Get first stats the name, so a name that is not there costs no
// directory read. It then reads the name element by element: each
// directory on the way is read, a chunk of entries at a time, until an
// entry whose name is the element byte for byte, so that `Home.html` is
// not found for a file `home.html` even on a file system that ignores
// case, such as macOS's. Every element but the last must be a directory,
// and the last a regular file. The fs.FS must therefore support reading
// directories, as fs.WalkDir needs too. A directory that cannot be read,
// or a file that is there but cannot be read, is an error wrapping
// template.ErrRead; nothing there is template.ErrTemplateNotFound. A file
// over the store's maximum size, [WithMaxSize], is an error wrapping
// template.ErrTooLarge, and is not read past it.
//
// List walks the fs.FS with fs.WalkDir and returns the names Get would
// find, sorted in byte order; it checks the context before each entry. A
// failure to walk is an error wrapping template.ErrList that names the
// path where the walk failed, inside the fs.FS.
//
// # Symlinks
//
// The store never follows a symlink it can tell is one: an entry that
// fs.ReadDir reports with fs.ModeSymlink is neither listed nor read, nor
// walked through on the way to a name. os.DirFS and os.Root's FS report
// symlinks so. An fs.FS whose directory entries do not report them gets
// whatever its Open does.
//
// os.DirFS follows symlinks out of its directory when a file is opened:
// with `evil.html` a symlink to `/etc/passwd.html`, fs.ReadFile on it
// returns that file. The store refuses `evil.html` because the directory
// entry is a symlink, but a symlink that replaces a file between that
// check and the read is still followed. For a directory on disk, use
// gas/template/dir, which reads through an os.Root, opens every file
// without blocking and checks what it opened, or give this store
// os.Root's FS, which refuses every path out of the root:
//
//	root, err := os.OpenRoot("templates")
//	store, err := templatefs.NewStore(root.FS())()
//
// An fs.FS has no way to open a file without blocking, so over a
// directory on disk a FIFO put in place of a template between Get's
// lookup and its open makes Get wait for a writer. Keep this store to
// file systems that do not change under it, such as an embed.FS, or to a
// directory no one the app does not trust can write to.
//
// # Concurrency
//
// A Store holds no state of its own beyond its fs.FS and extensions, and
// is safe for concurrent use when the fs.FS is, as embed.FS, os.DirFS and
// os.Root's FS are.
//
// # Errors
//
// The constructor returns template.ErrNilFS for a nil fs.FS and an error
// wrapping template.ErrInvalidExtension or template.ErrInvalidMaxSize for
// a bad option. The methods return errors wrapping
// template.ErrInvalidName, template.ErrTemplateNotFound, template.ErrRead,
// template.ErrTooLarge, template.ErrList, the context's error and
// template.ErrReadOnly, with the texts the template package lists. A path
// in an error is the path inside the fs.FS; the error of an *fs.PathError
// is kept without its path, which some file systems give in full.
package fs
