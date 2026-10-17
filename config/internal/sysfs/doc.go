// Package sysfs is the default file system gas/config's file providers read
// through.
//
// SysFS.Open, through SafeOpen, cleans the path and makes it absolute, then
// refuses it with ErrUnsafeFilePathOutsideDirectory unless it lies inside
// the working directory, with ErrUnsafeFilePathSymlink if its last element is
// a symlink, and with ErrConfigFileTooLarge if the file is over 1 MB. The
// inside-the-directory check is made on the path as written and again after
// resolving symlinks in its directory part, against the working directory
// with its own symlinks resolved, so a symlinked directory that points
// outside the working directory is refused; one that stays inside is
// followed. The checks run before the file is opened, so a path replaced by
// a symlink between the check and the open is not caught.
package sysfs
