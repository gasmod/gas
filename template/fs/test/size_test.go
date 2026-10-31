package fs_test

import (
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/template"
	templatefs "github.com/gasmod/gas/template/fs"
)

// Get refuses a file over the maximum size with ErrTooLarge and reads one
// of the maximum size; a maximum that is not positive is refused by the
// constructor.
func TestStore_MaxSize(t *testing.T) {
	t.Parallel()

	fsys := fstest.MapFS{"big.html": {Data: []byte("abcd")}, "ok.html": {Data: []byte("abc")}}

	s, err := templatefs.NewStore(fsys, templatefs.WithMaxSize(3))()
	require.NoError(t, err)

	_, err = s.Get(t.Context(), "big.html")
	require.ErrorIs(t, err, template.ErrTooLarge)
	require.EqualError(t, err, `template: too large: "big.html": over 3 bytes`)

	b, err := s.Get(t.Context(), "ok.html")
	require.NoError(t, err)
	assert.Equal(t, "abc", string(b))

	_, err = templatefs.NewStore(fsys, templatefs.WithMaxSize(-1))()
	require.ErrorIs(t, err, template.ErrInvalidMaxSize)
}
