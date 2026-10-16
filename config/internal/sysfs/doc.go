// Package sysfs is the default file system gas/config's file providers read
// through.
//
// SysFS.Open, through SafeOpen, cleans the path and makes it absolute, then
// refuses it with ErrUnsafeFilePathOutsideDirectory unless it lies inside
// the working directory, with ErrUnsafeFilePathSymlink if its last element is
// a symlink, and with ErrConfigFileTooLarge if the file is over 1 MB. The
// inside-the-directory check is made on the path as written: a symlinked
// directory along the path is followed, so a file outside the working
// directory can be read through one (KI-027 in KNOWN_ISSUES.md).
package sysfs
