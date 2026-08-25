// Package maputils provides utilities for deep binding and merging of maps into Go data structures.
// It supports recursive binding of map[string]any into structs with handling for nested types:
// structs, slices, arrays, maps and pointers. Field matching uses json tags (if present) then
// case-insensitive field names.
//
// The package includes:
//   - Bind: converts map[string]any to struct handling nested types
//   - Unbind: converts struct to map[string]any handling nested types
//   - Merge: deep merges maps while normalizing and filtering keys
//
// Key features:
//   - Type conversion between common Go types
//   - Support for json struct tags
//   - Case-insensitive field matching
//   - Handling of nested types (structs, slices, arrays, maps)
//   - Pointer auto-initialization
//   - Detailed error reporting
package maputils

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"time"
)

var (
	// Validation errors...

	// ErrDestIsNil indicates that the destination value is nil.
	ErrDestIsNil = errors.New("dest is nil")
	// ErrDestMustBePointer indicates that the destination must be a non-nil pointer to a struct.
	ErrDestMustBePointer = errors.New("dest must be a non-nil pointer to a struct")
	// ErrDestMustPointToStruct indicates that the destination pointer must point to a struct.
	ErrDestMustPointToStruct = errors.New("dest must point to a struct")
	// ErrDestinationNotSettable indicates that the destination value cannot be set.
	ErrDestinationNotSettable = errors.New("destination not settable")
	// ErrSrcIsNil indicates that the source value is nil.
	ErrSrcIsNil = errors.New("src is nil")
	// ErrSrcMustBeStruct indicates that the source must be a struct or pointer to struct.
	ErrSrcMustBeStruct = errors.New("src must be a struct or pointer to struct")

	// Type conversion errors...

	// ErrCannotSetStructFrom indicates cannot set struct from the given value type.
	ErrCannotSetStructFrom = errors.New("cannot set struct from")
	// ErrCannotSetMapFrom indicates cannot set map from the given value type.
	ErrCannotSetMapFrom = errors.New("cannot set map from")
	// ErrCannotSetSliceFrom indicates cannot set slice from the given value type.
	ErrCannotSetSliceFrom = errors.New("cannot set slice from")
	// ErrCannotSetArrayFrom indicates cannot set array from the given value type.
	ErrCannotSetArrayFrom = errors.New("cannot set array from")
	// ErrArrayTooSmall indicates the source has more elements than the destination array can hold.
	ErrArrayTooSmall = errors.New("too many elements for array")
	// ErrUnsupportedKind indicates unsupported destination value kind.
	ErrUnsupportedKind = errors.New("unsupported kind")
	// ErrUnsupportedKeyType indicates unsupported map key type during conversion.
	ErrUnsupportedKeyType = errors.New("unsupported key type")

	// Conversion errors...

	// ErrCannotConvertToBool indicates type cannot be converted to bool.
	ErrCannotConvertToBool = errors.New("cannot convert to bool")
	// ErrCannotConvertToInt64 indicates type cannot be converted to int64.
	ErrCannotConvertToInt64 = errors.New("cannot convert to int64")
	// ErrCannotConvertToUint64 indicates type cannot be converted to uint64.
	ErrCannotConvertToUint64 = errors.New("cannot convert to uint64")
	// ErrCannotConvertToFloat64 indicates type cannot be converted to float64.
	ErrCannotConvertToFloat64 = errors.New("cannot convert to float64")

	// Range/overflow errors...

	// ErrIntegerOverflow indicates when an integer value would overflow the target integer type.
	ErrIntegerOverflow = errors.New("integer overflows")
	// ErrUnsignedIntegerOverflow indicates when an unsigned integer value would overflow the target unsigned integer type.
	ErrUnsignedIntegerOverflow = errors.New("unsigned integer overflows")
	// ErrUint64Overflow indicates when a uint64 value is too large to fit in an int64.
	ErrUint64Overflow = errors.New("uint64 overflows int64")
	// ErrNegativeIntCannotConvert indicates when a negative int value cannot be converted to uint64.
	ErrNegativeIntCannotConvert = errors.New("negative int cannot convert to uint64")
	// ErrNegativeInt8CannotConvert indicates when a negative int8 value cannot be converted to uint64.
	ErrNegativeInt8CannotConvert = errors.New("negative int8 cannot convert to uint64")
	// ErrNegativeInt16CannotConvert indicates when a negative int16 value cannot be converted to uint64.
	ErrNegativeInt16CannotConvert = errors.New("negative int16 cannot convert to uint64")
	// ErrNegativeInt32CannotConvert indicates when a negative int32 value cannot be converted to uint64.
	ErrNegativeInt32CannotConvert = errors.New("negative int32 cannot convert to uint64")
	// ErrNegativeInt64CannotConvert indicates when a negative int64 value cannot be converted to uint64.
	ErrNegativeInt64CannotConvert = errors.New("negative int64 cannot convert to uint64")
	// ErrNegativeFloatCannotConvert indicates when a negative float value cannot be converted to uint64.
	ErrNegativeFloatCannotConvert = errors.New("negative float cannot convert to uint64")

	// Map errors...

	// ErrMapKeyConversion indicates an error during conversion of a map key.
	ErrMapKeyConversion = errors.New("map key conversion error")
)

