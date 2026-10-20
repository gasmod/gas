//go:build windows

package vfs

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// IsMissing reports whether err is what an operation gets for a file or
// directory that is not there. On Windows that includes a directory removed
// while handles to it were open: os.Root removes with POSIX semantics where
// the file system allows it (Go's internal/syscall/windows.Deleteat, in
// at_windows.go), which takes the name away at once but leaves the
// directory delete-pending until its last handle closes. Opening or creating
// a file through such a handle fails with STATUS_DELETE_PENDING, which Go
// passes through RtlNtStatusToDosError (NTStatus.Errno in
// syscall_windows.go). Windows documents that as mapping to
// ERROR_ACCESS_DENIED, which is what Windows 11 and Server 2025 report;
// ERROR_DELETE_PENDING is accepted too. A caller must confirm the bucket is
// gone (see Removed) before treating access denied as missing, since it has
// other causes.
func IsMissing(err error) bool {
	return errors.Is(err, os.ErrNotExist) ||
		errors.Is(err, windows.ERROR_ACCESS_DENIED) ||
		errors.Is(err, windows.ERROR_DELETE_PENDING)
}

// isDeletePending reports whether err is what an operation on a name gets
// while another holder is removing it. os.Root.Remove sets a POSIX delete
// disposition on a handle and then closes it (Go's
// internal/syscall/windows.Deleteat), and until that close the name is
// delete-pending: opening or statting it fails with STATUS_DELETE_PENDING,
// which Go reports as ERROR_ACCESS_DENIED. Once the remove returns the name
// is gone, so see retryPending.
func isDeletePending(err error) bool {
	return errors.Is(err, windows.ERROR_ACCESS_DENIED) ||
		errors.Is(err, windows.ERROR_DELETE_PENDING)
}
