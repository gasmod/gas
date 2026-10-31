package storage_test

import (
	"math"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/storage"
)

// ParseRange reads one byte range, and Resolve selects its bytes of an
// object, clamping a last byte past the end and a long suffix.
func TestParseRange(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		header, canonical string
		size, off, n      int64
	}{
		{"bytes=0-4", "bytes=0-4", 10, 0, 5},
		{"bytes=5-", "bytes=5-", 10, 5, 5},
		{"bytes=-3", "bytes=-3", 10, 7, 3},
		{"bytes=8-100", "bytes=8-100", 10, 8, 2},
		{"bytes=-100", "bytes=-100", 10, 0, 10},
		{" BYTES=1-1\t", "bytes=1-1", 10, 1, 1},
		{"bytes=007-0008", "bytes=7-8", 10, 7, 2},
		{"bytes=-1", "bytes=-1", 0, 0, 0},
		{"bytes=0-99999999999999999999", "bytes=0-" + strconv.FormatInt(math.MaxInt64, 10), 10, 0, 10},
		{"bytes=99999999999999999998-99999999999999999999", "bytes=9223372036854775807-9223372036854775807", 0, 0, 0},
	} {
		r, err := storage.ParseRange(c.header)
		require.NoError(t, err, c.header)
		assert.Equal(t, c.canonical, r.String(), c.header)

		again, err := storage.ParseRange(r.String())
		require.NoError(t, err)
		assert.Equal(t, r, again)

		if c.header == "bytes=99999999999999999998-99999999999999999999" {
			_, _, err = r.Resolve(c.size)
			require.ErrorIs(t, err, storage.ErrRangeNotSatisfiable)

			continue
		}

		off, n, err := r.Resolve(c.size)
		require.NoError(t, err, c.header)
		assert.Equal(t, c.off, off, c.header)
		assert.Equal(t, c.n, n, c.header)
	}

	var zero storage.ByteRange

	assert.Equal(t, "bytes=0-0", zero.String())
}

// A range that is not one byte range is ErrInvalidRange, and one that
// selects nothing ErrRangeNotSatisfiable.
func TestParseRange_Errors(t *testing.T) {
	t.Parallel()

	for _, h := range []string{
		"", "bytes", "bytes=", "bytes=-", "bytes=a-1", "bytes=1-a", "bytes=+1-2", "bytes=1-2,3-4",
		"bytes=2-1", "items=0-1", "bytes 0-1", "bytes=1", "bytes=- 1", "bytes=99999999999999999999-1",
	} {
		_, err := storage.ParseRange(h)
		require.EqualError(t, err, "invalid range: not one byte range", h)
		require.ErrorIs(t, err, storage.ErrInvalidRange)
	}

	for h, size := range map[string]int64{"bytes=10-": 10, "bytes=-0": 10, "bytes=0-0": 0, "bytes=0-": 0} {
		r, err := storage.ParseRange(h)
		require.NoError(t, err)

		_, _, err = r.Resolve(size)
		require.ErrorIs(t, err, storage.ErrRangeNotSatisfiable, h)
	}
}

// ParseRange is deterministic and never panics; a range it reads writes
// back in a form it reads to the same range, and Resolve selects bytes
// within the object or refuses it.
func FuzzParseRange(f *testing.F) {
	for _, seed := range []string{"bytes=0-4", "bytes=-3", "bytes=5-", "bytes=2-1", "x", "bytes=99999999999999999999-"} {
		f.Add(seed, int64(10))
	}

	f.Fuzz(func(t *testing.T, s string, size int64) {
		if size < 0 {
			size = -(size + 1)
		}

		r, err := storage.ParseRange(s)
		again, againErr := storage.ParseRange(s)
		require.Equal(t, r, again)
		require.Equal(t, err, againErr)

		if err != nil {
			require.EqualError(t, err, "invalid range: not one byte range")

			return
		}

		back, err := storage.ParseRange(r.String())
		require.NoError(t, err)
		require.Equal(t, r, back)

		off, n, err := r.Resolve(size)
		if err != nil {
			require.ErrorIs(t, err, storage.ErrRangeNotSatisfiable)

			return
		}

		require.GreaterOrEqual(t, off, int64(0))
		require.GreaterOrEqual(t, n, int64(0))
		require.LessOrEqual(t, off+n, size)
	})
}
