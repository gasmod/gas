//go:build unix

package dir_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/template"
	"github.com/gasmod/gas/template/dir"
	"github.com/gasmod/gas/template/internal/conformance"
)

// A FIFO is neither listed nor read, so List and Get agree, and opening
// one never waits for a writer.
func TestStore_FIFO(t *testing.T) {
	t.Parallel()

	s, d := open(t, fstest.MapFS{"page.html": {Data: []byte("page")}})
	require.NoError(t, syscall.Mkfifo(filepath.Join(d, "pipe.html"), 0o600))
	require.NoError(t, os.Symlink("pipe.html", filepath.Join(d, "topipe.html")))

	names, err := s.List(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []string{"page.html"}, names)

	for _, n := range []string{"pipe.html", "topipe.html"} {
		_, err := s.Get(t.Context(), n)
		require.ErrorIs(t, err, template.ErrTemplateNotFound, n)
	}
}

// A FIFO swapped in for a template, or for a directory, while Get and List
// look it up never blocks them, nor Close after them: each Get returns the
// content, ErrTemplateNotFound, or ErrRead for a file that is not the
// regular file it looked up, and List a list or ErrList. Before, a Get
// that opened the FIFO waited for a writer for good, and Close behind it.
func TestStore_FIFOSwap(t *testing.T) {
	t.Parallel()

	s, d := open(t, fstest.MapFS{"t.html": {Data: []byte("R")}, "sub/t.html": {Data: []byte("R")}})
	conformance.WriteFiles(t, d, fstest.MapFS{"reg.tmp": {Data: []byte("R")}})
	require.NoError(t, syscall.Mkfifo(filepath.Join(d, "fifo.tmp"), 0o600))

	stop := make(chan struct{})
	swapped := make(chan error, 1)

	go func() { swapped <- swapLoop(d, stop) }()

	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		readOnce(t, s)
	}

	close(stop)
	require.NoError(t, <-swapped)

	closed := make(chan error, 1)

	go func() { closed <- s.Close(context.Background()) }()

	select {
	case err := <-closed:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Close blocked")
	}
}

// readOnce runs a Get and a List at once and fails the test when either
// blocks or returns an error other than those a swap explains.
func readOnce(t *testing.T, s *dir.Store) {
	t.Helper()

	done := make(chan error, 2)

	go func() {
		_, err := s.Get(context.Background(), "t.html")
		done <- err
	}()

	go func() {
		_, err := s.List(context.Background())
		done <- err
	}()

	for range 2 {
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, template.ErrTemplateNotFound) &&
				!errors.Is(err, template.ErrRead) && !errors.Is(err, template.ErrList) {
				t.Fatalf("unexpected error: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("a read blocked on a FIFO")
		}
	}
}

// swapLoop puts the FIFO `fifo.tmp` of d in place of `t.html` and of the
// directory `sub` and back, by rename, until stop closes.
func swapLoop(d string, stop <-chan struct{}) error {
	for i := 0; ; i++ {
		select {
		case <-stop:
			return nil
		default:
		}

		err := errors.Join(swapFile(d, i%2 == 1), swapDir(d, i))
		if err != nil {
			return err
		}
	}
}

// swapFile puts a hard link of d's `fifo.tmp`, or of its `reg.tmp`, in
// place of its `t.html`, by rename.
func swapFile(d string, toFIFO bool) error {
	src, tmp := filepath.Join(d, "reg.tmp"), filepath.Join(d, "x.tmp")
	if toFIFO {
		src = filepath.Join(d, "fifo.tmp")
	}

	err := os.Remove(tmp)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	return errors.Join(os.Link(src, tmp), os.Rename(tmp, filepath.Join(d, "t.html")))
}

// swapDir, on odd turns, parks d's directory `sub` and puts a hard link of
// its `fifo.tmp` in its place, and on even turns after the first puts the
// directory back.
func swapDir(d string, turn int) error {
	sub, parked := filepath.Join(d, "sub"), filepath.Join(d, "dir.tmp")

	switch {
	case turn%2 == 1:
		return errors.Join(os.Rename(sub, parked), os.Link(filepath.Join(d, "fifo.tmp"), sub))
	case turn > 0:
		return errors.Join(os.Remove(sub), os.Rename(parked, sub))
	default:
		return nil
	}
}
