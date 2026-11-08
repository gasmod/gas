package maputil

import (
	"cmp"
	"reflect"
	"slices"
	"strings"

	"github.com/gasmod/gas/config/internal/reflection"
)

// NormalizeKeys returns a copy of m with every key trimmed and lowercased,
// at every depth, as described in the package documentation. The copy
// shares no maps or slices with m, except those inside a struct or array
// value, which is copied the way Go copies it, and m is not modified. A nil
// m returns an empty, non-nil map.
//
// Two keys of one map that become the same key return a nil map and an
// error wrapping [ErrKeyConflict] that names both keys and the path to
// their map. No error includes a value.
func NormalizeKeys(m map[string]any) (map[string]any, error) {
	return normalizeMap(reflect.ValueOf(m))
}

func normalizeMap(m reflect.Value) (map[string]any, error) {
	type entry struct {
		value reflect.Value
		key   string
	}

	entries := make([]entry, 0, m.Len())
	for iter := m.MapRange(); iter.Next(); {
		entries = append(entries, entry{key: keyString(iter.Key()), value: iter.Value()})
	}

	// Sorted keys report the same conflict on every call.
	slices.SortFunc(entries, func(a, b entry) int { return cmp.Compare(a.key, b.key) })

	out := make(map[string]any, len(entries))
	// from records the key each normalized key came from, to name both keys
	// in a conflict.
	from := make(map[string]string, len(entries))

	// Every key is checked before any value is normalized, so a conflict
	// among this map's keys is reported whatever the map order, ahead of
	// one inside a value. Two keys that format the same tie in the sort.
	keys := make([]string, len(entries))

	for i, e := range entries {
		keys[i] = strings.ToLower(strings.TrimSpace(e.key))
		if keys[i] == "" {
			continue
		}

		if first, ok := from[keys[i]]; ok {
			return nil, conflict(first, e.key)
		}

		from[keys[i]] = e.key
	}

	for i, e := range entries {
		if keys[i] == "" {
			continue
		}

		value, err := normalizeValue(e.value.Interface())
		if err != nil {
			return nil, within(keys[i], err)
		}

		out[keys[i]] = value
	}

	return out, nil
}

// NormalizeValue returns a copy of v normalized as [NormalizeKeys]
// normalizes the values of a map: a map of any key type becomes a
// map[string]any with its keys trimmed and lowercased, the elements of a
// []any are normalized, and anything else is copied. Two keys of one map
// that become the same key return an error wrapping [ErrKeyConflict].
func NormalizeValue(v any) (any, error) {
	return normalizeValue(v)
}

// normalizeValue normalizes a map, normalizes the elements of a []any, and
// copies anything else.
func normalizeValue(v any) (any, error) {
	if list, ok := v.([]any); ok && list != nil {
		out := make([]any, len(list))
		for i, elem := range list {
			value, err := normalizeValue(elem)
			if err != nil {
				return nil, within(index(i), err)
			}

			out[i] = value
		}

		return out, nil
	}

	if rv := reflect.ValueOf(v); rv.Kind() == reflect.Map {
		return normalizeMap(rv)
	}

	return reflection.Clone(v), nil
}
