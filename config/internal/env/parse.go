// Package env provides utilities for parsing environment variables into nested Go data structures.
// It includes functions to filter unsafe variables, normalize keys, and construct hierarchical
// maps from environment variable names using specified separators.
package env

import (
	"strings"
)

// ParseVariables processes a map of environment variables into a nested map structure.
// It takes the following parameters:
// - vars: map of environment variable key-value pairs to process
// - pre: prefix to filter variables by (variables not matching prefix are excluded)
// - sep: separator used in environment variable names to create nested structure
// Returns a nested map[string]any containing the processed environment variables.
func ParseVariables(vars map[string]string, pre, sep string) map[string]any {
	data := make(map[string]any)

	pre = strings.ToLower(strings.TrimSpace(pre))

	for key, value := range vars {
		key = strings.ToLower(strings.TrimSpace(key))

		// Filter out unsafe variables
		if IsUnsafeVar(key) {
			continue
		}

		if pre != "" {
			if after, ok := strings.CutPrefix(key, pre); ok {
				key = after
			} else {
				continue // Skip if doesn't match prefix
			}
		}

		// Continue using the original keys anyway.
		if sep != "" {
			// Build nested map structure
			BuildNestedMap(data, key, value, sep)
		} else {
			data[key] = value
		}
	}

	return data
}
