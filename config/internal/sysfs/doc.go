// Package sysfs holds [SysFS], the file system the config module's JSON
// and `.env` providers read through by default. It opens a file only when
// it lies inside the working directory, symlinks included, is a regular
// file, and is at most 1 MiB.
//
// # Usage
//
// The zero value is ready to use. Package providers reads through it with
// [fs.ReadFile]:
//
//	data, err := fs.ReadFile(sysfs.SysFS{}, "config.json")
//
// SysFS holds no state, so it is safe for concurrent use. Each Open looks
// up the working directory again, so a change of working directory applies
// to the next Open.
//
// # Names
//
// Open does not follow the [fs.FS] rule that names are slash-separated and
// unrooted. It takes a path in the operating system's form, relative to
// the working directory or absolute, such as `config.json`,
// `./conf/app.json` or `/srv/app/config.json`.
//
// The path is cleaned and made absolute against the working directory,
// and must then be the working directory or lie inside it. `..` elements
// are allowed as long as the result stays inside, so with the working
// directory `/srv/app`, `conf/../app.json` and `../app/app.json` are read,
// while `../app.json`, `/srv/application/app.json` and `/etc/app.json` are
// refused with [ErrUnsafeFilePathOutsideDirectory]. With the working
// directory `/`, every absolute path is inside.
//
// A path that ends in a separator names a directory, so `app.json/` fails
// with the operating system's "not a directory" error rather than opening
// `app.json`.
//
// # Symlinks
//
// The file is opened through an [os.Root] on the working directory, which
// resolves the path one element at a time as it opens it. A symlink is
// followed, in the directory part or as the last element, when its target
// is relative and never steps above the working directory. Unlike the path
// itself, a target is not cleaned first: one that goes above the working
// directory at any point, even to come back inside as `../app/app.json`
// would, is refused. A symlink whose target leads outside, or is absolute,
// is refused with [ErrUnsafeFilePathOutsideDirectory], even when the
// absolute target lies inside the working directory. The working directory
// itself may lie under a symlink.
//
// Because each element is checked as it is opened, a symlink swapped in
// while Open runs cannot lead outside the working directory either.
//
// # Files
//
// The opened file must be a regular file: a directory, FIFO, socket or
// device is refused with [ErrNotRegularFile] before anything is read. The
// file is opened without blocking, so a FIFO is refused rather than
// waiting for a writer.
//
// The file must be at most 1 MiB (1,048,576 bytes), or Open returns
// [ErrConfigFileTooLarge]. Both checks are made on the opened file, so they
// apply to the file that is read. A file that grows after Open passes it
// fails with ErrConfigFileTooLarge from Read once more than 1 MiB has been
// read.
//
// The returned [fs.File] has Stat, Read and Close only; it is not an
// [*os.File].
//
// # Errors
//
// A refusal returns ErrUnsafeFilePathOutsideDirectory, ErrNotRegularFile
// or ErrConfigFileTooLarge as it is, without the file name, to be tested
// with [errors.Is]. The provider that reads the file names it:
//
//	failed to read JSON config file app.json: config file too large
//
// Any other failure to open the file, such as a missing file or a
// permission error, is the [*fs.PathError] from os, which names the path
// relative to the working directory, so errors.Is tells a missing file by
// [fs.ErrNotExist]:
//
//	openat app.json: no such file or directory
//
// # Limits
//
// An absolute path is compared with the working directory as [os.Getwd]
// reports it, without resolving symlinks. When the working directory lies
// under a symlink, an absolute path that spells it the other way is
// refused.
//
// A symlink loop, or a path that needs more than eight symlinks, fails
// with the operating system's "too many levels of symbolic links" error.
//
// os.Root confines symlinks, not the file system. A mount point inside the
// working directory is crossed like any directory, and a hard link there
// to a file elsewhere is read like any regular file.
package sysfs
