package reflection_test

import (
	"testing"

	"github.com/gasmod/gas/config/internal/reflection"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClone_MapWithNilValue(t *testing.T) {
	t.Parallel()

	src := map[string]any{"host": "localhost", "port": nil}

	got := reflection.Clone(src)

	require.Contains(t, got, "port", "key with a nil value must survive cloning")
	assert.Nil(t, got["port"])
	assert.Equal(t, src, got)
}

func TestClone_NestedMapWithNilValue(t *testing.T) {
	t.Parallel()

	src := map[string]any{"db": map[string]any{"host": "localhost", "port": nil}}

	got := reflection.Clone(src)

	inner, ok := got["db"].(map[string]any)
	require.True(t, ok)
	require.Contains(t, inner, "port", "nested key with a nil value must survive cloning")
	assert.Equal(t, src, got)
}

func TestClone_SliceWithNilElement(t *testing.T) {
	t.Parallel()

	src := []any{"a", nil, "b"}

	require.NotPanics(t, func() {
		got := reflection.Clone(src)
		assert.Equal(t, src, got)
	})
}

func TestClone_MapWithNilSliceAndMapValues(t *testing.T) {
	t.Parallel()

	var (
		nilSlice []string
		nilMap   map[string]string
	)

	src := map[string]any{"slice": nilSlice, "map": nilMap}

	got := reflection.Clone(src)

	require.Contains(t, got, "slice", "key with a typed-nil slice must survive cloning")
	require.Contains(t, got, "map", "key with a typed-nil map must survive cloning")
}