// Bind binds src (map[string]any) into dest which must be a pointer to struct.
// It recursively assigns values handling nested structs, slices, arrays, maps and pointers.
// Field matching: `json` tag (if present) then case-insensitive field name.
func Bind(src map[string]any, dest any) error {
	if dest == nil {
		return ErrDestIsNil
	}

	rv := reflect.ValueOf(dest)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return ErrDestMustBePointer
	}

	rv = rv.Elem()
	if rv.Kind() != reflect.Struct {
		return ErrDestMustPointToStruct
	}

	typeInfo := buildStructFieldMap(rv.Type())

	for k, v := range src {
		if fi, ok := typeInfo[k]; ok {
			fv := getFieldByPath(rv, fi.Path)
			if !fv.CanSet() {
				// unexported field
				continue
			}

			if err := setValue(fv, v); err != nil {
				return fmt.Errorf("field %s: %w", fi.Name, err)
			}
		}
	}

	return nil
}

// getFieldByPath retrieves a field value following a path through embedded structs.
func getFieldByPath(rv reflect.Value, path []int) reflect.Value {
	current := rv
	for _, index := range path {
		current = current.Field(index)
		// If we encounter a pointer to an embedded struct, allocate it if nil
		if current.Kind() == reflect.Pointer && current.IsNil() && current.CanSet() {
			current.Set(reflect.New(current.Type().Elem()))
		}
		// Dereference pointer if needed
		if current.Kind() == reflect.Pointer {
			current = current.Elem()
		}
	}

	return current
}

// Unbind converts src (struct or pointer to struct) into dest (map[string]any).
// It recursively assigns values from the struct to the map, handling nested structs,
// slices, arrays, maps and pointers. Field keys use json tag (if present) then field name.
func Unbind(src any, dest map[string]any) error {
	if src == nil {
		return ErrSrcIsNil
	}

	if dest == nil {
		return ErrDestIsNil
	}

	srv := reflect.ValueOf(src)
	for srv.Kind() == reflect.Pointer {
		if srv.IsNil() {
			return ErrSrcIsNil
		}

		srv = srv.Elem()
	}

	if srv.Kind() != reflect.Struct {
		return ErrSrcMustBeStruct
	}

	return setMapFromStruct(srv, dest)
}

func setMapFromStruct(rv reflect.Value, m map[string]any) error {
	return setMapFromStructRecursive(rv, m)
}

func setMapFromStructRecursive(rv reflect.Value, m map[string]any) error {
	t := rv.Type()
	for i := range rv.NumField() {
		sf := t.Field(i)
		if sf.PkgPath != "" {
			continue // unexported
		}

		fv := rv.Field(i)

		if handled, err := handleEmbeddedField(sf, fv, m); err != nil {
			return err
		} else if handled {
			continue
		}

		key := fieldKey(sf)

		val, err := getAnyFromValue(fv)
		if err != nil {
			return fmt.Errorf("field %s: %w", sf.Name, err)
		}

		m[key] = val
	}

	return nil
}

