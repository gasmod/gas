package composite_test

import (
	"maps"
	"slices"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/template/composite"
	"github.com/gasmod/gas/template/dir"
	templatefs "github.com/gasmod/gas/template/fs"
	"github.com/gasmod/gas/template/internal/conformance"
	"github.com/gasmod/gas/template/memory"
)

// The examples in the package documentation's Usage section.
func TestDoc_Usage(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	sub := fstest.MapFS{"a.html": {Data: []byte("embedded")}, "b.html": {Data: []byte("embedded")}}

	overrides, err := memory.NewStore()()
	require.NoError(t, err)
	require.NoError(t, overrides.Register(ctx, "a.html", []byte("override")))

	embedded, err := templatefs.NewStore(sub)()
	require.NoError(t, err)

	store, err := composite.NewStore(overrides, embedded)()
	require.NoError(t, err)

	b, err := store.Get(ctx, "a.html")
	require.NoError(t, err)
	assert.Equal(t, "override", string(b))

	b, err = store.Get(ctx, "b.html")
	require.NoError(t, err)
	assert.Equal(t, "embedded", string(b))

	d := t.TempDir()
	conformance.WriteFiles(t, d, fstest.MapFS{"c.html": {Data: []byte("disk")}})

	w := gas.NewWorker(
		gas.WithSingletonService[*dir.Store](dir.NewStore(d)),
		gas.WithSingletonService[gas.TemplateProvider](
			func(d *dir.Store) (*composite.Store, error) {
				return composite.NewStore(d, embedded)()
			},
		),
	)
	require.NoError(t, w.Start(ctx))

	p, err := w.ServiceContainer().Resolve[gas.TemplateProvider]()
	require.NoError(t, err)

	names, err := p.List(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"a.html", "b.html", "c.html"}, names)
	require.NoError(t, w.Shutdown(ctx))
}

// The error messages the package documentation lists are the ones the
// store returns, and it lists every message this test makes.
func TestDoc_Errors(t *testing.T) {
	t.Parallel()

	m := newMemory(t)
	cases := map[string]func() error{
		"template: provider is nil: the writable provider": func() error {
			_, err := composite.NewStore(nil)()

			return err
		},
		"template: provider is nil: reader 2": func() error {
			_, err := composite.NewStore(m, m, nil)()

			return err
		},
	}

	documented := conformance.DocLines(t, "../doc.go", "template: ")
	assert.ElementsMatch(t, documented, slices.Collect(maps.Keys(cases)))

	for msg, produce := range cases {
		assert.EqualError(t, produce(), msg)
	}
}
