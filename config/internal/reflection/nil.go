package reflection

import "reflect"

// IsNil checks if the given value is nil, supporting both typed and untyped nils.
func IsNil[T any](v T) bool {
	// plain nil interface
	if any(v) == nil {
		return true
	}

	// typed nils
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Map, reflect.Chan, reflect.Func, reflect.Interface:
		return rv.IsNil()
	default:
		return false
	}
}
