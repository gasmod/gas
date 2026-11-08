package maputil

import (
	"cmp"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"time"

	"github.com/gasmod/gas/config/internal/reflection"
)

// Unbind writes the fields of src, a struct or a pointer to one, into dest,
// as described in the package documentation. It writes the fields Bind
// reads, each under its name with its case kept, and overwrites a key dest
// already holds. Unbind copies what it takes from src, so dest shares no
// maps or slices with it, except those inside a struct or array value,
// which is copied the way Go copies it.
//
// A nil dest returns an error wrapping [ErrInvalidDest], and a src that is
// not a struct or a non-nil pointer to one an error wrapping
// [ErrInvalidSrc]. Two keys of a map field that format the same return an
// error wrapping [ErrKeyConflict]; dest may then hold some of the fields.
func Unbind(src any, dest map[string]any) error {
	if dest == nil {
		return fmt.Errorf("%w: dest map is nil", ErrInvalidDest)
	}

	rv := reflect.ValueOf(src)
	for rv.Kind() == reflect.Pointer && !rv.IsNil() {
		rv = rv.Elem()
	}

	if rv.Kind() != reflect.Struct {
		return fmt.Errorf(
			"%w: want a struct or a non-nil pointer to one, got %s",
			ErrInvalidSrc,
			describe(src),
		)
	}

	return unbindStruct(rv, dest)
}

func unbindStruct(rv reflect.Value, dest map[string]any) error {
	for _, f := range fieldsOf(rv.Type()) {
		fv, ok := fieldForGet(rv, f.index)
		if !ok {
			continue
		}

		value, err := toAny(fv)
		if err != nil {
			return within(f.name, err)
		}

		dest[f.name] = value
	}

	return nil
}

// fieldForGet returns the field of the struct v at index, or false when an
// embedded pointer on the way is nil.
func fieldForGet(v reflect.Value, index []int) (reflect.Value, bool) {
	for i, x := range index {
		if i > 0 && v.Kind() == reflect.Pointer {
			if v.IsNil() {
				return reflect.Value{}, false
			}

			v = v.Elem()
		}

		v = v.Field(x)
	}

	return v, true
}

// toAny converts v to the form Unbind writes: a struct with fields to a
// map, a map to a map[string]any, a slice or array to a []any, a nil
// pointer, map or slice to nil, and anything else, including the value of
// an interface, to a copy of itself.
func toAny(v reflect.Value) (any, error) {
	//nolint:exhaustive // not interested in the rest
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return nil, nil
		}

		return toAny(v.Elem())
	case reflect.Struct:
		return structToAny(v)
	case reflect.Map:
		if v.IsNil() {
			return nil, nil
		}

		return mapToAny(v)
	case reflect.Slice, reflect.Array:
		return listToAny(v)
	default:
		return reflection.Clone(v.Interface()), nil
	}
}

func structToAny(v reflect.Value) (any, error) {
	// A struct with nothing to write, such as time.Time, would turn into an
	// empty map and lose its value. An embedded struct of an unexported type
	// cannot be read as a whole, so it is written as a map of its fields
	// even then.
	if len(fieldsOf(v.Type())) == 0 && v.CanInterface() {
		return reflection.Clone(v.Interface()), nil
	}

	m := make(map[string]any)

	err := unbindStruct(v, m)
	if err != nil {
		return nil, err
	}

	return m, nil
}

func listToAny(v reflect.Value) (any, error) {
	if v.Kind() == reflect.Slice && v.IsNil() {
		return nil, nil
	}

	out := make([]any, v.Len())
	for i := range v.Len() {
		elem, err := toAny(v.Index(i))
		if err != nil {
			return nil, within(index(i), err)
		}

		out[i] = elem
	}

	return out, nil
}

// mapToAny converts a map to a map[string]any, formatting a key that is not
// a string as keyString does.
func mapToAny(v reflect.Value) (map[string]any, error) {
	type entry struct {
		value reflect.Value
		key   string
	}

	entries := make([]entry, 0, v.Len())
	for iter := v.MapRange(); iter.Next(); {
		entries = append(entries, entry{key: keyString(iter.Key()), value: iter.Value()})
	}

	slices.SortFunc(entries, func(a, b entry) int { return cmp.Compare(a.key, b.key) })

	out := make(map[string]any, len(entries))

	// Every key is checked before any value is converted, so a conflict
	// among this map's keys is reported whatever the map order, ahead of
	// one inside a value.
	for i := 1; i < len(entries); i++ {
		if entries[i-1].key == entries[i].key {
			return nil, conflict(entries[i].key, entries[i].key)
		}
	}

	for _, e := range entries {
		value, err := toAny(e.value)
		if err != nil {
			return nil, within(e.key, err)
		}

		out[e.key] = value
	}

	return out, nil
}

// keyString is the map key k as a string: itself for a string, a
// time.Duration with its unit, a bool or a number as strconv formats it, by
// its kind, and anything else as fmt.Sprint formats it. Formatting by kind
// ignores a String method, so time.Monday is "1", which Bind reads back as a
// time.Weekday. Bind takes a duration only with a unit, so 90 seconds is
// "1m30s" rather than "90000000000".
func keyString(k reflect.Value) string {
	switch {
	case k.Kind() == reflect.Interface && !k.IsNil():
		return keyString(k.Elem())
	case k.Type() == durationType:
		return time.Duration(k.Int()).String()
	case k.Kind() == reflect.String:
		return k.String()
	case k.Kind() == reflect.Bool:
		return strconv.FormatBool(k.Bool())
	case k.CanInt():
		return strconv.FormatInt(k.Int(), 10)
	case k.CanUint():
		return strconv.FormatUint(k.Uint(), 10)
	case k.CanFloat():
		return strconv.FormatFloat(k.Float(), 'g', -1, k.Type().Bits())
	default:
		return fmt.Sprint(k.Interface())
	}
}
