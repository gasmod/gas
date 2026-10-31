package memory_test

import (
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/template"
	"github.com/gasmod/gas/template/internal/conformance"
	"github.com/gasmod/gas/template/memory"
)

// The memory store keeps the contract of the template package.
func TestStore_Conformance(t *testing.T) {
	t.Parallel()

	conformance.Run(t, conformance.Subject{
		New: func(t *testing.T, seed fstest.MapFS) gas.TemplateProvider {
			t.Helper()

			s, err := memory.NewStore(memory.WithExtensions(conformance.Extensions...))()
			require.NoError(t, err)
			require.NoError(t, s.RegisterFS(t.Context(), seed))

			return s
		},
	})
}

// A store accepts `.html` unless WithExtensions gives it others.
func TestNewStore_DefaultExtension(t *testing.T) {
	t.Parallel()

	s, err := memory.NewStore()()
	require.NoError(t, err)

	require.NoError(t, s.Register(t.Context(), "a.html", nil))

	err = s.Register(t.Context(), "a.txt", nil)
	require.ErrorIs(t, err, template.ErrInvalidName)
	require.EqualError(t, err, "template: invalid name: extension not allowed")
}

// The constructor refuses a bad extension set.
func TestNewStore_InvalidExtensions(t *testing.T) {
	t.Parallel()

	_, err := memory.NewStore(memory.WithExtensions())()
	require.ErrorIs(t, err, template.ErrInvalidExtension)
	require.EqualError(t, err, "template: invalid extension: none given")

	_, err = memory.NewStore(memory.WithExtensions(".html", "txt"))()
	require.ErrorIs(t, err, template.ErrInvalidExtension)
}

// WithExtensions copies its arguments, so changing the caller's slice
// afterwards changes nothing.
func TestWithExtensions_Copies(t *testing.T) {
	t.Parallel()

	exts := []string{".txt"}
	ctor := memory.NewStore(memory.WithExtensions(exts...))

	exts[0] = ".html"

	s, err := ctor()
	require.NoError(t, err)
	require.NoError(t, s.Register(t.Context(), "a.txt", nil))
	require.ErrorIs(t, s.Register(t.Context(), "a.html", nil), template.ErrInvalidName)
}

// Get returns a copy, so changing the content Register was given after the
// call changes nothing, which the old store did not do (survey P1).
func TestStore_RegisterAliasing(t *testing.T) {
	t.Parallel()

	s, err := memory.NewStore()()
	require.NoError(t, err)

	content := []byte("hello")
	require.NoError(t, s.Register(t.Context(), "a.html", content))
	copy(content, "JX")

	b, err := s.Get(t.Context(), "a.html")
	require.NoError(t, err)
	assert.Equal(t, "hello", string(b))
}
