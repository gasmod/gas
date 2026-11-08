package dir_test

import (
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/template"
	"github.com/gasmod/gas/template/dir"
	"github.com/gasmod/gas/template/internal/conformance"
)

// KI-033: the dir store declared Close() error and no Name or Init, so the
// container refused to build it as gas.TemplateProvider. It now
// implements gas.Service: the container builds it, Init opens the
// directory, and shutdown closes it.
func TestKI033_DirStoreRegistersAsTemplateProvider(t *testing.T) {
	t.Parallel()

	d := t.TempDir()
	conformance.WriteFiles(t, d, fstest.MapFS{"a.html": {Data: []byte("a")}})

	w := gas.NewWorker(gas.WithSingletonService[gas.TemplateProvider](dir.NewStore(d)))
	require.NoError(t, w.Start(t.Context()))

	p, err := w.ServiceContainer().Resolve[gas.TemplateProvider]()
	require.NoError(t, err)

	b, err := p.Get(t.Context(), "a.html")
	require.NoError(t, err)
	assert.Equal(t, "a", string(b))

	require.NoError(t, w.Shutdown(t.Context()))

	_, err = p.Get(t.Context(), "a.html")
	require.ErrorIs(t, err, template.ErrClosed)
}
