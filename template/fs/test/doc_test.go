package fs_test

import (
	"context"
	"io/fs"
	"os"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	templatefs "github.com/gasmod/gas/template/fs"
	"github.com/gasmod/gas/template/internal/conformance"
)

// The examples in the package documentation's Usage and Symlinks
// sections.
func TestDoc_Usage(t *testing.T) {
	t.Parallel()

	embedded := fstest.MapFS{"templates/pages/home.html": {Data: []byte("<h1>home</h1>")}}

	sub, err := fs.Sub(embedded, "templates")
	require.NoError(t, err)

	store, err := templatefs.NewStore(sub)()
	require.NoError(t, err)

	b, err := store.Get(t.Context(), "pages/home.html")
	require.NoError(t, err)
	assert.Equal(t, "<h1>home</h1>", string(b))

	w := gas.NewWorker(gas.WithSingletonService[gas.TemplateProvider](templatefs.NewStore(sub)))
	require.NoError(t, w.InitServices(t.Context()))

	dir := t.TempDir()
	conformance.WriteFiles(t, dir, fstest.MapFS{"pages/home.html": {Data: []byte("x")}})

	root, err := os.OpenRoot(dir)
	require.NoError(t, err)

	defer func() { require.NoError(t, root.Close()) }()

	store, err = templatefs.NewStore(root.FS())()
	require.NoError(t, err)

	_, err = store.Get(context.Background(), "pages/home.html")
	require.NoError(t, err)
}
