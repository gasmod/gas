package maputils

import (
	"fmt"
	"reflect"
	"strings"
)

// NormalizeKeys recursively converts all keys in the map to lowercase and return the new normalized map
func NormalizeKeys(m map[string]any) map[string]any {
	if val, ok := normalizeKeysInternal(m).(map[string]any); ok {
		return val
	}
	return m
}

func normalizeKeysInternal(val any) any {
	rv := reflect.ValueOf(val)
	if rv.Kind() == reflect.Map {
		out := make(map[string]any, rv.Len())
		for _, k := range rv.MapKeys() {
			sk := fmt.Sprint(k.Interface())
			nk := strings.ToLower(strings.TrimSpace(sk))
			if nk == "" {
				continue
			}
			out[nk] = normalizeKeysInternal(rv.MapIndex(k).Interface())
		}
		return out
	}
	return val
}
