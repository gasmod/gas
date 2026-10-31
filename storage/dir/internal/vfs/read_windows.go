//go:build windows

package vfs

// openReadFlags are the flags openRegular opens a file with beyond
// O_RDONLY: none, as Windows has no O_NONBLOCK or O_NOFOLLOW open flag.
// The fstat and the Lstat of openRegular still refuse what is not a
// regular file at the name itself.
const openReadFlags = 0
