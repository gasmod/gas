//go:build !unix

package coreworkers_test

import (
	"errors"
	"syscall"
	"testing"
)

// requireSignals skips tests that stop Run by signaling the test process,
// which this platform cannot do.
func requireSignals(t *testing.T) {
	t.Helper()
	t.Skip("needs to send SIGINT or SIGTERM to the test process")
}

// sendSignal is unsupported here.
func sendSignal(syscall.Signal) error {
	return errors.New("signals are not supported on this platform")
}
