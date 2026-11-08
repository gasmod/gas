package maputil

// FindNestedMap returns the map[string]any at path in m, following one key
// per element of path. An empty path returns m itself.
//
// A missing key returns nil, unless create is set: then FindNestedMap adds
// an empty map at that key and at every key after it, so m must not be nil.
// A nil map[string]any on the path holds nothing, so it counts as missing:
// create replaces it with an empty map. A key that holds anything else but
// a map[string]any returns nil, with or without create, and is left as it
// is. FindNestedMap matches keys exactly; it does not normalize path.
func FindNestedMap(m map[string]any, path []string, create bool) map[string]any {
	current := m

	for _, key := range path {
		value, ok := current[key]
		next, isMap := value.(map[string]any)

		switch {
		case ok && !isMap:
			return nil
		case next != nil:
			current = next
		case !create:
			return nil
		default:
			// The key is missing, or holds a nil map.
			next = make(map[string]any)
			current[key] = next
			current = next
		}
	}

	return current
}
