//go:build windows

package vfs

import "os"

// syncDirFile does nothing: Windows syncs no directory handle, and NTFS
// journals its directory changes.
func syncDirFile(*os.Root, string) error { return nil }