func handleEmbeddedField(sf reflect.StructField, fv reflect.Value, m map[string]any) (bool, error) {
	if !sf.Anonymous {
		return false, nil
	}

	fieldType := sf.Type
	if fieldType.Kind() == reflect.Pointer {
		if fv.IsNil() {
			return true, nil
		}

		fv = fv.Elem()
		fieldType = fieldType.Elem()
	}

	if fieldType.Kind() != reflect.Struct {
		return false, nil
	}

	if err := setMapFromStructRecursive(fv, m); err != nil {
		return false, err
	}

	return true, nil
}

func fieldKey(sf reflect.StructField) string {
	key := sf.Name

	jsonTag := sf.Tag.Get("json")
	if jsonTag != "" {
		parts := strings.Split(jsonTag, ",")
		if parts[0] != "" && parts[0] != "-" {
			key = parts[0]
		}
	}

	return key
}

//nolint:gocyclo,cyclop // type switch with many cases is inherently complex
func getAnyFromValue(rv reflect.Value) (any, error) {
	if !rv.IsValid() {
		return nil, nil
	}

	if rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return nil, nil
		}

		rv = rv.Elem()
	}

	switch rv.Kind() {
	case reflect.Struct:
		subM := make(map[string]any)
		err := setMapFromStruct(rv, subM)

		return subM, err
	case reflect.Map:
		subM := make(map[string]any)

		for _, key := range rv.MapKeys() {
			kv := key.Interface()
			val := rv.MapIndex(key)

			valAny, err := getAnyFromValue(val)
			if err != nil {
				return nil, err
			}

			keyStr := fmt.Sprintf("%v", kv)
			subM[keyStr] = valAny
		}

		return subM, nil
	case reflect.Slice:
		sl := make([]any, rv.Len())
		for i := range rv.Len() {
			val, err := getAnyFromValue(rv.Index(i))
			if err != nil {
				return nil, err
			}

			sl[i] = val
		}

		return sl, nil
	case reflect.Array:
		arr := make([]any, rv.Len())
		for i := range rv.Len() {
			val, err := getAnyFromValue(rv.Index(i))
			if err != nil {
				return nil, err
			}

			arr[i] = val
		}

		return arr, nil
	case reflect.Interface:
		if rv.IsNil() {
			return nil, nil
		}

		return rv.Interface(), nil
	default:
		return rv.Interface(), nil
	}
}

type fieldInfo struct {
	Name  string
	Tag   string
	Path  []int
	Index int
}

// buildStructFieldMap creates a lookup for "keys" to fields using json tag then case-insensitive name.
func buildStructFieldMap(t reflect.Type) map[string]fieldInfo {
	out := map[string]fieldInfo{}
	buildStructFieldMapRecursive(t, []int{}, out)

	return out
}

// buildStructFieldMapRecursive recursively builds a field map handling embedded structs.
func buildStructFieldMapRecursive(t reflect.Type, indexPath []int, out map[string]fieldInfo) {
	for i := range t.NumField() {
		sf := t.Field(i)
		// skip unexported fields
		if sf.PkgPath != "" {
			continue
		}

		currentPath := make([]int, len(indexPath)+1)
		copy(currentPath, indexPath)
		currentPath[len(indexPath)] = i

		// Handle embedded structs
		if sf.Anonymous && isEmbeddedStruct(sf.Type) {
			fieldType := sf.Type
			if fieldType.Kind() == reflect.Pointer {
				fieldType = fieldType.Elem()
			}

			buildStructFieldMapRecursive(fieldType, currentPath, out)

			continue
		}

		jsonTag := sf.Tag.Get("json")
		name := sf.Name

		key := strings.ToLower(name)

		if jsonTag != "" {
			parts := strings.Split(jsonTag, ",")
			if parts[0] != "" && parts[0] != "-" {
				out[parts[0]] = fieldInfo{
					Name:  sf.Name,
					Index: currentPath[len(currentPath)-1],
					Tag:   jsonTag,
					Path:  currentPath,
				}
			}
		}
		// fallback by lowercased field name if not already present
		if _, exists := out[key]; !exists {
			out[key] = fieldInfo{
				Name:  sf.Name,
				Index: currentPath[len(currentPath)-1],
				Tag:   "",
				Path:  currentPath,
			}
		}
	}
}

