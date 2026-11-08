package maputil_test

import (
	"fmt"
	"math"
	"net/http"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/config/internal/maputil"
)

// Scalars holds a field of each scalar kind Bind converts to.
type Scalars struct {
	String   string
	Mode     Mode
	Float64  float64
	Int      int
	Uint     uint
	Duration time.Duration
	SameSite http.SameSite
	Float32  float32
	Int16    int16
	Uint16   uint16
	Int8     int8
	Uint8    uint8
	Bool     bool
	Flag     Flag
}

type (
	Mode string
	Flag bool
)

// convertCase binds value to one field of Scalars, and gives the field's
// value afterwards or the error it must fail with.
type convertCase struct {
	value   any
	want    any
	wantErr error
	wantMsg string
	field   string
}

func runConvertCases(t *testing.T, tests []convertCase) {
	t.Helper()

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s/%T(%v)", tt.field, tt.value, tt.value), func(t *testing.T) {
			t.Parallel()

			var got Scalars

			err := maputil.Bind(m{tt.field: tt.value}, &got)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				require.EqualError(t, err, tt.field+": "+tt.wantMsg)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.want, fieldOf(&got, tt.field))
		})
	}
}

// fieldOf returns the field of s whose lowercased name is name.
func fieldOf(s *Scalars, name string) any {
	return reflect.ValueOf(s).Elem().FieldByNameFunc(func(n string) bool {
		return strings.ToLower(n) == name
	}).Interface()
}

func TestBind_Bool(t *testing.T) {
	t.Parallel()

	runConvertCases(t, []convertCase{
		{field: "bool", value: true, want: true},
		{field: "bool", value: "true", want: true},
		{field: "bool", value: "F", want: false},
		{field: "bool", value: "1", want: true},
		{field: "bool", value: 0.0, want: false},
		{field: "bool", value: 2, want: true},
		{field: "bool", value: uint8(0), want: false},
		{field: "flag", value: "true", want: Flag(true)},
		{field: "flag", value: Flag(true), want: Flag(true)},
		{field: "bool", value: Flag(true), want: true},
		{
			field:   "bool",
			value:   "yes",
			wantErr: maputil.ErrConvert,
			wantMsg: "cannot convert string to bool: invalid syntax",
		},
		{
			field:   "bool",
			value:   m{},
			wantErr: maputil.ErrConvert,
			wantMsg: "cannot convert map[string]any to bool",
		},
	})
}

func TestBind_String(t *testing.T) {
	t.Parallel()

	runConvertCases(t, []convertCase{
		{field: "string", value: "x", want: "x"},
		{field: "string", value: []byte("x"), want: "x"},
		{field: "string", value: 8080.0, want: "8080"},
		{field: "string", value: 1.5, want: "1.5"},
		{field: "string", value: 1234567.0, want: "1234567"},
		{field: "string", value: 1e21, want: "1000000000000000000000"},
		{field: "string", value: 1e-7, want: "0.0000001"},
		{field: "string", value: float32(0.1), want: "0.1"},
		{field: "string", value: -3, want: "-3"},
		{field: "string", value: uint(3), want: "3"},
		{field: "string", value: true, want: "true"},
		{field: "string", value: Mode("dev"), want: "dev"},
		{field: "string", value: 30 * time.Second, want: "30s"},
		{field: "mode", value: "dev", want: Mode("dev")},
		{
			field:   "string",
			value:   m{"host": "x"},
			wantErr: maputil.ErrConvert,
			wantMsg: "cannot convert map[string]any to string",
		},
		{
			field:   "string",
			value:   []any{"a"},
			wantErr: maputil.ErrConvert,
			wantMsg: "cannot convert []any to string",
		},
	})
}

func TestBind_Int(t *testing.T) {
	t.Parallel()

	runConvertCases(t, []convertCase{
		{field: "int", value: 42, want: 42},
		{field: "int", value: "-42", want: -42},
		{field: "int", value: "+7", want: 7},
		{field: "int", value: 8080.0, want: 8080},
		{field: "int", value: 1e2, want: 100},
		{field: "int", value: uint64(5), want: 5},
		{field: "int", value: time.Second, want: int(time.Second)},
		{field: "int8", value: -128, want: int8(-128)},
		{field: "int16", value: "32767", want: int16(32767)},
		{field: "samesite", value: 2.0, want: http.SameSiteLaxMode},
		{field: "samesite", value: "3", want: http.SameSiteStrictMode},
	})
}

