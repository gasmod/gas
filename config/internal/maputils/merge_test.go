package maputils_test

import (
	"testing"

	"github.com/gasmod/gas/config/internal/maputils"
	"github.com/stretchr/testify/assert"
)

func TestMerge(t *testing.T) {
	t.Parallel()

	tests := []struct {
		dst      map[string]any
		src      map[string]any
		expected map[string]any
		name     string
	}{
		{
			name: "simple merge with override",
			dst:  map[string]any{"key1": "value1", "key2": "value2"},
			src:  map[string]any{"key2": "newvalue2", "key3": "value3"},
			expected: map[string]any{
				"key1": "value1",
				"key2": "newvalue2", // overridden
				"key3": "value3",
			},
		},
		{
			name: "nested map merge",
			dst: map[string]any{
				"app": map[string]any{
					"name": "oldapp",
					"port": 8080,
				},
			},
			src: map[string]any{
				"app": map[string]any{
					"name":    "newapp", // should override
					"version": "1.0.0",  // should be added
				},
			},
			expected: map[string]any{
				"app": map[string]any{
					"name":    "newapp", // overridden
					"port":    8080,     // preserved
					"version": "1.0.0",  // added
				},
			},
		},
		{
			name: "empty src map",
			dst:  map[string]any{"key1": "value1"},
			src:  map[string]any{},
			expected: map[string]any{
				"key1": "value1",
			},
		},
		{
			name: "empty dst map",
			dst:  map[string]any{},
			src:  map[string]any{"key1": "value1"},
			expected: map[string]any{
				"key1": "value1",
			},
		},
		{
			name: "key normalization",
			dst:  map[string]any{"key1": "value1"},
			src:  map[string]any{"KEY1": "newvalue1", "Key2": "value2"},
			expected: map[string]any{
				"key1": "newvalue1", // overridden with normalized key
				"key2": "value2",    // added with normalized key
			},
		},
		{
			name: "ignore empty keys",
			dst:  map[string]any{"key1": "value1"},
			src:  map[string]any{"": "ignored", "  ": "alsopgnored", "key2": "value2"},
			expected: map[string]any{
				"key1": "value1",
				"key2": "value2",
			},
		},
		{
			name: "override non-map with map",
			dst:  map[string]any{"config": "simple"},
			src: map[string]any{
				"config": map[string]any{
					"nested": "value",
				},
			},
			expected: map[string]any{
				"config": map[string]any{
					"nested": "value",
				},
			},
		},
		{
			name: "override map with non-map",
			dst: map[string]any{
				"config": map[string]any{
					"nested": "value",
				},
			},
			src: map[string]any{"config": "simple"},
			expected: map[string]any{
				"config": "simple",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			maputils.Merge(tt.dst, tt.src)
			assert.Equal(t, tt.expected, tt.dst)
		})
	}
}

