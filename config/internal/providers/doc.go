// Package providers holds FSProvider, the file-reading base shared by
// gas/config's JSONProvider and DotEnvProvider.
//
// FSProvider reads through an fs.FS. NewFSProvider with a nil fs.FS uses
// sysfs.SysFS, which applies the working-directory, symlink and size checks
// described in package sysfs; SetFS replaces it, for example with an
// embed.FS. Errors from OpenFile and ReadFile wrap the file system's error
// and name the file.
package providers
