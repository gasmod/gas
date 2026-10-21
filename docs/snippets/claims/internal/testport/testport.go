// Package testport hands claims tests TCP ports to serve on, and waits for a
// server to come up without missing that it failed to.
//
// A port picked by listening on port 0 and closing the listener is in the
// kernel's ephemeral range, so between the close and the server's bind any
// outgoing connection, or another test's port-0 listener, can take it. Free
// picks ports below every common ephemeral range instead, never repeats one
// within a process, and on Unix reserves each with a lock file held for the
// life of the process, so concurrent test binaries do not pick the same port.
// The lock files are left in place: deleting one could let two processes hold
// the same port.
package testport

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

const (
	// first and last bound the ports Free hands out. They lie below the
	// ephemeral ranges of Linux (32768 and up) and macOS (49152 and up).
	first = 20000
	last  = 31999
)

var (
	mu   sync.Mutex
	next int
	held []*os.File // lock files kept open for the life of the process
)

// Free returns a TCP port on 127.0.0.1 that nothing was listening on and that
// no other caller of Free, in this process or in another test process on the
// machine, will be handed while this process runs. The range and the lock
// make the reservation; the only probe is a dial, so Free never binds the
// port itself.
func Free(t testing.TB) int {
	t.Helper()
	mu.Lock()
	defer mu.Unlock()
	if next == 0 {
		// Start each process at a different place, so concurrent test
		// binaries rarely contend for the same lock.
		next = first + os.Getpid()%(last-first+1)
	}
	dir := filepath.Join(os.TempDir(), "gas-docs-claims-ports")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("testport: %v", err)
	}
	for range last - first + 1 {
		port := next
		next++
		if next > last {
			next = first
		}
		f, ok, err := reserve(filepath.Join(dir, fmt.Sprint(port)))
		if err != nil {
			t.Fatalf("testport: reserve %d: %v", port, err)
		}
		if !ok {
			continue
		}
		// Probe by dialing, never by listening: a listener opened here would
		// be inherited by any child process forked meanwhile and keep the
		// port bound until that child execs.
		if Accepting(port) {
			release(f)
			continue // something outside the tests serves on it
		}
		held = append(held, f)
		return port
	}
	t.Fatalf("testport: no free port in %d-%d", first, last)
	return 0
}

// Hold takes the lock for a fixed port, waiting while another test process
// holds it, and releases it when t ends. Tests that must serve on a fixed
// port, such as an example app's 8080, call it so that packages running at
// the same time take turns instead of answering each other's requests. It
// does not check that the port is free; Accepting does that without binding.
func Hold(t testing.TB, port int) {
	t.Helper()
	dir := filepath.Join(os.TempDir(), "gas-docs-claims-ports")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("testport: %v", err)
	}
	f, err := hold(filepath.Join(dir, fmt.Sprint(port)))
	if err != nil {
		t.Fatalf("testport: hold %d: %v", port, err)
	}
	t.Cleanup(func() { release(f) })
}

// Accepting reports whether something accepts TCP connections on port.
func Accepting(port int) bool {
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// ErrNotServing is reported when the server did not come up in time.
var ErrNotServing = errors.New("testport: server did not come up")

// WaitServing polls up until it reports true, for at most timeout. If done
// delivers first, the server has stopped before it served, and WaitServing
// returns that result wrapped, so an early bind failure surfaces with its
// message instead of as a timeout. done is read at most once.
func WaitServing(timeout time.Duration, up func() bool, done <-chan error) error {
	deadline := time.Now().Add(timeout)
	for {
		select {
		case err := <-done:
			return fmt.Errorf("testport: server returned before serving: %w", errOrNil(err))
		default:
		}
		if up() {
			return nil
		}
		if time.Now().After(deadline) {
			return ErrNotServing
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// errNilReturn stands in for a nil result from a server that returned.
var errNilReturn = errors.New("returned nil")

func errOrNil(err error) error {
	if err == nil {
		return errNilReturn
	}
	return err
}
