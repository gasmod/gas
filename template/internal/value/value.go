package value

import "reflect"

// IsNil reports whether v is nil, or an interface holding a nil pointer,
// map, slice, func or channel.
func IsNil(v any) bool {
	if v == nil {
		return true
	}

	rv := reflect.ValueOf(v)

	//nolint:exhaustive // only these kinds can be nil.
	switch rv.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		return rv.IsNil()
	default:
		return false
	}
}

// Clone returns a copy of b that shares no memory with it, empty and not
// nil when b is nil or empty.
func Clone(b []byte) []byte {
	return append(make([]byte, 0, len(b)), b...)
}