func isEmbeddedStruct(t reflect.Type) bool {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	return t.Kind() == reflect.Struct
}

func setValue(dst reflect.Value, v any) error {
	// handle pointer destination by allocating if nil
	for dst.Kind() == reflect.Pointer {
		if dst.IsNil() {
			dst.Set(reflect.New(dst.Type().Elem()))
		}

		dst = dst.Elem()
	}

	if !dst.CanSet() {
		return ErrDestinationNotSettable
	}

	if v == nil {
		dst.Set(reflect.Zero(dst.Type()))

		return nil
	}

	srcVal := reflect.ValueOf(v)

	switch dst.Kind() {
	case reflect.Struct:
		return setStructValue(dst, v, srcVal)
	case reflect.Map:
		return setMapValue(dst, v, srcVal)
	case reflect.Slice:
		return setSliceValue(dst, v, srcVal)
	case reflect.Array:
		return setArrayValue(dst, v, srcVal)
	case reflect.Interface:
		dst.Set(srcVal)

		return nil
	default:
		return setBasicKind(dst, v)
	}
}

func setStructValue(dst reflect.Value, v any, srcVal reflect.Value) error {
	m, ok := v.(map[string]any)
	if ok {
		return setStructFromMap(dst, m)
	}

	if srcVal.Type().AssignableTo(dst.Type()) {
		dst.Set(srcVal)

		return nil
	}

	if srcVal.Type().ConvertibleTo(dst.Type()) {
		dst.Set(srcVal.Convert(dst.Type()))

		return nil
	}

	return fmt.Errorf("%w %T", ErrCannotSetStructFrom, v)
}

func setStructFromMap(dst reflect.Value, m map[string]any) error {
	fieldMap := buildStructFieldMap(dst.Type())
	for key, val := range m {
		fi, ok := fieldMap[key]
		if !ok {
			fi, ok = fieldMap[strings.ToLower(key)]
		}

		if !ok {
			continue
		}

		fv := getFieldByPath(dst, fi.Path)
		if !fv.CanSet() {
			continue
		}

		if err := setValue(fv, val); err != nil {
			return fmt.Errorf("struct field %s: %w", fi.Name, err)
		}
	}

	return nil
}

func setMapValue(dst reflect.Value, v any, srcVal reflect.Value) error {
	m, ok := v.(map[string]any)
	if ok {
		return setMapFromStringMap(dst, m)
	}

	if srcVal.Type().AssignableTo(dst.Type()) {
		dst.Set(srcVal)

		return nil
	}

	return fmt.Errorf("%w %T", ErrCannotSetMapFrom, v)
}

func setMapFromStringMap(dst reflect.Value, m map[string]any) error {
	newMap := reflect.MakeMapWithSize(dst.Type(), len(m))
	keyType := dst.Type().Key()
	elemType := dst.Type().Elem()

	for mk, mv := range m {
		kv := reflect.New(keyType).Elem()

		if err := setSimpleValueFromString(kv, mk); err != nil {
			if keyType.Kind() == reflect.String {
				kv.SetString(mk)
			} else {
				return fmt.Errorf("%w: %w", ErrMapKeyConversion, err)
			}
		}

		ev := reflect.New(elemType).Elem()
		if err := setValue(ev, mv); err != nil {
			return fmt.Errorf("map value for key %s: %w", mk, err)
		}

		newMap.SetMapIndex(kv, ev)
	}

	dst.Set(newMap)

	return nil
}

