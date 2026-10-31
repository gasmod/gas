package dir

import (
	"bytes"
	"os"
	"strings"
	"syscall"
	"unsafe"
)

// maxPathLen is MAXPATHLEN, the size of F_GETPATH's buffer.
const maxPathLen = 1024

// exactPath reports whether macOS gives the open file f's path, as the
// file system stores it, ending in name, which confirms name's case
// without reading a directory. False means only that it did not confirm
// it: the caller then reads the directories, which decide.
func exactPath(f *os.File, name string) bool {
	conn, err := f.SyscallConn()
	if err != nil {
		return false
	}

	buf := make([]byte, maxPathLen)

	var errno syscall.Errno

	err = conn.Control(func(fd uintptr) {
		// F_GETPATH writes the path into buf, at most MAXPATHLEN bytes.
		arg := uintptr(unsafe.Pointer(&buf[0])) //nolint:gosec // buf is MAXPATHLEN bytes.

		_, _, errno = syscall.Syscall(syscall.SYS_FCNTL, fd, syscall.F_GETPATH, arg)
	})
	if err != nil || errno != 0 {
		return false
	}

	path, _, ok := bytes.Cut(buf, []byte{0})
	if !ok {
		return false
	}

	return strings.HasSuffix(string(path), "/"+name)
}
