package util

import (
	"errors"
	"testing"
)

// closer records that it was closed and returns err.
type closer struct {
	err    error
	closed bool
}

// Close records the call and returns c.err.
func (c *closer) Close() error {
	c.closed = true
	return c.err
}

// TestCloseWithErr checks that CloseWithErr closes every closer and keeps
// every error.
func TestCloseWithErr(t *testing.T) {
	errA, errB, errOp := errors.New("a"), errors.New("b"), errors.New("op")

	t.Run("no errors", func(t *testing.T) {
		a, b := &closer{}, &closer{}
		if err := CloseWithErr(nil, a, b); err != nil {
			t.Fatalf("CloseWithErr = %v, want nil", err)
		}
		if !a.closed || !b.closed {
			t.Fatal("not every closer was closed")
		}
	})

	t.Run("no closers", func(t *testing.T) {
		if err := CloseWithErr(errOp); !errors.Is(err, errOp) {
			t.Fatalf("CloseWithErr = %v, want %v", err, errOp)
		}
	})

	t.Run("keeps err and every close error", func(t *testing.T) {
		a, ok, b := &closer{err: errA}, &closer{}, &closer{err: errB}
		err := CloseWithErr(errOp, a, ok, b)
		for _, want := range []error{errOp, errA, errB} {
			if !errors.Is(err, want) {
				t.Errorf("CloseWithErr = %v, want it to wrap %v", err, want)
			}
		}
		if !a.closed || !ok.closed || !b.closed {
			t.Fatal("a failing closer stopped the others from closing")
		}
	})

	t.Run("close error without err", func(t *testing.T) {
		if err := CloseWithErr(nil, &closer{err: errA}); !errors.Is(err, errA) {
			t.Fatalf("CloseWithErr = %v, want %v", err, errA)
		}
	})
}
