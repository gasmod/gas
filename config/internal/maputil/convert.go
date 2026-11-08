package maputil

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// float32Bits is the size of a float32, as reflect.Type.Bits reports it.
const float32Bits = 32

var (
	durationType = reflect.TypeFor[time.Duration]()
	stringerType = reflect.TypeFor[fmt.Stringer]()
)

// bareNumber matches a decimal number with no unit, such as "30" or "-1.5".
var bareNumber = regexp.MustCompile(`^[+-]?(\d+\.?\d*|\.\d+)$`)

// setScalar converts sv to the bool, string or number dst holds.
func setScalar(dst, sv reflect.Value) error {
	err := storeScalar(dst, sv)

	switch {
	case errors.Is(err, errKind):
		return cannotConvert(sv.Type(), dst.Type(), nil)
	case errors.Is(err, strconv.ErrSyntax), errors.Is(err, errNotWhole):
		return cannotConvert(sv.Type(), dst.Type(), err)
	case errors.Is(err, strconv.ErrRange):
		return outOfRange(dst.Type())
	default:
		return err
	}
}

// storeScalar is setScalar before its errors are reported with the types:
// it fails with errKind, strconv.ErrSyntax, errNotWhole, strconv.ErrRange or
// an error that is already complete.
func storeScalar(dst, sv reflect.Value) error {
	//nolint:exhaustive // not interested in the rest
	switch dst.Kind() {
	case reflect.Bool:
		b, err := toBool(sv)
		if err == nil {
			dst.SetBool(b)
		}

		return err
	case reflect.String:
		s, err := toString(sv)
		if err == nil {
			dst.SetString(s)
		}

		return err
	case reflect.Int,
		reflect.Int8,
		reflect.Int16,
		reflect.Int32,
		reflect.Int64:
		return setInt(dst, sv)
	case reflect.Uint,
		reflect.Uint8,
		reflect.Uint16,
		reflect.Uint32,
		reflect.Uint64,
		reflect.Uintptr:
		return setUint(dst, sv)
	case reflect.Float32, reflect.Float64:
		return setFloat(dst, sv)
	default:
		return errKind
	}
}

// errKind is the reason a value of a kind that does not convert to the
// destination's kind fails, such as a map for a string field.
var errKind = errors.New("unsupported kind")

func toBool(sv reflect.Value) (bool, error) {
	switch {
	case sv.Kind() == reflect.Bool:
		return sv.Bool(), nil
	case sv.Kind() == reflect.String:
		b, err := strconv.ParseBool(sv.String())
		if err != nil {
			return false, strconv.ErrSyntax
		}

		return b, nil
	case sv.CanInt():
		return sv.Int() != 0, nil
	case sv.CanUint():
		return sv.Uint() != 0, nil
	case sv.CanFloat():
		return sv.Float() != 0, nil
	default:
		return false, errKind
	}
}

// toString formats bools and numbers as fmt.Sprint does, apart from a float
// without a String method, which it writes without an exponent, and takes
// a byte slice as its text.
func toString(sv reflect.Value) (string, error) {
	switch {
	case sv.Kind() == reflect.String:
		return sv.String(), nil
	case sv.Kind() == reflect.Slice && sv.Type().Elem().Kind() == reflect.Uint8:
		return string(sv.Bytes()), nil
	case sv.CanFloat() && !sv.Type().Implements(stringerType):
		// fmt.Sprint writes 1234567.0 as 1.234567e+06, which is not the
		// text a string field such as an account number wants.
		return strconv.FormatFloat(sv.Float(), 'f', -1, sv.Type().Bits()), nil
	case sv.Kind() == reflect.Bool, sv.CanInt(), sv.CanUint(), sv.CanFloat():
		return fmt.Sprint(sv.Interface()), nil
	default:
		return "", errKind
	}
}

func setInt(dst, sv reflect.Value) error {
	if dst.Type() == durationType {
		d, err := toDuration(sv)
		if err == nil {
			dst.SetInt(int64(d))
		}

		return err
	}

	i, err := toInt64(sv)
	if err != nil {
		return err
	}

	if dst.OverflowInt(i) {
		return strconv.ErrRange
	}

	dst.SetInt(i)

	return nil
}

func toInt64(sv reflect.Value) (int64, error) {
	switch {
	case sv.CanInt():
		return sv.Int(), nil
	case sv.CanUint():
		u := sv.Uint()
		if u > math.MaxInt64 {
			return 0, strconv.ErrRange
		}

		return int64(u), nil
	case sv.CanFloat():
		return floatToInt64(sv.Float())
	case sv.Kind() == reflect.String:
		return parseInt(sv.String())
	default:
		return 0, errKind
	}
}

func floatToInt64(f float64) (int64, error) {
	if f != math.Trunc(f) || math.IsInf(f, 0) {
		return 0, errNotWhole
	}

	// -2⁶³ is exactly representable; 2⁶³ is the first value past the end of
	// the range.
	if f < math.MinInt64 || f >= -math.MinInt64 {
		return 0, strconv.ErrRange
	}

	return int64(f), nil
}