func setSliceValue(dst reflect.Value, v any, srcVal reflect.Value) error {
	if arr, ok := v.([]any); ok {
		return setSliceFromAnySlice(dst, arr)
	}

	if str, ok := v.(string); ok {
		return setSliceFromCSV(dst, str)
	}

	if srcVal.Kind() == reflect.Slice || srcVal.Kind() == reflect.Array {
		return setSliceFromReflect(dst, srcVal)
	}

	return fmt.Errorf("%w %T", ErrCannotSetSliceFrom, v)
}

func setSliceFromAnySlice(dst reflect.Value, arr []any) error {
	slice := reflect.MakeSlice(dst.Type(), len(arr), len(arr))
	for i := range arr {
		if err := setValue(slice.Index(i), arr[i]); err != nil {
			return fmt.Errorf("slice index %d: %w", i, err)
		}
	}

	dst.Set(slice)

	return nil
}

func setSliceFromCSV(dst reflect.Value, str string) error {
	parts := strings.Split(str, ",")

	if dst.Len() < len(parts) {
		dst.Grow(len(parts))
		dst.SetLen(len(parts))
	}

	for i, part := range parts {
		if err := setValue(dst.Index(i), strings.TrimSpace(part)); err != nil {
			return fmt.Errorf("array index %d: %w", i, err)
		}
	}

	return nil
}

func setSliceFromReflect(dst, srcVal reflect.Value) error {
	if srcVal.Type().AssignableTo(dst.Type()) {
		dst.Set(srcVal)

		return nil
	}

	l := srcVal.Len()
	slice := reflect.MakeSlice(dst.Type(), l, l)

	for i := range l {
		elem := srcVal.Index(i).Interface()
		if err := setValue(slice.Index(i), elem); err != nil {
			return fmt.Errorf("slice element %d: %w", i, err)
		}
	}

	dst.Set(slice)

	return nil
}

func setArrayValue(dst reflect.Value, v any, srcVal reflect.Value) error {
	if arr, ok := v.([]any); ok {
		// An array cannot grow, so a longer source cannot be bound without dropping values.
		if len(arr) > dst.Len() {
			return fmt.Errorf("%w %s: %d elements", ErrArrayTooSmall, dst.Type().String(), len(arr))
		}

		// Build into a new array so the destination keeps its previous
		// contents if an element fails to convert.
		newArr := reflect.New(dst.Type()).Elem()
		for i := range arr {
			if err := setValue(newArr.Index(i), arr[i]); err != nil {
				return fmt.Errorf("array index %d: %w", i, err)
			}
		}

		dst.Set(newArr)

		return nil
	}

	if srcVal.Type().AssignableTo(dst.Type()) {
		dst.Set(srcVal)

		return nil
	}

	return fmt.Errorf("%w %T", ErrCannotSetArrayFrom, v)
}

//nolint:cyclop // type switch with many cases is inherently complex
func setBasicKind(dst reflect.Value, v any) error {
	switch dst.Kind() {
	case reflect.Bool:
		b, err := toBool(v)
		if err != nil {
			return err
		}

		dst.SetBool(b)

		return nil
	case reflect.String:
		dst.SetString(toString(v))

		return nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if dt := dst.Type(); dt.PkgPath() == "time" && dt.Name() == "Duration" {
			i, err := toDuration(v)
			if err != nil {
				return err
			}

			dst.SetInt(int64(i))

			return nil
		}

		i, err := toInt64(v)
		if err != nil {
			return err
		}

		if !withinIntRange(i, dst.Type().Bits()) {
			return fmt.Errorf("%w %s: %d", ErrIntegerOverflow, dst.Type().Kind().String(), i)
		}

		dst.SetInt(i)

		return nil
	case reflect.Uint,
		reflect.Uint8,
		reflect.Uint16,
		reflect.Uint32,
		reflect.Uint64,
		reflect.Uintptr:
		u, err := toUint64(v)
		if err != nil {
			return err
		}

		if !withinUintRange(u, dst.Type().Bits()) {
			return fmt.Errorf(
				"%w %s: %d",
				ErrUnsignedIntegerOverflow,
				dst.Type().Kind().String(),
				u,
			)
		}

		dst.SetUint(u)

		return nil
	case reflect.Float32, reflect.Float64:
		f, err := toFloat64(v)
		if err != nil {
			return err
		}

		dst.SetFloat(f)

		return nil
	default:
		return fmt.Errorf("%w %s for value %T", ErrUnsupportedKind, dst.Kind().String(), v)
	}
}

