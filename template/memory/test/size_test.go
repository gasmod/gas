package memory_test

import (
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/template"
	"github.com/gasmod/gas/template/memory"
)

// Register and RegisterFS refuse content over the maximum size with
// ErrTooLarge and store nothing; content of the maximum size is stored;
// a maximum that is not positive is refused by the constructor.
func TestStore_MaxSize(t *testing.T) {
	t.Parallel()

	s, err := memory.NewStore(memory.WithMaxSize(3))()
	require.NoError(t, err)

	ctx := t.Context()

	err = s.Register(ctx, "a.html", []byte("abcd"))
	require.ErrorIs(t, err, template.ErrTooLarge)
	require.EqualError(t, err, `template: too large: "a.html": over 3 bytes`)
	require.NoError(t, s.Register(ctx, "b.html", []byte("abc")))

	err = s.RegisterFS(ctx, fstest.MapFS{
		"c.html": {Data: []byte("c")},
		"d.html": {Data: []byte("abcd")},
	})
	require.ErrorIs(t, err, template.ErrTooLarge)

	_, err = s.Get(ctx, "c.html")
	require.ErrorIs(t, err, template.ErrTemplateNotFound)

	_, err = memory.NewStore(memory.WithMaxSize(0))()
	require.ErrorIs(t, err, template.ErrInvalidMaxSize)
}
