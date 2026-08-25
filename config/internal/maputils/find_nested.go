package maputils

// FindNestedMap traverses a nested map based on the provided pathParts and returns
// the target map at the end of the path.
// If the path does not exist and create is true, intermediate maps are created.
// Returns nil if not found or not a map.
func FindNestedMap(m map[string]any, pathParts []string, create bool) map[string]any {
	var find func(map[string]any, []string, int) map[string]any

	find = func(m map[string]any, parts []string, index int) map[string]any {
		if index >= len(parts) {
			return m
		}

		part := parts[index]
		if val, ok := m[part]; ok {
			if index == len(parts)-1 {
				if fm, ok2 := val.(map[string]any); ok2 {
					return fm // final map
				}

				return nil // final part is not a map
			}

			if nestedM, ok2 := val.(map[string]any); ok2 {
				return find(nestedM, parts, index+1)
			}

			return nil // expected map but isn't
		}

		if create {
			m[part] = make(map[string]any)

			return find(m[part].(map[string]any), parts, index+1)
		}

		return nil
	}

	return find(m, pathParts, 0)
}
