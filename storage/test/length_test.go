package storage_test

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/storage"
)

// badCount is a body whose Read returns a count below zero.
type badCount struct{}

// Read returns -1.
func (badCount) Read([]byte) (int, error) { return -1, nil }

// tooMany is a body whose Read returns a count above its buffer's length.
type tooMany struct{}

// Read returns one more than len(p).
func (tooMany) Read(p []byte) (int, error) { return len(p) + 1, nil }

// errDisk is the error of a body that fails.
var errDisk = errors.New("disk gone")

// A body of the declared length reads whole; one shorter or longer is
// ErrContentLengthMismatch, sticky, and the bytes that complete a longer
// one are withheld.
func TestCheckLength(t *testing.T) {
	t.Parallel()

	for _, body := range []io.Reader{
		strings.NewReader("abcde"),
		iotest.OneByteReader(strings.NewReader("abcde")),
		iotest.DataErrReader(strings.NewReader("abcde")),
	} {
		r, err := storage.CheckLength(body, new(int64(5)))
		require.NoError(t, err)

		got, err := io.ReadAll(r)
		require.NoError(t, err)
		assert.Equal(t, "abcde", string(got))
	}

	r, err := storage.CheckLength(strings.NewReader("abcdef"), new(int64(5)))
	require.NoError(t, err)

	buf := make([]byte, 5)

	_, err = io.ReadFull(r, buf)
	require.ErrorIs(t, err, storage.ErrContentLengthMismatch)

	_, err = r.Read(buf)
	require.ErrorIs(t, err, storage.ErrContentLengthMismatch)

	n, err := r.Read(nil)
	require.ErrorIs(t, err, storage.ErrContentLengthMismatch)
	assert.Zero(t, n)

	r, err = storage.CheckLength(strings.NewReader("abc"), new(int64(5)))
	require.NoError(t, err)

	_, err = io.ReadAll(r)
	require.EqualError(t, err, "content length mismatch: body has 3 bytes, ContentLength is 5")
}

// A nil body is empty, and a nil length is unknown: the body reads whole,
// with its errors wrapped.
func TestCheckLength_Unknown(t *testing.T) {
	t.Parallel()

	for _, n := range []*int64{nil, new(int64(0))} {
		r, err := storage.CheckLength(nil, n)
		require.NoError(t, err)

		got, err := io.ReadAll(r)
		require.NoError(t, err)
		assert.Empty(t, got)
	}

	r, err := storage.CheckLength(iotest.HalfReader(strings.NewReader("abcdef")), nil)
	require.NoError(t, err)

	got, err := io.ReadAll(r)
	require.NoError(t, err)
	assert.Equal(t, "abcdef", string(got))

	n, err := r.Read(make([]byte, 1))
	assert.Zero(t, n)
	require.ErrorIs(t, err, io.EOF)

	r, err = storage.CheckLength(
		io.MultiReader(strings.NewReader("ab"), iotest.ErrReader(errDisk)),
		nil,
	)
	require.NoError(t, err)

	_, err = io.ReadAll(r)
	require.ErrorIs(t, err, errDisk)
	require.EqualError(t, err, "read body: disk gone")

	r, err = storage.CheckLength(strings.NewReader("ab"), nil)
	require.NoError(t, err)

	n, err = r.Read(nil)
	require.NoError(t, err)
	assert.Zero(t, n)
}

// A body that breaks the io.Reader contract fails the read, with an error
// that wraps no length sentinel.
func TestCheckLength_BrokenBodies(t *testing.T) {
	t.Parallel()

	for _, n := range []*int64{nil, new(int64(5)), new(int64(0))} {
		for _, body := range []io.Reader{badCount{}, tooMany{}} {
			r, err := storage.CheckLength(body, n)
			require.NoError(t, err)

			_, err = io.ReadAll(r)
			require.EqualError(t, err, "read body: Read returned an invalid count")
			require.NotErrorIs(t, err, storage.ErrContentLengthMismatch)
		}

		r, err := storage.CheckLength(zeroReader{}, n)
		require.NoError(t, err)

		_, err = io.ReadAll(r)
		if n != nil && *n > 0 {
			require.ErrorIs(t, err, io.ErrNoProgress)

			continue
		}

		require.EqualError(t, err, "read body: multiple Read calls return no data or error")
	}

	r, err := storage.CheckLength(iotest.ErrReader(errDisk), new(int64(0)))
	require.NoError(t, err)

	_, err = io.ReadAll(r)
	require.ErrorIs(t, err, errDisk)

	r, err = storage.CheckLength(iotest.ErrReader(errDisk), new(int64(3)))
	require.NoError(t, err)

	_, err = io.ReadAll(r)
	require.EqualError(t, err, "read body: disk gone")
}

// The reader CheckLength returns yields the body's bytes up to the
// declared length, and fails with ErrContentLengthMismatch exactly when
// the body is of another length; it never panics.
func FuzzCheckLength(f *testing.F) {
	f.Add([]byte("abcde"), int64(5), uint8(1))
	f.Add([]byte("abc"), int64(5), uint8(3))
	f.Add([]byte(""), int64(0), uint8(0))

	f.Fuzz(func(t *testing.T, body []byte, n int64, chunk uint8) {
		r, err := storage.CheckLength(bytes.NewReader(body), &n)
		if n < 0 {
			require.ErrorIs(t, err, storage.ErrInvalidArgument)

			return
		}

		require.NoError(t, err)

		var got []byte

		buf := make([]byte, int(chunk)+1)

		for {
			k, readErr := r.Read(buf)

			got = append(got, buf[:k]...)

			if readErr == nil {
				continue
			}

			if int64(len(body)) == n {
				require.ErrorIs(t, readErr, io.EOF)
				require.Equal(t, string(body), string(got))
			} else {
				require.ErrorIs(t, readErr, storage.ErrContentLengthMismatch)
				require.LessOrEqual(t, int64(len(got)), n)
			}

			return
		}
	})
}