func TestMergeWithoutOverride(t *testing.T) {
	t.Parallel()

	tests := []struct {
		dst      map[string]any
		src      map[string]any
		expected map[string]any
		name     string
	}{
		{
			name: "simple merge without override",
			dst:  map[string]any{"key1": "value1", "key2": "value2"},
			src:  map[string]any{"key2": "newvalue2", "key3": "value3"},
			expected: map[string]any{
				"key1": "value1",
				"key2": "value2", // NOT overridden
				"key3": "value3", // added
			},
		},
		{
			name: "nested map merge without override",
			dst: map[string]any{
				"app": map[string]any{
					"name": "oldapp",
					"port": 8080,
				},
			},
			src: map[string]any{
				"app": map[string]any{
					"name":    "newapp", // should NOT override
					"version": "1.0.0",  // should be added
				},
			},
			expected: map[string]any{
				"app": map[string]any{
					"name":    "oldapp", // NOT overridden
					"port":    8080,     // preserved
					"version": "1.0.0",  // added
				},
			},
		},
		{
			name: "empty src map",
			dst:  map[string]any{"key1": "value1"},
			src:  map[string]any{},
			expected: map[string]any{
				"key1": "value1",
			},
		},
		{
			name: "empty dst map",
			dst:  map[string]any{},
			src:  map[string]any{"key1": "value1"},
			expected: map[string]any{
				"key1": "value1",
			},
		},
		{
			name: "key normalization without override",
			dst:  map[string]any{"key1": "value1"},
			src:  map[string]any{"KEY1": "newvalue1", "Key2": "value2"},
			expected: map[string]any{
				"key1": "value1", // NOT overridden (normalized key already exists)
				"key2": "value2", // added with normalized key
			},
		},
		{
			name: "ignore empty keys",
			dst:  map[string]any{"key1": "value1"},
			src:  map[string]any{"": "ignored", "  ": "alsoignored", "key2": "value2"},
			expected: map[string]any{
				"key1": "value1",
				"key2": "value2",
			},
		},
		{
			name: "do not override non-map with map",
			dst:  map[string]any{"config": "simple"},
			src: map[string]any{
				"config": map[string]any{
					"nested": "value",
				},
			},
			expected: map[string]any{
				"config": "simple", // NOT overridden
			},
		},
		{
			name: "do not override map with non-map",
			dst: map[string]any{
				"config": map[string]any{
					"nested": "value",
				},
			},
			src: map[string]any{"config": "simple"},
			expected: map[string]any{
				"config": map[string]any{
					"nested": "value",
				}, // NOT overridden
			},
		},
		{
			name: "complex nested structure without override",
			dst: map[string]any{
				"database": map[string]any{
					"host": "localhost",
					"port": 5432,
					"connection": map[string]any{
						"timeout": 30,
					},
				},
				"app": map[string]any{
					"name": "existing",
				},
			},
			src: map[string]any{
				"database": map[string]any{
					"host": "remotehost", // should NOT override
					"user": "admin",      // should be added
					"connection": map[string]any{
						"timeout":   60,   // should NOT override
						"pool_size": 10,   // should be added
						"ssl":       true, // should be added
					},
				},
				"app": map[string]any{
					"name":    "newapp", // should NOT override
					"version": "1.0.0",  // should be added
				},
				"cache": map[string]any{ // should be added entirely
					"enabled": true,
					"ttl":     300,
				},
			},
			expected: map[string]any{
				"database": map[string]any{
					"host": "localhost", // NOT overridden
					"port": 5432,        // preserved
					"user": "admin",     // added
					"connection": map[string]any{
						"timeout":   30,   // NOT overridden
						"pool_size": 10,   // added
						"ssl":       true, // added
					},
				},
				"app": map[string]any{
					"name":    "existing", // NOT overridden
					"version": "1.0.0",    // added
				},
				"cache": map[string]any{ // added entirely
					"enabled": true,
					"ttl":     300,
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			maputils.MergeWithoutOverride(tt.dst, tt.src)
			assert.Equal(t, tt.expected, tt.dst)
		})
	}
}

func TestMerge_vs_MergeWithoutOverride(t *testing.T) {
	t.Parallel()

	src := map[string]any{
		"key1": "new1",
		"key3": "new3",
		"nested": map[string]any{
			"key2": "new2",
			"key4": "new4",
		},
	}

	// Test Merge (with override)
	dstMerge := map[string]any{
		"key1": "original1",
		"nested": map[string]any{
			"key2": "original2",
		},
	}

	maputils.Merge(dstMerge, src)

	expectedMerge := map[string]any{
		"key1": "new1", // overridden
		"key3": "new3", // added
		"nested": map[string]any{
			"key2": "new2", // overridden
			"key4": "new4", // added
		},
	}
	assert.Equal(t, expectedMerge, dstMerge)

	// Test MergeWithoutOverride
	dstMergeWithoutOverride := map[string]any{
		"key1": "original1",
		"nested": map[string]any{
			"key2": "original2",
		},
	}

	maputils.MergeWithoutOverride(dstMergeWithoutOverride, src)

	expectedMergeWithoutOverride := map[string]any{
		"key1": "original1", // NOT overridden
		"key3": "new3",      // added
		"nested": map[string]any{
			"key2": "original2", // NOT overridden
			"key4": "new4",      // added
		},
	}
	assert.Equal(t, expectedMergeWithoutOverride, dstMergeWithoutOverride)
}
