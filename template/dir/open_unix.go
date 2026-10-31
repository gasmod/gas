//go:build unix

package dir

import (
	"os"
	"syscall"
)

// openFlags opens a file for reading without blocking, so that a FIFO in
// place of a template or a directory opens at once, for the store to
// refuse, instead of waiting for a writer.
const openFlags = os.O_RDONLY | syscall.O_NONBLOCK
