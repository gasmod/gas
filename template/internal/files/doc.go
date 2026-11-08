// Package files reads templates out of an fs.FS for the fs store and for
// every store's RegisterFS, and gives the dir store the pieces it shares
// with them, with the name, symlink, case and size rules of the template
// package documentation.
//
// # Usage
//
//	b, err := files.Read(fsys, "pages/home.html", template.DefaultMaxSize)
//	names, err := files.List(ctx, fsys, []string{".html"})
//	all, err := files.ReadAll(ctx, fsys, []string{".html"}, template.DefaultMaxSize)
//
// # Lookup
//
// [Find] stats a name first, so that a name that is not there costs no
// directory read. It then walks the name element by element, looking in
// each directory with [Entry] for an entry whose name is the element byte
// for byte, so that a file system that ignores case, such as macOS's,
// does not find `Home.html` for `home.html`. Every element but the last
// must be a directory entry that is not a symlink, and the last a regular
// file; a symlink is never followed. Entry reads a directory that opens
// as an fs.ReadDirFile a chunk of entries at a time and stops at the
// entry, and any other through fs.ReadDir. [Read] then opens the file and
// reads it with [ReadClose].
//
// # Size
//
// [ReadClose] reads an open file into one buffer sized by its Stat, and
// [ReadLimited] under it reads at most the maximum size and one byte
// more, so that a file over the maximum is an error wrapping
// template.ErrTooLarge without being read whole, and one whose Stat
// already says so is not read at all.
//
// # Listing
//
// [List] walks the file system with fs.WalkDir, which does not follow
// symlinked directories, and keeps the regular files whose names pass
// template.CheckName with the given extensions, sorted in byte order. It
// checks the context before each entry.
//
// # Reading all
//
// [ReadAll] walks the file system and reads every regular file whose
// name ends in one of the extensions, symlinks excluded, after checking
// the context, each with ReadClose. A name with an allowed extension that
// template.CheckName refuses is an error, never skipped.
//
// # Absence
//
// [Absent] tells the errors that mean nothing is at a path: fs.ErrNotExist,
// ENOTDIR, ELOOP, and os.Root's refusal of a path that leads out of it,
// which the os package does not export and which is told by its text. Such
// an error makes a template not found, or leaves it out of a list. [Cause]
// drops the path of a *fs.PathError, which os.Root's FS gives as an
// absolute path for some errors.
//
// # Errors
//
// Read returns errors wrapping template.ErrTemplateNotFound,
// template.ErrRead or template.ErrTooLarge, List errors wrapping
// template.ErrList, and ReadAll errors wrapping template.ErrRead,
// template.ErrTooLarge, template.ErrInvalidName or the context's error.
// Each names the path inside the file system, never an absolute one.
package files
