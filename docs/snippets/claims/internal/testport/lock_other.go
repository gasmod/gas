//go:build !unix || solaris || aix

package testport

import "os"

// reserve takes no cross-process lock where syscall.Flock is missing; ports
// are still unique within the process and below the ephemeral range.
func reserve(string) (*os.File, bool, error) { return nil, true, nil }

func release(*os.File) {}

// hold takes no cross-process lock where syscall.Flock is missing.
func hold(string) (*os.File, error) { return nil, nil }
