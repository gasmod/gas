package memory_test

import (
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/template/memory"
)

// The example in the package documentation's Usage section.
func TestDoc_Usage(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	emails := fstest.MapFS{
		"welcome.html": {Data: []byte("<p>hi</p>")},
		"welcome.txt":  {Data: []byte("hi")},
		"README.md":    {Data: []byte("docs")},
	}

	store, err := memory.NewStore(memory.WithExtensions(".html", ".txt"))()
	require.NoError(t, err)

	err = store.RegisterFS(ctx, emails)
	require.NoError(t, err)

	names, err := store.List(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"welcome.html", "welcome.txt"}, names)

	w := gas.NewWorker(gas.WithSingletonService[gas.TemplateProvider](memory.NewStore()))
	require.NoError(t, w.InitServices(ctx))

	p, err := w.ServiceContainer().Resolve[gas.TemplateProvider]()
	require.NoError(t, err)
	assert.IsType(t, &memory.Store{}, p)
}
