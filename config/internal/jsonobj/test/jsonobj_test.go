package jsonobj_test

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/config/internal/jsonobj"
)

type m = map[string]any

// An object decodes to the types encoding/json gives, except numbers: an
// integer is an int64, or a uint64 past that, so it keeps every digit, and
// any other number is a float64. null is nil. Keys are kept as they are.
func TestDecode_Object(t *testing.T) {
	t.Parallel()

	got, err := jsonobj.Decode([]byte(` {
		"DB": {"Host": "db", "port": 5432},
		"debug": true,
		"hosts": ["a", {"b": null}],
		"id": 9007199254740993,
		"token": null
	} `))
	require.NoError(t, err)
	assert.Equal(t, m{
		"DB":    m{"Host": "db", "port": int64(5432)},
		"debug": true,
		"hosts": []any{"a", m{"b": nil}},
		"id":    int64(9007199254740993),
		"token": nil,
	}, got)
}

// Each number takes the narrowest of int64, uint64 and float64 that holds
// it, at every depth, in lists too.
func TestDecode_Numbers(t *testing.T) {
	t.Parallel()

	got, err := jsonobj.Decode([]byte(`{
		"int": -9223372036854775808,
		"zero": -0,
		"uint": 18446744073709551615,
		"past": 18446744073709551616,
		"frac": 1.5,
		"whole": 1.0,
		"exp": 1e3,
		"nested": {"list": [1, [2.5, {"n": 3}]]}
	}`))
	require.NoError(t, err)
	assert.Equal(t, m{
		"int":    int64(-9223372036854775808),
		"zero":   int64(0),
		"uint":   uint64(18446744073709551615),
		"past":   1.8446744073709552e19,
		"frac":   1.5,
		"whole":  1.0,
		"exp":    1000.0,
		"nested": m{"list": []any{int64(1), []any{2.5, m{"n": int64(3)}}}},
	}, got)
}

// The result does not share memory with the input.
func TestDecode_NoSharedMemory(t *testing.T) {
	t.Parallel()

	data := []byte(`{"a": "bc"}`)
	got, err := jsonobj.Decode(data)
	require.NoError(t, err)

	copy(data, `{"a": "xy"}`)
	assert.Equal(t, m{"a": "bc"}, got)
}

// Anything but a valid object is an error and a nil result. The error gives
// an offset or a kind, and never quotes the input.
func TestDecode_Errors(t *testing.T) {
	t.Parallel()

	for input, want := range map[string]string{
		`hunter2`:               "value is not valid JSON: syntax error after byte 1",
		`{"password": hunter2}`: "value is not valid JSON: syntax error after byte 14",
		`{"a": 1} x`:            "value is not valid JSON: syntax error after byte 10",
		`{"a": `:                "value is not valid JSON: syntax error after byte 6",
		``:                      "value is not valid JSON: syntax error after byte 0",
		" \n\t":                 "value is not valid JSON: syntax error after byte 3",
		`null`:                  "value is a JSON null, not an object",
		`true`:                  "value is a JSON boolean, not an object",
		`5432`:                  "value is a JSON number, not an object",
		`"hunter2"`:             "value is a JSON string, not an object",
		`["a"]`:                 "value is a JSON array, not an object",
		`{"a": [1e400]}`:        "value holds a number out of range for float64",
	} {
		got, err := jsonobj.Decode([]byte(input))
		assert.Nil(t, got, input)
		require.EqualError(t, err, want, input)
		assert.NotContains(t, err.Error(), "'", input)
	}
}

// FuzzDecode decodes any input and checks that Decode does not panic, that
// a result holds no json.Number and only the types the package documents,
// and that an error has one of the documented shapes, none of which can
// quote the input.
func FuzzDecode(f *testing.F) {
	for _, seed := range []string{
		`{}`, `{"a": 1}`, `{"a": [1.5, {"b": null}]}`, `{"a": 1e400}`, `{"a": -0}`,
		`{"a": 18446744073709551616}`, `[]`, `null`, `"x"`, ``, `{"a": hunter2}`,
	} {
		f.Add([]byte(seed))
	}

	shape := regexp.MustCompile(`^(value is not valid JSON: syntax error after byte \d+` +
		`|value is a JSON (null|boolean|number|string|array), not an object` +
		`|value holds a number out of range for float64)$`)

	f.Fuzz(func(t *testing.T, data []byte) {
		got, err := jsonobj.Decode(data)
		if err != nil {
			if got != nil {
				t.Fatalf("Decode(%q) returned a result with error %v", data, err)
			}

			if !shape.MatchString(err.Error()) {
				t.Fatalf("Decode(%q) error %q has no documented shape", data, err)
			}

			return
		}

		checkTypes(t, data, got)
	})
}

// checkTypes fails when v holds a type Decode does not document.
func checkTypes(t *testing.T, data []byte, v any) {
	t.Helper()

	switch v := v.(type) {
	case map[string]any:
		for _, elem := range v {
			checkTypes(t, data, elem)
		}
	case []any:
		for _, elem := range v {
			checkTypes(t, data, elem)
		}
	case nil, bool, string, int64, uint64, float64:
	default:
		t.Fatalf("Decode(%q) holds a %T", data, v)
	}
}
