//go:build unix

package corelifecycle_test

import (
	"os"
	"syscall"
	"testing"
)

// requireSignals is a no-op where the test can signal its own process.
func requireSignals(*testing.T) {}

// sendSignal delivers sig to the test process, as a shutdown would.
func sendSignal(sig syscall.Signal) error { return syscall.Kill(os.Getpid(), sig) }
