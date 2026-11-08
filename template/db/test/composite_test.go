package db_test

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/template/composite"
	templatefs "github.com/gasmod/gas/template/fs"
)

// A composite store of a db store over an fs store reads a name longer
// than the db store takes from the fs store, so List and Get agree: the
// reviewer's probe on MySQL listed such a name that Get refused with
// ErrInvalidName.
func TestComposite_LongNameFallsThrough(t *testing.T) {
	t.Parallel()

	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()

			long := strings.Repeat("n", 300) + ".html"

			embedded, err := templatefs.NewStore(fstest.MapFS{long: {Data: []byte("long")}})()
			require.NoError(t, err)

			c, err := composite.NewStore(newEnv(t, b).store(t), embedded)()
			require.NoError(t, err)

			names, err := c.List(t.Context())
			require.NoError(t, err)
			assert.Equal(t, []string{long}, names)

			got, err := c.Get(t.Context(), long)
			require.NoError(t, err)
			assert.Equal(t, "long", string(got))
		})
	}
}