func parseInt(s string) (int64, error) {
	i, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, numError(err)
	}

	return i, nil
}

func setUint(dst, sv reflect.Value) error {
	u, err := toUint64(sv)
	if err != nil {
		return err
	}

	if dst.OverflowUint(u) {
		return strconv.ErrRange
	}

	dst.SetUint(u)

	return nil
}

func toUint64(sv reflect.Value) (uint64, error) {
	switch {
	case sv.CanUint():
		return sv.Uint(), nil
	case sv.CanInt():
		i := sv.Int()
		if i < 0 {
			return 0, strconv.ErrRange
		}

		return uint64(i), nil
	case sv.CanFloat():
		return floatToUint64(sv.Float())
	case sv.Kind() == reflect.String:
		return parseUint(sv.String())
	default:
		return 0, errKind
	}
}

func floatToUint64(f float64) (uint64, error) {
	if f != math.Trunc(f) || math.IsInf(f, 0) {
		return 0, errNotWhole
	}

	// 2⁶⁴ is the first value past the end of the range.
	if f < 0 || f >= math.MaxUint64 {
		return 0, strconv.ErrRange
	}

	return uint64(f), nil
}

// parseUint parses s as a decimal uint64 with an optional sign, as
// strconv.ParseInt takes one. A negative number is out of range rather than
// a syntax error, and "-0" is 0.
func parseUint(s string) (uint64, error) {
	digits, negative := s, false
	if rest, ok := strings.CutPrefix(s, "+"); ok {
		digits = rest
	} else if rest, ok := strings.CutPrefix(s, "-"); ok {
		digits, negative = rest, true
	}

	u, err := strconv.ParseUint(digits, 10, 64)
	if err != nil {
		return 0, numError(err)
	}

	if negative && u != 0 {
		return 0, strconv.ErrRange
	}

	return u, nil
}

func setFloat(dst, sv reflect.Value) error {
	bits := dst.Type().Bits()

	var f float64

	switch {
	case sv.CanFloat():
		f = sv.Float()
	case sv.CanInt():
		f = intToFloat(sv.Int(), bits)
	case sv.CanUint():
		f = uintToFloat(sv.Uint(), bits)
	case sv.Kind() == reflect.String:
		var err error

		// Parsing at the field's size rounds once, straight to it.
		f, err = strconv.ParseFloat(sv.String(), bits)
		if err != nil {
			return numError(err)
		}
	default:
		return errKind
	}

	if overflowsFloat32(f, bits) {
		return strconv.ErrRange
	}

	dst.SetFloat(f)

	return nil
}

// intToFloat converts i to a float of the given size, rounding once, as Go
// converts. Going through a float64 first would round twice for a float32:
// past 2⁵³, to a float64 and then to a float32.
func intToFloat(i int64, bits int) float64 {
	if bits == float32Bits {
		return float64(float32(i))
	}

	return float64(i)
}

// uintToFloat is intToFloat for an unsigned integer.
func uintToFloat(u uint64, bits int) float64 {
	if bits == float32Bits {
		return float64(float32(u))
	}

	return float64(u)
}

// overflowsFloat32 reports whether f, finite, rounds to an infinity as a
// float of the given size, which only a float32 can. It rounds as Go
// converts, so math.MaxFloat32 written as 3.4028235e+38 fits, though as a
// float64 it is a little larger, and reflect.Value.OverflowFloat, which
// compares without rounding, reports it out of range.
func overflowsFloat32(f float64, bits int) bool {
	return bits == float32Bits && !math.IsInf(f, 0) && math.IsInf(float64(float32(f)), 0)
}

// toDuration converts sv to a time.Duration. A string is parsed with
// time.ParseDuration, so it needs a unit unless it is "0". A number has no
// unit, so only 0 is accepted.
func toDuration(sv reflect.Value) (time.Duration, error) {
	switch {
	case sv.Kind() == reflect.String:
		s := sv.String()

		d, err := time.ParseDuration(s)
		if err == nil {
			return d, nil
		}

		if bareNumber.MatchString(s) {
			return 0, durationWithoutUnit()
		}

		return 0, strconv.ErrSyntax
	case sv.CanInt(), sv.CanUint(), sv.CanFloat():
		if sv.IsZero() {
			return 0, nil
		}

		return 0, durationWithoutUnit()
	default:
		return 0, errKind
	}
}

func durationWithoutUnit() error {
	return fmt.Errorf(`%w; add one such as "s" or "ms"`, ErrDurationWithoutUnit)
}

// numError returns the reason a strconv parse failed, without the input the
// error quotes.
func numError(err error) error {
	if errors.Is(err, strconv.ErrRange) {
		return strconv.ErrRange
	}

	return strconv.ErrSyntax
}
