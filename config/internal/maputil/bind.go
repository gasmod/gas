package maputil

import (
	"cmp"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/gasmod/gas/config/internal/reflection"
)

// Bind fills the struct dest points to from src, as described in the package
// documentation. A field without a key in src keeps its value. Bind copies
// what it takes from src, so dest shares no maps or slices with it, except
// those inside a struct or array value, which is copied the way Go copies
// it.
//
// dest must be a non-nil pointer to a struct; anything else returns an error
// wrapping [ErrInvalidDest]. A value that does not fit its field returns an
// error that names the key path and wraps [ErrConvert], [ErrOutOfRange],
// [ErrDurationWithoutUnit] or [ErrArrayTooSmall]. Two keys that match one
// field, or a key that matches several fields without regard to case and
// none of them exactly, return an error wrapping [ErrKeyConflict]. On an
// error dest may be partly filled. No error includes a value.
func Bind(src map[string]any, dest any) error {
	rv := reflect.ValueOf(dest)
	if rv.Kind() != reflect.Pointer || rv.IsNil() || rv.Elem().Kind() != reflect.Struct {
		return fmt.Errorf(
			"%w: want a non-nil pointer to a struct, got %s",
			ErrInvalidDest,
			describe(dest),
		)
	}

	return bindStruct(rv.Elem(), src)
}

// match is a field of a struct and the key of the source map that fills it.
type match struct {
	key string
	f   field
}

// matchFields pairs each field of fs with the key of src that matches it,
// exactly or else without regard to case, in the order the fields are
// declared.
func matchFields(src map[string]any, fs []field) ([]match, error) {
	exact := make(map[string]int, len(fs))
	folded := make(map[string][]int, len(fs))

	for i, f := range fs {
		exact[f.name] = i

		lower := strings.ToLower(f.name)

		folded[lower] = append(folded[lower], i)
	}

	keys := make([]string, len(fs))

	for _, key := range slices.Sorted(maps.Keys(src)) {
		i, ok := exact[key]
		if !ok {
			found := folded[strings.ToLower(key)]
			switch len(found) {
			case 0:
				continue
			case 1:
				i = found[0]
			default:
				return nil, fmt.Errorf(
					"%w: %q matches fields %q and %q",
					ErrKeyConflict,
					key,
					fs[found[0]].name,
					fs[found[1]].name,
				)
			}
		}

		if keys[i] != "" {
			return nil, conflict(keys[i], key)
		}

		keys[i] = key
	}

	var matches []match

	for i, key := range keys {
		if key != "" {
			matches = append(matches, match{key: key, f: fs[i]})
		}
	}

	return matches, nil
}

func bindStruct(dst reflect.Value, src map[string]any) error {
	matches, err := matchFields(src, fieldsOf(dst.Type()))
	if err != nil {
		return err
	}

	for _, m := range matches {
		fv, fvErr := fieldForSet(dst, m.f.index)
		if fvErr == nil {
			fvErr = set(fv, src[m.key])
		}

		if fvErr != nil {
			return within(m.key, fvErr)
		}
	}

	return nil
}

// fieldForSet returns the field of the struct v at index, allocating the nil
// embedded pointers on the way.
func fieldForSet(v reflect.Value, index []int) (reflect.Value, error) {
	for i, x := range index {
		if i > 0 && v.Kind() == reflect.Pointer {
			if v.IsNil() {
				if !v.CanSet() {
					return reflect.Value{}, fmt.Errorf(
						"%w: embedded pointer to unexported struct %s",
						ErrInvalidDest,
						typeName(v.Type().Elem()),
					)
				}

				v.Set(reflect.New(v.Type().Elem()))
			}

			v = v.Elem()
		}

		v = v.Field(x)
	}

	return v, nil
}

// set stores v in dst, converting it to dst's type.
func set(dst reflect.Value, v any) error {
	if !dst.CanSet() {
		return setUnexported(dst, v)
	}

	if v == nil {
		dst.SetZero()

		return nil
	}

	sv := reflect.ValueOf(v)
	if sv.Type().AssignableTo(dst.Type()) {
		dst.Set(reflect.ValueOf(reflection.Clone(v)))

		return nil
	}

	return convert(dst, v, sv)
}

// convert stores v, whose type is not assignable to dst's, in dst by the
// rules for dst's kind. sv is the reflect.Value of v.
func convert(dst reflect.Value, v any, sv reflect.Value) error {
	//nolint:exhaustive // not interested in the rest
	switch dst.Kind() {
	case reflect.Pointer:
		if dst.IsNil() {
			dst.Set(reflect.New(dst.Type().Elem()))
		}

		return set(dst.Elem(), v)
	case reflect.Struct:
		return setStruct(dst, sv)
	case reflect.Map:
		return setMap(dst, sv)
	case reflect.Slice:
		return setSlice(dst, sv)
	case reflect.Array:
		return setArray(dst, sv)
	default:
		return setScalar(dst, sv)
	}
}

