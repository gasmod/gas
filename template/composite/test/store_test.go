package composite_test

import (
	"context"
	"errors"
	"maps"
	"slices"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/template"
	"github.com/gasmod/gas/template/composite"
	"github.com/gasmod/gas/template/dir"
	templatefs "github.com/gasmod/gas/template/fs"
	"github.com/gasmod/gas/template/internal/conformance"
	"github.com/gasmod/gas/template/memory"
	"github.com/gasmod/gas/template/templatetest"
)

// errDown is a provider's failure that is not a missing template.
var errDown = errors.New("template: database down")

// newMemory returns a memory store with the conformance extensions.
func newMemory(t *testing.T) *memory.Store {
	t.Helper()

	s, err := memory.NewStore(memory.WithExtensions(conformance.Extensions...))()
	require.NoError(t, err)

	return s
}

// newComposite returns a composite store over writable and readers.
func newComposite(
	t *testing.T,
	writable gas.TemplateProvider,
	readers ...gas.TemplateProvider,
) *composite.Store {
	t.Helper()

	s, err := composite.NewStore(writable, readers...)()
	require.NoError(t, err)

	return s
}

// The composite store keeps the contract of the template package, with
// the seed split between a writable memory store and a read-only fs
// store under it.
func TestStore_Conformance(t *testing.T) {
	t.Parallel()

	conformance.Run(t, conformance.Subject{
		New: func(t *testing.T, seed fstest.MapFS) gas.TemplateProvider {
			t.Helper()

			upper, lower := fstest.MapFS{}, fstest.MapFS{}

			for i, name := range slices.Sorted(maps.Keys(seed)) {
				if i%2 == 0 {
					upper[name] = seed[name]
				} else {
					lower[name] = seed[name]
				}
			}

			w := newMemory(t)
			require.NoError(t, w.RegisterFS(t.Context(), upper))

			r, err := templatefs.NewStore(
				lower,
				templatefs.WithExtensions(conformance.Extensions...),
			)()
			require.NoError(t, err)

			return newComposite(t, w, r)
		},
	})
}

// The constructor refuses a nil provider, a typed nil included, and
// names its position.
func TestNewStore_NilProvider(t *testing.T) {
	t.Parallel()

	m := newMemory(t)

	_, err := composite.NewStore(nil, m)()
	require.ErrorIs(t, err, composite.ErrNilProvider)
	require.EqualError(t, err, "template: provider is nil: the writable provider")

	_, err = composite.NewStore(m, m, (*memory.Store)(nil))()
	require.ErrorIs(t, err, composite.ErrNilProvider)
	require.EqualError(t, err, "template: provider is nil: reader 2")
}

// Get reads the writable provider first, then each reader in order.
func TestStore_GetOrder(t *testing.T) {
	t.Parallel()

	w, r1, r2 := newMemory(t), newMemory(t), newMemory(t)
	ctx := t.Context()

	require.NoError(t, r1.Register(ctx, "a.html", []byte("r1")))
	require.NoError(t, r2.Register(ctx, "a.html", []byte("r2")))
	require.NoError(t, r2.Register(ctx, "b.html", []byte("r2")))

	s := newComposite(t, w, r1, r2)

	b, err := s.Get(ctx, "a.html")
	require.NoError(t, err)
	assert.Equal(t, "r1", string(b))

	b, err = s.Get(ctx, "b.html")
	require.NoError(t, err)
	assert.Equal(t, "r2", string(b))

	require.NoError(t, s.Register(ctx, "a.html", []byte("w")))

	b, err = s.Get(ctx, "a.html")
	require.NoError(t, err)
	assert.Equal(t, "w", string(b))
}

