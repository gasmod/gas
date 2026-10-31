//go:build !darwin

package dir

import "os"

// exactPath reports false: only macOS gives the path of an open file as
// the file system stores it, so elsewhere the caller reads the
// directories to check name's case.
func exactPath(*os.File, string) bool { return false }
