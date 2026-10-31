//go:build !unix

package dir

import "os"

// openFlags opens a file for reading. Only Unix systems have FIFOs that
// block an open.
const openFlags = os.O_RDONLY
