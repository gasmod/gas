package reflection

import "reflect"

// Clone returns a deep copy of v, as described in the package
// documentation.
func Clone[T any](v T) T {
	rv := reflect.ValueOf(v)

	//nolint:exhaustive // not interested in the rest
	switch rv.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Map:
		return clone(rv).Interface().(T) //nolint:forcetypeassert // clone keeps the type of v
	default:
		return v
	}
}

// clone returns a deep copy of v. Working on reflect.Value keeps the type
// of every level, and a nil interface inside a map, slice or pointer stays
// a valid Value, which SetMapIndex would otherwise take as a deletion.
func clone(v reflect.Value) reflect.Value {
	//nolint:exhaustive // not interested in the rest
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return v
		}

		return clonePointer(v)

	case reflect.Interface:
		if v.IsNil() {
			return v
		}

		i := reflect.New(v.Type()).Elem()
		i.Set(clone(v.Elem()))

		return i

	case reflect.Slice:
		if v.IsNil() {
			return v
		}

		return cloneSlice(v)

	case reflect.Map:
		if v.IsNil() {
			return v
		}

		return cloneMap(v)

	default:
		return v
	}
}

func clonePointer(v reflect.Value) reflect.Value {
	p, ok := allocate(v.Type().Elem())
	if !ok {
		return v
	}

	p.Elem().Set(clone(v.Elem()))

	// New yields the unnamed pointer type; convert keeps a named one.
	return p.Convert(v.Type())
}

// allocate returns a pointer to a new zero value of t, or false when Go
// cannot allocate t, such as an incomplete C type.
func allocate(t reflect.Type) (p reflect.Value, ok bool) {
	// reflect has no way to ask whether t can be allocated; New panics.
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()

	return reflect.New(t), true
}

func cloneSlice(v reflect.Value) reflect.Value {
	s := reflect.MakeSlice(v.Type(), v.Len(), v.Cap())
	kind := v.Type().Elem().Kind()

	//nolint:exhaustive // not interested in the rest
	switch kind {
	case reflect.Pointer,
		reflect.Slice,
		reflect.Map,
		reflect.Interface:
		for i := range v.Len() {
			s.Index(i).Set(clone(v.Index(i)))
		}
	default:
		// clone returns any other element as it is, so one copy does.
		reflect.Copy(s, v)
	}

	return s
}

func cloneMap(v reflect.Value) reflect.Value {
	m := reflect.MakeMapWithSize(v.Type(), v.Len())
	// Ranging yields each entry, including one under a NaN key, which
	// MapIndex cannot find because NaN is never equal to itself. Keys are
	// kept as they are: a copied pointer key would be a new key.
	for iter := v.MapRange(); iter.Next(); {
		m.SetMapIndex(iter.Key(), clone(iter.Value()))
	}

	return m
}
