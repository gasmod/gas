package maputil_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/config/internal/maputil"
)

// findNestedCases are calls to FindNestedMap, the map each returns, and
// the map m holds afterwards.
var findNestedCases = []struct {
	m      m
	want   m
	after  m
	name   string
	path   []string
	create bool
}{
	{
		name:  "empty path returns m",
		m:     m{"a": m{"b": 1}},
		path:  nil,
		want:  m{"a": m{"b": 1}},
		after: m{"a": m{"b": 1}},
	},
	{
		name:  "existing map",
		m:     m{"a": m{"b": m{"c": 1}}},
		path:  []string{"a", "b"},
		want:  m{"c": 1},
		after: m{"a": m{"b": m{"c": 1}}},
	},
	{
		name:  "missing key",
		m:     m{"a": m{"b": 1}},
		path:  []string{"a", "x"},
		want:  nil,
		after: m{"a": m{"b": 1}},
	},
	{
		name:   "missing keys created",
		m:      m{"a": m{"b": 1}},
		path:   []string{"a", "x", "y"},
		create: true,
		want:   m{},
		after:  m{"a": m{"b": 1, "x": m{"y": m{}}}},
	},
	{
		name:   "value at the end not replaced",
		m:      m{"a": m{"b": 1}},
		path:   []string{"a", "b"},
		create: true,
		want:   nil,
		after:  m{"a": m{"b": 1}},
	},
	{
		name:   "value on the way not replaced",
		m:      m{"a": "x"},
		path:   []string{"a", "b"},
		create: true,
		want:   nil,
		after:  m{"a": "x"},
	},
	{
		name:  "typed map is not a map[string]any",
		m:     m{"a": map[string]string{"b": "c"}},
		path:  []string{"a"},
		want:  nil,
		after: m{"a": map[string]string{"b": "c"}},
	},
	{
		name:  "keys matched exactly",
		m:     m{"db": m{"host": 1}},
		path:  []string{"DB"},
		want:  nil,
		after: m{"db": m{"host": 1}},
	},
}

func TestFindNestedMap(t *testing.T) {
	t.Parallel()

	for _, tt := range findNestedCases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := maputil.FindNestedMap(tt.m, tt.path, tt.create)
			if tt.want == nil {
				assert.Nil(t, got)
			} else {
				assert.Equal(t, tt.want, got)
			}

			assert.Equal(t, tt.after, tt.m)
		})
	}
}

// The returned map is the one in m, so writing to it writes to m.
func TestFindNestedMap_ReturnsMapInPlace(t *testing.T) {
	t.Parallel()

	data := m{"a": m{}}

	maputil.FindNestedMap(data, []string{"a"}, false)["b"] = 1
	maputil.FindNestedMap(data, []string{"x"}, true)["y"] = 2
	assert.Equal(t, m{"a": m{"b": 1}, "x": m{"y": 2}}, data)
}

// With create, a nil map[string]any on the path is replaced by an empty one,
// as a missing key is.
func TestFindNestedMap_CreateThroughNilMap(t *testing.T) {
	t.Parallel()

	tree := m{"db": map[string]any(nil)}

	var got map[string]any

	require.NotPanics(t, func() { got = maputil.FindNestedMap(tree, []string{"db", "pool"}, true) })
	require.NotNil(t, got)

	got["size"] = 5

	assert.Equal(t, m{"db": m{"pool": m{"size": 5}}}, tree)
}
