// Package reflection provides utilities for working with Go's reflection system.
// It includes functions for deep cloning of values, handling nil checks, and other
// reflection-based operations.
package reflection

import "reflect"

// Clone creates and returns a deep copy of the input value.
func Clone[T any](v T) T {
	if IsNil(v) {
		// Nils have nothing to copy; returning them as-is preserves their type.
		return v
	}

	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Pointer:
		elem := Clone(rv.Elem().Interface())
		newPtr := reflect.New(rv.Type().Elem())
		newPtr.Elem().Set(reflect.ValueOf(elem))

		return newPtr.Interface().(T)

	case reflect.Slice:
		newSlice := reflect.MakeSlice(rv.Type(), rv.Len(), rv.Cap())
		elemType := rv.Type().Elem()

		for i := range rv.Len() {
			newSlice.Index(i).Set(cloneValueOfType(rv.Index(i).Interface(), elemType))
		}

		return newSlice.Interface().(T)

	case reflect.Map:
		newMap := reflect.MakeMapWithSize(rv.Type(), rv.Len())
		keyType, elemType := rv.Type().Key(), rv.Type().Elem()

		for _, key := range rv.MapKeys() {
			newMap.SetMapIndex(
				cloneValueOfType(key.Interface(), keyType),
				cloneValueOfType(rv.MapIndex(key).Interface(), elemType),
			)
		}

		return newMap.Interface().(T)

	default:
		return v
	}
}

// cloneValueOfType deep copies v and returns it as a reflect.Value of type t.
// A nil v yields the zero value of t: reflect.ValueOf(nil) is the invalid zero
// reflect.Value, which panics when set into a slice and silently deletes the
// entry when used with SetMapIndex.
func cloneValueOfType(v any, t reflect.Type) reflect.Value {
	rv := reflect.ValueOf(Clone(v))
	if !rv.IsValid() {
		return reflect.Zero(t)
	}

	return rv
}
