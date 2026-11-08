//go:build unix

package vfs

import "golang.org/x/sys/unix"

// openReadFlags are the flags openRegular opens a file with beyond
// O_RDONLY: O_NONBLOCK, so that the open of a FIFO or a device returns at
// once and its fstat refuses it, and O_NOFOLLOW.
const openReadFlags = unix.O_NONBLOCK | unix.O_NOFOLLOW