// A provider's error other than not found is returned unchanged and stops
// Get, so a closed writable store is not hidden by a fallback, as it was
// (survey P10).
func TestStore_GetDoesNotHideErrors(t *testing.T) {
	t.Parallel()

	d, err := dir.NewStore(t.TempDir())()
	require.NoError(t, err)
	require.NoError(t, d.Init(t.Context()))
	require.NoError(t, d.Close(t.Context()))

	fallback := newMemory(t)
	require.NoError(t, fallback.Register(t.Context(), "a.html", []byte("fallback")))

	_, err = newComposite(t, d, fallback).Get(t.Context(), "a.html")
	require.ErrorIs(t, err, template.ErrClosed)
	require.EqualError(t, err, "template: store closed")

	down := &templatetest.MockTemplate{
		GetFn: func(context.Context, string) ([]byte, error) { return nil, errDown },
	}

	_, err = newComposite(t, newMemory(t), down, fallback).Get(t.Context(), "a.html")
	require.ErrorIs(t, err, errDown)

	_, err = newComposite(t, newMemory(t), fallback).Get(t.Context(), "missing.html")
	require.ErrorIs(t, err, template.ErrTemplateNotFound)
	require.EqualError(t, err, `template: not found: "missing.html"`)
}

// List merges every provider's names once each in byte order, and fails
// when any provider fails, where the old store returned the others' names
// (survey P10).
func TestStore_List(t *testing.T) {
	t.Parallel()

	w, r := newMemory(t), newMemory(t)
	ctx := t.Context()

	require.NoError(t, w.Register(ctx, "b.html", nil))
	require.NoError(t, r.Register(ctx, "a.html", nil))
	require.NoError(t, r.Register(ctx, "b.html", nil))

	names, err := newComposite(t, w, r).List(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"a.html", "b.html"}, names)

	down := &templatetest.MockTemplate{
		ListFn: func(context.Context) ([]string, error) { return nil, errDown },
	}

	_, err = newComposite(t, w, r, down).List(ctx)
	require.ErrorIs(t, err, errDown)
}

// Register and RegisterFS go to the writable provider and return its
// error unchanged.
func TestStore_WritesGoToWritable(t *testing.T) {
	t.Parallel()

	ro, err := templatefs.NewStore(fstest.MapFS{})()
	require.NoError(t, err)

	s := newComposite(t, ro, newMemory(t))

	require.ErrorIs(t, s.Register(t.Context(), "a.html", nil), template.ErrReadOnly)
	require.ErrorIs(t, s.RegisterFS(t.Context(), fstest.MapFS{}), template.ErrReadOnly)
}

// A name a provider refuses goes on to the next provider, as a missing
// one does, so providers of different extensions mix and List and Get
// agree: a memory store of `.html` over an fs store of `.html` and `.txt`
// lists `x.txt` and reads it, as the reviewer's probe found it did not.
// A name no provider has is the last not-found error, and a name every
// provider refuses the first provider's error.
func TestStore_InvalidNameFallsThrough(t *testing.T) {
	t.Parallel()

	html, err := memory.NewStore()()
	require.NoError(t, err)

	r, err := templatefs.NewStore(
		fstest.MapFS{"x.txt": {Data: []byte("x")}},
		templatefs.WithExtensions(".html", ".txt"),
	)()
	require.NoError(t, err)

	s := newComposite(t, html, r)

	names, err := s.List(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []string{"x.txt"}, names)

	for _, n := range names {
		b, getErr := s.Get(t.Context(), n)
		require.NoError(t, getErr, n)
		assert.Equal(t, "x", string(b))
	}

	_, err = s.Get(t.Context(), "missing.txt")
	require.ErrorIs(t, err, template.ErrTemplateNotFound)
	require.EqualError(t, err, `template: not found: "missing.txt"`)

	_, err = s.Get(t.Context(), "a.env")
	require.ErrorIs(t, err, template.ErrInvalidName)
	require.EqualError(t, err, "template: invalid name: extension not allowed")

	_, err = s.Get(t.Context(), "../a.html")
	require.ErrorIs(t, err, template.ErrInvalidName)
	require.EqualError(t, err, "template: invalid name: not a clean slash path")
}
