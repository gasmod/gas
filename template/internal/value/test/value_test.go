package value_test

import (
	"io/fs"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"

	"github.com/gasmod/gas/template/internal/value"
)

// IsNil tells nil and typed nil values of every nillable kind from the
// rest.
func TestIsNil(t *testing.T) {
	t.Parallel()

	var (
		fsys fs.FS
		ptr  *int
		fn   func()
		ch   chan int
		sl   []int
	)

	for _, v := range []any{nil, fsys, ptr, fn, ch, sl, fstest.MapFS(nil)} {
		assert.True(t, value.IsNil(v), "%T", v)
	}

	for _, v := range []any{0, "", fstest.MapFS{}, new(int), struct{}{}} {
		assert.False(t, value.IsNil(v), "%T", v)
	}
}

// Clone copies, and returns an empty, non-nil slice for nil and empty
// content.
func TestClone(t *testing.T) {
	t.Parallel()

	src := []byte("abc")
	c := value.Clone(src)

	src[0] = 'X'

	assert.Equal(t, "abc", string(c))

	for _, b := range [][]byte{nil, {}} {
		c := value.Clone(b)
		assert.NotNil(t, c)
		assert.Empty(t, c)
	}
}