// setUnexported binds v into dst, an embedded struct of an unexported type
// with a json tag name, or a pointer to one. Such a field cannot be set as a
// whole, only its exported fields can, so it takes only a map, and a nil
// pointer cannot be allocated.
func setUnexported(dst reflect.Value, v any) error {
	if dst.Kind() == reflect.Pointer {
		if dst.IsNil() {
			return fmt.Errorf(
				"%w: embedded pointer to unexported struct %s",
				ErrInvalidDest,
				typeName(dst.Type().Elem()),
			)
		}

		dst = dst.Elem()
	}

	if m, ok := v.(map[string]any); ok && dst.Kind() == reflect.Struct {
		return bindStruct(dst, m)
	}

	return fmt.Errorf(
		"%w: embedded struct of unexported type %s takes only a map",
		ErrInvalidDest,
		typeName(dst.Type()),
	)
}

func setStruct(dst, sv reflect.Value) error {
	if m, ok := reflect.TypeAssert[map[string]any](sv); ok {
		return bindStruct(dst, m)
	}

	if sv.Type().ConvertibleTo(dst.Type()) {
		dst.Set(reflect.ValueOf(reflection.Clone(sv.Interface())).Convert(dst.Type()))

		return nil
	}

	return cannotConvert(sv.Type(), dst.Type(), nil)
}

// setMap replaces the map in dst with one converted from sv, a map with
// string keys.
func setMap(dst, sv reflect.Value) error {
	if sv.Kind() != reflect.Map || sv.Type().Key().Kind() != reflect.String {
		return cannotConvert(sv.Type(), dst.Type(), nil)
	}

	type entry struct {
		value reflect.Value
		key   string
	}

	entries := make([]entry, 0, sv.Len())
	for iter := sv.MapRange(); iter.Next(); {
		entries = append(entries, entry{key: iter.Key().String(), value: iter.Value()})
	}

	slices.SortFunc(entries, func(a, b entry) int { return cmp.Compare(a.key, b.key) })

	keyType, elemType := dst.Type().Key(), dst.Type().Elem()
	out := reflect.MakeMapWithSize(dst.Type(), len(entries))
	// from records the source key each converted key came from, to name
	// both keys when two convert to the same one.
	from := make(map[any]string, len(entries))

	for _, e := range entries {
		kv := reflect.New(keyType).Elem()

		err := set(kv, e.key)
		if err != nil {
			return within(e.key, fmt.Errorf("key: %w", err))
		}

		if first, ok := from[kv.Interface()]; ok {
			return conflict(first, e.key)
		}

		from[kv.Interface()] = e.key

		ev := reflect.New(elemType).Elem()

		serr := set(ev, e.value.Interface())
		if serr != nil {
			return within(e.key, serr)
		}

		out.SetMapIndex(kv, ev)
	}

	dst.Set(out)

	return nil
}

// setSlice replaces the slice in dst with one converted from sv, a list or a
// comma-separated string.
func setSlice(dst, sv reflect.Value) error {
	if sv.Kind() == reflect.String {
		return setSliceFromString(dst, sv.String())
	}

	if sv.Kind() != reflect.Slice && sv.Kind() != reflect.Array {
		return cannotConvert(sv.Type(), dst.Type(), nil)
	}

	out := reflect.MakeSlice(dst.Type(), sv.Len(), sv.Len())

	err := setElems(out, sv)
	if err != nil {
		return err
	}

	dst.Set(out)

	return nil
}

func setSliceFromString(dst reflect.Value, s string) error {
	var parts []string

	if strings.TrimSpace(s) != "" {
		parts = strings.Split(s, ",")
	}

	out := reflect.MakeSlice(dst.Type(), len(parts), len(parts))
	for i, part := range parts {
		err := set(out.Index(i), strings.TrimSpace(part))
		if err != nil {
			return within(index(i), err)
		}
	}

	dst.Set(out)

	return nil
}

// setArray replaces the array in dst with one converted from sv, a list no
// longer than the array. Elements past the end of sv are zero.
func setArray(dst, sv reflect.Value) error {
	if sv.Kind() != reflect.Slice && sv.Kind() != reflect.Array {
		return cannotConvert(sv.Type(), dst.Type(), nil)
	}

	if sv.Len() > dst.Len() {
		return fmt.Errorf("%w %s: got %d", ErrArrayTooSmall, typeName(dst.Type()), sv.Len())
	}

	// Building a new array leaves dst as it was if an element fails.
	out := reflect.New(dst.Type()).Elem()

	err := setElems(out, sv)
	if err != nil {
		return err
	}

	dst.Set(out)

	return nil
}

// setElems stores each element of the list sv in the element of dst at the
// same index.
func setElems(dst, sv reflect.Value) error {
	for i := range sv.Len() {
		err := set(dst.Index(i), sv.Index(i).Interface())
		if err != nil {
			return within(index(i), err)
		}
	}

	return nil
}
