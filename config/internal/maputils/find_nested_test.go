package maputils_test

import (
	"testing"

	"github.com/gasmod/gas/config/internal/maputils"
	"github.com/stretchr/testify/assert"
)

func TestFindNestedMap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		m         map[string]any
		expected  map[string]any
		name      string
		pathParts []string
		create    bool
	}{
		{
			name:      "empty path and map without create",
			m:         map[string]any{},
			pathParts: []string{},
			create:    false,
			expected:  map[string]any{},
		},
		{
			name: "empty path with non-empty map without create",
			m: map[string]any{
				"key1": map[string]any{"key2": "value"},
			},
			pathParts: []string{},
			create:    false,
			expected: map[string]any{
				"key1": map[string]any{"key2": "value"},
			},
		},
		{
			name: "existing nested map",
			m: map[string]any{
				"key1": map[string]any{
					"key2": map[string]any{
						"key3": "value",
					},
				},
			},
			pathParts: []string{"key1", "key2"},
			create:    false,
			expected: map[string]any{
				"key3": "value",
			},
		},
		{
			name: "non-existent path without create",
			m: map[string]any{
				"key1": map[string]any{
					"key2": "value",
				},
			},
			pathParts: []string{"key1", "key3"},
			create:    false,
			expected:  nil,
		},
		{
			name: "non-existent path with create",
			m: map[string]any{
				"key1": map[string]any{
					"key2": "value",
				},
			},
			pathParts: []string{"key1", "key3", "key4"},
			create:    true,
			expected:  map[string]any{},
		},
		{
			name: "existing path overrides non-map",
			m: map[string]any{
				"key1": map[string]any{
					"key2": "value",
				},
			},
			pathParts: []string{"key1", "key2"},
			create:    true,
			expected:  nil,
		},
		{
			name:      "create new nested structure",
			m:         map[string]any{},
			pathParts: []string{"key1", "key2", "key3"},
			create:    true,
			expected:  map[string]any{},
		},
		{
			name: "return nil for path with non-map element",
			m: map[string]any{
				"key1": "value",
			},
			pathParts: []string{"key1", "key2"},
			create:    false,
			expected:  nil,
		},
		{
			name: "create new map for path with non-map parent",
			m: map[string]any{
				"key1": "value",
			},
			pathParts: []string{"key1", "key2"},
			create:    true,
			expected:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := maputils.FindNestedMap(tt.m, tt.pathParts, tt.create)
			assert.Equal(t, tt.expected, result,
				"(%s): FindNestedMap(%v, %v, %v) = %v, expected %v",
				tt.name,
				tt.m,
				tt.pathParts,
				tt.create,
				result,
				tt.expected,
			)
		})
	}
}