// helpers for conversions.
func toBool(val any) (bool, error) {
	switch typ := val.(type) {
	case bool:
		return typ, nil
	case string:
		b, err := strconv.ParseBool(typ)
		if err != nil {
			return false, fmt.Errorf("parse bool %q: %w", typ, err)
		}

		return b, nil
	case float64:
		return typ != 0, nil
	case float32:
		return typ != 0, nil
	case int, int8, int16, int32, int64:
		return reflect.ValueOf(typ).Int() != 0, nil
	case uint, uint8, uint16, uint32, uint64:
		return reflect.ValueOf(typ).Uint() != 0, nil
	default:
		return false, fmt.Errorf("%w %T", ErrCannotConvertToBool, val)
	}
}

func toString(val any) string {
	switch x := val.(type) {
	case string:
		return x
	case []byte:
		return string(x)
	default:
		// fallback to fmt
		return fmt.Sprintf("%v", val)
	}
}

//nolint:gocyclo,cyclop // type switch with many cases is inherently complex
func toInt64(val any) (int64, error) {
	switch typ := val.(type) {
	case int:
		return int64(typ), nil
	case int8:
		return int64(typ), nil
	case int16:
		return int64(typ), nil
	case int32:
		return int64(typ), nil
	case int64:
		return typ, nil
	case uint:
		if typ > math.MaxInt64 {
			return 0, fmt.Errorf("%w: %d", ErrUint64Overflow, typ)
		}

		return int64(typ), nil
	case uint8:
		return int64(typ), nil
	case uint16:
		return int64(typ), nil
	case uint32:
		return int64(typ), nil
	case uint64:
		if typ > math.MaxInt64 {
			return 0, fmt.Errorf("%w: %d", ErrUint64Overflow, typ)
		}

		return int64(typ), nil
	case float64:
		return int64(typ), nil
	case float32:
		return int64(typ), nil
	case string:
		i, err := strconv.ParseInt(typ, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("parse int %q: %w", typ, err)
		}

		return i, nil
	case time.Duration:
		return int64(typ), nil
	default:
		return 0, fmt.Errorf("%w %T", ErrCannotConvertToInt64, val)
	}
}

func toDuration(value any) (time.Duration, error) {
	switch typedValue := value.(type) {
	case time.Duration:
		return typedValue, nil
	case int64:
		return time.Duration(typedValue), nil
	case string:
		if typedValue == "" {
			return 0, fmt.Errorf("parse duration %q: %w", typedValue, strconv.ErrSyntax)
		}

		switch typedValue[len(typedValue)-1:] {
		case "s", "m", "h": // "s" captures "ns", "us", "µs", "ms", and "s"
			d, err := time.ParseDuration(typedValue)
			if err != nil {
				return 0, fmt.Errorf("parse duration %q: %w", typedValue, err)
			}

			return d, nil
		default:
			i, err := strconv.ParseInt(typedValue, 10, 64)

			return time.Duration(i), err
		}
	default:
		return 0, fmt.Errorf("%w %T", ErrCannotConvertToInt64, value)
	}
}