func TestBind_IntOutOfRange(t *testing.T) {
	t.Parallel()

	runConvertCases(t, []convertCase{
		{
			field:   "int8",
			value:   200,
			wantErr: maputil.ErrOutOfRange,
			wantMsg: "value out of range for int8",
		},
		{
			field:   "int16",
			value:   "-40000",
			wantErr: maputil.ErrOutOfRange,
			wantMsg: "value out of range for int16",
		},
		{
			field:   "int",
			value:   "99999999999999999999",
			wantErr: maputil.ErrOutOfRange,
			wantMsg: "value out of range for int",
		},
		{
			field:   "int",
			value:   uint64(math.MaxUint64),
			wantErr: maputil.ErrOutOfRange,
			wantMsg: "value out of range for int",
		},
		{
			field:   "int",
			value:   1e19,
			wantErr: maputil.ErrOutOfRange,
			wantMsg: "value out of range for int",
		},
	})
}

func TestBind_IntInvalid(t *testing.T) {
	t.Parallel()

	runConvertCases(t, []convertCase{
		{
			field:   "int",
			value:   8080.5,
			wantErr: maputil.ErrConvert,
			wantMsg: "cannot convert float64 to int: not a whole number",
		},
		{
			field:   "int",
			value:   math.NaN(),
			wantErr: maputil.ErrConvert,
			wantMsg: "cannot convert float64 to int: not a whole number",
		},
		{
			field:   "int",
			value:   math.Inf(1),
			wantErr: maputil.ErrConvert,
			wantMsg: "cannot convert float64 to int: not a whole number",
		},
		{
			field:   "int",
			value:   "8080.0",
			wantErr: maputil.ErrConvert,
			wantMsg: "cannot convert string to int: invalid syntax",
		},
		{
			field:   "int",
			value:   "0x10",
			wantErr: maputil.ErrConvert,
			wantMsg: "cannot convert string to int: invalid syntax",
		},
		{
			field:   "int",
			value:   " 1",
			wantErr: maputil.ErrConvert,
			wantMsg: "cannot convert string to int: invalid syntax",
		},
		{
			field:   "int",
			value:   true,
			wantErr: maputil.ErrConvert,
			wantMsg: "cannot convert bool to int",
		},
		{
			field:   "samesite",
			value:   "lax",
			wantErr: maputil.ErrConvert,
			wantMsg: "cannot convert string to http.SameSite: invalid syntax",
		},
	})
}

// -2⁶³ fits an int64 exactly, and 2⁶³ is the first float past the end.
func TestBind_IntFloatBounds(t *testing.T) {
	t.Parallel()

	var got struct{ N int64 }

	require.NoError(t, maputil.Bind(m{"n": float64(math.MinInt64)}, &got))
	assert.Equal(t, int64(math.MinInt64), got.N)
	require.ErrorIs(t, maputil.Bind(m{"n": math.Ldexp(1, 63)}, &got), maputil.ErrOutOfRange)
}

func TestBind_Uint(t *testing.T) {
	t.Parallel()

	runConvertCases(t, []convertCase{
		{field: "uint", value: 42, want: uint(42)},
		{field: "uint", value: "42", want: uint(42)},
		{field: "uint", value: 42.0, want: uint(42)},
		{field: "uint", value: float32(1), want: uint(1)},
		{field: "uint8", value: "255", want: uint8(255)},
		{field: "uint16", value: 65535, want: uint16(65535)},
		{field: "uint16", value: uint8(200), want: uint16(200)},
		{
			field:   "uint8",
			value:   uint64(256),
			wantErr: maputil.ErrOutOfRange,
			wantMsg: "value out of range for uint8",
		},
		{
			field:   "uint",
			value:   true,
			wantErr: maputil.ErrConvert,
			wantMsg: "cannot convert bool to uint",
		},
		{
			field:   "uint",
			value:   1.5,
			wantErr: maputil.ErrConvert,
			wantMsg: "cannot convert float64 to uint: not a whole number",
		},
		{
			field:   "uint",
			value:   2e19,
			wantErr: maputil.ErrOutOfRange,
			wantMsg: "value out of range for uint",
		},
		// A sign is taken as for an int field.
		{field: "uint", value: "+5", want: uint(5)},
		{field: "uint16", value: "+8080", want: uint16(8080)},
		{field: "uint", value: "+0", want: uint(0)},
		{field: "uint", value: "-0", want: uint(0)},
	})
}

