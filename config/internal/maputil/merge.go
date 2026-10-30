package maputil

// Merge merges src into dst, src winning, as described in the package
// documentation. Where both hold a map[string]any under a key, the two are
// merged the same way; otherwise src's value replaces dst's. A nil
// map[string]any under a key in dst holds nothing, so src's map takes its
// place.
//
// Merge does not normalize keys and does not copy: dst takes src's values,
// and a later merge into dst can modify a map that came from src. Pass a
// map that is not used afterwards, such as the result of [NormalizeKeys].
func Merge(dst, src map[string]any) {
	for key, value := range src {
		if from, ok := value.(map[string]any); ok {
			if into, ok := dst[key].(map[string]any); ok && into != nil {
				Merge(into, from)

				continue
			}
		}

		dst[key] = value
	}
}

// MergeWithoutOverride merges src into dst, dst winning: it adds the keys
// dst does not hold, and where both hold a map[string]any under a key, it
// merges the two the same way. A key dst holds keeps its value, even nil,
// except a nil map[string]any, which holds nothing: src's map takes its
// place.
//
// Like [Merge], it does not normalize keys and does not copy.
func MergeWithoutOverride(dst, src map[string]any) {
	for key, value := range src {
		existing, ok := dst[key]
		if !ok {
			dst[key] = value

			continue
		}

		into, ok := existing.(map[string]any)
		if !ok {
			continue
		}

		if from, ok := value.(map[string]any); ok {
			if into == nil {
				dst[key] = from

				continue
			}

			MergeWithoutOverride(into, from)
		}
	}
}
