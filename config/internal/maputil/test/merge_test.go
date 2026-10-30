package maputil_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/config/internal/maputil"
)

// mergeCase is a merge of src into dst, and the dst it must leave.
type mergeCase struct {
	dst  m
	src  m
	want m
	name string
}

func runMergeCases(t *testing.T, merge func(dst, src m), tests []mergeCase) {
	t.Helper()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			merge(tt.dst, tt.src)
			assert.Equal(t, tt.want, tt.dst)
		})
	}
}

func TestMerge(t *testing.T) {
	t.Parallel()

	runMergeCases(t, maputil.Merge, []mergeCase{
		{
			name: "src wins",
			dst:  m{"a": 1, "b": 2},
			src:  m{"b": 3, "c": 4},
			want: m{"a": 1, "b": 3, "c": 4},
		},
		{
			name: "maps merged key by key",
			dst:  m{"app": m{"name": "old", "port": 8080, "db": m{"host": "h"}}},
			src:  m{"app": m{"name": "new", "db": m{"port": 1}}},
			want: m{"app": m{"name": "new", "port": 8080, "db": m{"host": "h", "port": 1}}},
		},
		{
			name: "map replaces value",
			dst:  m{"a": "x"},
			src:  m{"a": m{"b": 1}},
			want: m{"a": m{"b": 1}},
		},
		{
			name: "value replaces map",
			dst:  m{"a": m{"b": 1}},
			src:  m{"a": "x"},
			want: m{"a": "x"},
		},
		{
			name: "list replaced as a whole",
			dst:  m{"a": []any{1, 2, 3}},
			src:  m{"a": []any{4}},
			want: m{"a": []any{4}},
		},
		{
			name: "nil replaces anything",
			dst:  m{"a": m{"b": 1}, "c": 1},
			src:  m{"a": nil, "c": nil},
			want: m{"a": nil, "c": nil},
		},
		{name: "empty src", dst: m{"a": 1}, src: m{}, want: m{"a": 1}},
		{name: "nil src", dst: m{"a": 1}, src: nil, want: m{"a": 1}},
		{name: "empty dst", dst: m{}, src: m{"a": 1}, want: m{"a": 1}},
		{
			name: "keys not normalized",
			dst:  m{"key": 1},
			src:  m{"KEY": 2, " key": 3},           //nolint:gocritic // a space on purpose
			want: m{"key": 1, "KEY": 2, " key": 3}, //nolint:gocritic // a space on purpose
		},
	})
}

func TestMergeWithoutOverride(t *testing.T) {
	t.Parallel()

	runMergeCases(t, maputil.MergeWithoutOverride, []mergeCase{
		{
			name: "dst wins",
			dst:  m{"a": 1, "b": 2},
			src:  m{"b": 3, "c": 4},
			want: m{"a": 1, "b": 2, "c": 4},
		},
		{
			name: "maps merged key by key",
			dst:  m{"app": m{"name": "old", "db": m{"host": "h"}}},
			src:  m{"app": m{"name": "new", "port": 1, "db": m{"host": "x", "port": 2}}},
			want: m{"app": m{"name": "old", "port": 1, "db": m{"host": "h", "port": 2}}},
		},
		{
			name: "value kept over map",
			dst:  m{"a": "x"},
			src:  m{"a": m{"b": 1}},
			want: m{"a": "x"},
		},
		{
			name: "map kept over value",
			dst:  m{"a": m{"b": 1}},
			src:  m{"a": "x"},
			want: m{"a": m{"b": 1}},
		},
		{
			name: "nil kept",
			dst:  m{"a": nil},
			src:  m{"a": m{"b": 1}},
			want: m{"a": nil},
		},
		{
			name: "whole map added",
			dst:  m{},
			src:  m{"cache": m{"ttl": 300}},
			want: m{"cache": m{"ttl": 300}},
		},
		{name: "empty src", dst: m{"a": 1}, src: m{}, want: m{"a": 1}},
		{
			name: "keys not normalized",
			dst:  m{"key": 1},
			src:  m{"KEY": 2},
			want: m{"key": 1, "KEY": 2},
		},
	})
}

// A nil map[string]any in dst holds nothing, so merging into it leaves src's
// keys there, as merging into an empty map does.
func TestMerge_IntoNilMap(t *testing.T) {
	t.Parallel()

	for name, merge := range map[string]func(dst, src m){
		"Merge":                maputil.Merge,
		"MergeWithoutOverride": maputil.MergeWithoutOverride,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			dst := m{"db": map[string]any(nil)}

			require.NotPanics(t, func() { merge(dst, m{"db": m{"host": "h"}}) })
			assert.Equal(t, m{"db": m{"host": "h"}}, dst)
		})
	}
}