func TestBind_UintErrors(t *testing.T) {
	t.Parallel()

	runConvertCases(t, []convertCase{
		{
			field:   "uint8",
			value:   256,
			wantErr: maputil.ErrOutOfRange,
			wantMsg: "value out of range for uint8",
		},
		{
			field:   "uint16",
			value:   "70000",
			wantErr: maputil.ErrOutOfRange,
			wantMsg: "value out of range for uint16",
		},
		{
			field:   "uint",
			value:   -1,
			wantErr: maputil.ErrOutOfRange,
			wantMsg: "value out of range for uint",
		},
		{
			field:   "uint",
			value:   "-1",
			wantErr: maputil.ErrOutOfRange,
			wantMsg: "value out of range for uint",
		},
		{
			field:   "uint",
			value:   -1.0,
			wantErr: maputil.ErrOutOfRange,
			wantMsg: "value out of range for uint",
		},
		{
			field:   "uint",
			value:   1e20,
			wantErr: maputil.ErrOutOfRange,
			wantMsg: "value out of range for uint",
		},
		{
			field:   "uint",
			value:   0.5,
			wantErr: maputil.ErrConvert,
			wantMsg: "cannot convert float64 to uint: not a whole number",
		},
		{
			field:   "uint",
			value:   "-x",
			wantErr: maputil.ErrConvert,
			wantMsg: "cannot convert string to uint: invalid syntax",
		},
	})
}

func TestBind_Float(t *testing.T) {
	t.Parallel()

	runConvertCases(t, []convertCase{
		{field: "float64", value: 2.25, want: 2.25},
		{field: "float64", value: "1e3", want: 1000.0},
		{field: "float64", value: 3, want: 3.0},
		{field: "float64", value: uint(3), want: 3.0},
		{field: "float32", value: "1.5", want: float32(1.5)},
		{field: "float32", value: float32(0.25), want: float32(0.25)},
		// An integer past 2⁵³ is rounded once to a float32, as Go converts
		// it and as the same number written as a string is parsed, not first
		// to a float64.
		{field: "float32", value: int64(1<<62 + 1<<38 + 1), want: float32(1<<62 + 1<<38 + 1)},
		{field: "float32", value: uint64(1<<62 + 1<<38 + 1), want: float32(1<<62 + 1<<38 + 1)},
		{field: "float32", value: "4611686293305294849", want: float32(1<<62 + 1<<38 + 1)},
		{
			field:   "float32",
			value:   1e300,
			wantErr: maputil.ErrOutOfRange,
			wantMsg: "value out of range for float32",
		},
		{
			field:   "float64",
			value:   "1e400",
			wantErr: maputil.ErrOutOfRange,
			wantMsg: "value out of range for float64",
		},
		{
			field:   "float64",
			value:   "1,5",
			wantErr: maputil.ErrConvert,
			wantMsg: "cannot convert string to float64: invalid syntax",
		},
	})
}

// A float32 takes a number that rounds to its largest finite value, such as
// "3.4028235e+38", the shortest text strconv and fmt write for
// math.MaxFloat32, and the key Unbind writes for it in a map[float32] field.
// Only a number that rounds past it is out of range.
func TestBind_Float32Max(t *testing.T) {
	t.Parallel()

	runConvertCases(t, []convertCase{
		{field: "float32", value: "3.4028235e+38", want: float32(math.MaxFloat32)},
		{field: "float32", value: "-3.4028235e+38", want: float32(-math.MaxFloat32)},
		{field: "float32", value: 3.4028235e38, want: float32(math.MaxFloat32)},
		{
			field:   "float32",
			value:   "3.4028236e+38",
			wantErr: maputil.ErrOutOfRange,
			wantMsg: "value out of range for float32",
		},
	})
}