//nolint:gocyclo,cyclop // type switch with many cases is inherently complex
func toUint64(val any) (uint64, error) {
	switch typ := val.(type) {
	case uint:
		return uint64(typ), nil
	case uint8:
		return uint64(typ), nil
	case uint16:
		return uint64(typ), nil
	case uint32:
		return uint64(typ), nil
	case uint64:
		return typ, nil
	case int:
		if typ < 0 {
			return 0, fmt.Errorf("%w: %d", ErrNegativeIntCannotConvert, typ)
		}

		return uint64(typ), nil
	case int8:
		if typ < 0 {
			return 0, fmt.Errorf("%w: %d", ErrNegativeInt8CannotConvert, typ)
		}

		return uint64(typ), nil
	case int16:
		if typ < 0 {
			return 0, fmt.Errorf("%w: %d", ErrNegativeInt16CannotConvert, typ)
		}

		return uint64(typ), nil
	case int32:
		if typ < 0 {
			return 0, fmt.Errorf("%w: %d", ErrNegativeInt32CannotConvert, typ)
		}

		return uint64(typ), nil
	case int64:
		if typ < 0 {
			return 0, fmt.Errorf("%w: %d", ErrNegativeInt64CannotConvert, typ)
		}

		return uint64(typ), nil
	case float64:
		if typ < 0 {
			return 0, fmt.Errorf("%w: %f", ErrNegativeFloatCannotConvert, typ)
		}

		return uint64(typ), nil
	case string:
		u, err := strconv.ParseUint(typ, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("parse uint %q: %w", typ, err)
		}

		return u, nil
	default:
		return 0, fmt.Errorf("%w %T", ErrCannotConvertToUint64, val)
	}
}

//nolint:cyclop // type switch with many cases is inherently complex
func toFloat64(val any) (float64, error) {
	switch typ := val.(type) {
	case float64:
		return typ, nil
	case float32:
		return float64(typ), nil
	case int:
		return float64(typ), nil
	case int8:
		return float64(typ), nil
	case int16:
		return float64(typ), nil
	case int32:
		return float64(typ), nil
	case int64:
		return float64(typ), nil
	case uint:
		return float64(typ), nil
	case uint8:
		return float64(typ), nil
	case uint16:
		return float64(typ), nil
	case uint32:
		return float64(typ), nil
	case uint64:
		return float64(typ), nil
	case string:
		f, err := strconv.ParseFloat(typ, 64)
		if err != nil {
			return 0, fmt.Errorf("parse float %q: %w", typ, err)
		}

		return f, nil
	default:
		return 0, fmt.Errorf("%w %T", ErrCannotConvertToFloat64, val)
	}
}

func withinIntRange(intVal int64, bits int) bool {
	switch bits {
	case 8:
		return intVal >= math.MinInt8 && intVal <= math.MaxInt8
	case 16:
		return intVal >= math.MinInt16 && intVal <= math.MaxInt16
	case 32:
		return intVal >= math.MinInt32 && intVal <= math.MaxInt32
	case 64:
		return true
	default:
		return true
	}
}

func withinUintRange(unt uint64, bits int) bool {
	switch bits {
	case 8:
		return unt <= math.MaxUint8
	case 16:
		return unt <= math.MaxUint16
	case 32:
		return unt <= math.MaxUint32
	case 64:
		return true
	default:
		return true
	}
}

// setSimpleValueFromString tries to set a reflect.Value from a string (used for map keys).
func setSimpleValueFromString(dst reflect.Value, str string) error {
	switch dst.Kind() {
	case reflect.String:
		dst.SetString(str)

		return nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		i, err := strconv.ParseInt(str, 10, dst.Type().Bits())
		if err != nil {
			return fmt.Errorf("parse int key %q: %w", str, err)
		}

		dst.SetInt(i)

		return nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		u, err := strconv.ParseUint(str, 10, dst.Type().Bits())
		if err != nil {
			return fmt.Errorf("parse uint key %q: %w", str, err)
		}

		dst.SetUint(u)

		return nil
	case reflect.Bool:
		b, err := strconv.ParseBool(str)
		if err != nil {
			return fmt.Errorf("parse bool key %q: %w", str, err)
		}

		dst.SetBool(b)

		return nil
	default:
		return fmt.Errorf("%w %s", ErrUnsupportedKeyType, dst.Kind().String())
	}
}