func TestBind_Duration(t *testing.T) {
	t.Parallel()

	const noUnit = `duration has no unit; add one such as "s" or "ms"`

	runConvertCases(t, []convertCase{
		{field: "duration", value: 2 * time.Minute, want: 2 * time.Minute},
		{field: "duration", value: "300ms", want: 300 * time.Millisecond},
		{field: "duration", value: "-1.5h", want: -90 * time.Minute},
		{field: "duration", value: "2h45m", want: 165 * time.Minute},
		{field: "duration", value: "0", want: time.Duration(0)},
		{field: "duration", value: 0, want: time.Duration(0)},
		{field: "duration", value: 0.0, want: time.Duration(0)},
		{field: "duration", value: uint8(0), want: time.Duration(0)},
		{field: "duration", value: "30", wantErr: maputil.ErrDurationWithoutUnit, wantMsg: noUnit},
		{
			field:   "duration",
			value:   "-1.5",
			wantErr: maputil.ErrDurationWithoutUnit,
			wantMsg: noUnit,
		},
		{field: "duration", value: 5.0, wantErr: maputil.ErrDurationWithoutUnit, wantMsg: noUnit},
		{field: "duration", value: 30, wantErr: maputil.ErrDurationWithoutUnit, wantMsg: noUnit},
		{
			field:   "duration",
			value:   int64(time.Second),
			wantErr: maputil.ErrDurationWithoutUnit,
			wantMsg: noUnit,
		},
		{
			field:   "duration",
			value:   "",
			wantErr: maputil.ErrConvert,
			wantMsg: "cannot convert string to time.Duration: invalid syntax",
		},
		{
			field:   "duration",
			value:   "1e3",
			wantErr: maputil.ErrConvert,
			wantMsg: "cannot convert string to time.Duration: invalid syntax",
		},
		{
			field:   "duration",
			value:   "5 s",
			wantErr: maputil.ErrConvert,
			wantMsg: "cannot convert string to time.Duration: invalid syntax",
		},
		{
			field:   "duration",
			value:   true,
			wantErr: maputil.ErrConvert,
			wantMsg: "cannot convert bool to time.Duration",
		},
	})
}

// A failed duration leaves the field as it was.
func TestBind_DurationErrorKeepsField(t *testing.T) {
	t.Parallel()

	dest := struct{ Timeout time.Duration }{Timeout: time.Second}
	require.ErrorIs(t, maputil.Bind(m{"timeout": "30"}, &dest), maputil.ErrDurationWithoutUnit)
	assert.Equal(t, time.Second, dest.Timeout)
}

// errorShape matches every message a string can fail to bind with: a key
// path, then fixed text and Go type names. Nothing in it can come from the
// value.
var errorShape = regexp.MustCompile(`^[a-z0-9]+(\[\d+\])*: (` +
	`cannot convert string to [a-zA-Z0-9_.\[\]]+(: invalid syntax)?` +
	`|value out of range for [a-zA-Z0-9_.]+` +
	`|duration has no unit; add one such as "s" or "ms"` +
	`)$`)

// FuzzBindString binds any string to a field of every kind and checks that
// Bind does not panic and that a failure never includes the string.
func FuzzBindString(f *testing.F) {
	for _, seed := range []string{
		"", "0", "-1", "1.5", "1e400", "true", "30", "30s", "a,b", "1,x", "hunter2",
		"99999999999999999999", "NaN", "\x00", "ä",
	} {
		f.Add(seed)
	}

	type Lists struct {
		Ints      []int
		Strings   []string
		Durations []time.Duration
		Bools     []bool
		Array     [2]int
	}

	fields := []string{
		"string", "mode", "float64", "int", "uint", "duration", "samesite", "float32",
		"int16", "uint16", "int8", "uint8", "bool", "flag",
	}

	f.Fuzz(func(t *testing.T, s string) {
		for _, name := range fields {
			var got Scalars

			checkShape(t, name, s, maputil.Bind(m{name: s}, &got))
		}

		for _, name := range []string{"ints", "strings", "durations", "bools", "array"} {
			var got Lists

			checkShape(t, name, s, maputil.Bind(m{name: s}, &got))
		}
	})
}

func checkShape(t *testing.T, field, value string, err error) {
	t.Helper()

	if err == nil || errorShape.MatchString(err.Error()) {
		return
	}

	t.Errorf("binding %q to %s: unexpected error %q", value, field, err)
}
