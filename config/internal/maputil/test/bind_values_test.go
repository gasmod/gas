package maputil_test

import (
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/config/internal/maputil"
)

// A value of a type assignable to the field is copied into it as it is,
// including named types, funcs and values for an interface field.
func TestBind_AssignableValue(t *testing.T) {
	t.Parallel()

	type Settings struct{ Host string }

	type S struct {
		Settings *Settings
		Hook     func() int
		Any      any
		Stringer fmt.Stringer
		Labels   map[string]string
		Hosts    []string
		Timeout  time.Duration
		SameSite http.SameSite
		Level    slog.Level
	}

	src := m{
		"settings": &Settings{Host: "h"},
		"hook":     func() int { return 7 },
		"any":      m{"k": []any{1}},
		"stringer": time.Second,
		"labels":   map[string]string{"team": "x"},
		"hosts":    []string{"a", "b"},
		"timeout":  2 * time.Minute,
		"samesite": http.SameSiteStrictMode,
		"level":    slog.LevelWarn,
	}

	var got S

	require.NoError(t, maputil.Bind(src, &got))
	assert.Equal(t, &Settings{Host: "h"}, got.Settings)
	assert.Equal(t, 7, got.Hook())
	assert.Equal(t, m{"k": []any{1}}, got.Any)
	assert.Equal(t, time.Second, got.Stringer)
	assert.Equal(t, map[string]string{"team": "x"}, got.Labels)
	assert.Equal(t, []string{"a", "b"}, got.Hosts)
	assert.Equal(t, 2*time.Minute, got.Timeout)
	assert.Equal(t, http.SameSiteStrictMode, got.SameSite)
	assert.Equal(t, slog.LevelWarn, got.Level)
}

// Bind copies a pointer to an interface that holds nil, such as new(any),
// into a field it is assignable to as a new pointer to nil.
func TestBind_PointerToNilInterface(t *testing.T) {
	t.Parallel()

	var (
		got struct{ P *any }
		err error
	)

	require.NotPanics(t, func() { err = maputil.Bind(m{"p": new(any)}, &got) })
	require.NoError(t, err)
	require.NotNil(t, got.P)
	assert.Nil(t, *got.P)
}

// Bind copies a map with a NaN key, which is never equal to itself and so
// cannot be looked up, into a field it is assignable to with the key and its
// value.
func TestBind_NaNMapKey(t *testing.T) {
	t.Parallel()

	var (
		got struct{ Weights map[float64]int }
		err error
	)

	require.NotPanics(t, func() {
		err = maputil.Bind(m{"weights": map[float64]int{math.NaN(): 1}}, &got)
	})
	require.NoError(t, err)
	require.Len(t, got.Weights, 1)

	for k, v := range got.Weights {
		assert.True(t, math.IsNaN(k))
		assert.Equal(t, 1, v)
	}
}

// Changing the struct after Bind does not change the source, whether a
// value was copied as it is or converted.
func TestBind_SharesNothingWithSrc(t *testing.T) {
	t.Parallel()

	type Settings struct{ Host string }

	type S struct {
		Settings *Settings
		Any      any
		Extra    map[string]any
		Labels   map[string]string
		Hosts    []string
		Ports    []int
	}

	src := m{
		"settings": &Settings{Host: "h"},
		"any":      m{"k": m{"x": 1}},
		"extra":    m{"k": m{"x": 1}},
		"labels":   map[string]string{"team": "x"},
		"hosts":    []string{"a"},
		"ports":    []any{1},
	}

	var got S

	require.NoError(t, maputil.Bind(src, &got))

	got.Settings.Host = "mutated"
	got.Any.(m)["k"].(m)["x"] = "mutated"
	got.Extra["k"].(m)["x"] = "mutated"
	got.Labels["team"] = "mutated"
	got.Hosts[0] = "mutated"
	got.Ports[0] = 99

	assert.Equal(t, m{
		"settings": &Settings{Host: "h"},
		"any":      m{"k": m{"x": 1}},
		"extra":    m{"k": m{"x": 1}},
		"labels":   map[string]string{"team": "x"},
		"hosts":    []string{"a"},
		"ports":    []any{1},
	}, src)
}

func TestBind_Nil(t *testing.T) {
	t.Parallel()

	type Settings struct{ Host string }

	type S struct {
		Ptr      *Settings
		Labels   map[string]string
		Name     string
		Settings Settings
		Hosts    []string
		Port     int
	}

	dest := S{
		Ptr:      &Settings{Host: "h"},
		Labels:   map[string]string{"a": "b"},
		Name:     "n",
		Hosts:    []string{"a"},
		Settings: Settings{Host: "h"},
		Port:     1,
	}
	src := m{"ptr": nil, "labels": nil, "name": nil, "hosts": nil, "settings": nil, "port": nil}
	require.NoError(t, maputil.Bind(src, &dest))
	assert.Equal(t, S{}, dest)
}

func TestBind_Pointer(t *testing.T) {
	t.Parallel()

	type Settings struct {
		Host string
		Port int
	}

	type S struct {
		Settings *Settings
		Count    **int
	}

	t.Run("allocated when nil", func(t *testing.T) {
		t.Parallel()

		var got S

		require.NoError(t, maputil.Bind(m{"settings": m{"host": "h"}, "count": "3"}, &got))
		assert.Equal(t, &Settings{Host: "h"}, got.Settings)
		require.NotNil(t, got.Count)
		require.NotNil(t, *got.Count)
		assert.Equal(t, 3, **got.Count)
	})

	t.Run("reused when not nil", func(t *testing.T) {
		t.Parallel()

		existing := &Settings{Host: "default", Port: 1}
		got := S{Settings: existing}
		require.NoError(t, maputil.Bind(m{"settings": m{"port": 2}}, &got))
		assert.Same(t, existing, got.Settings)
		assert.Equal(t, Settings{Host: "default", Port: 2}, *existing)
	})
}

func TestBind_Struct(t *testing.T) {
	t.Parallel()

	type Inner struct {
		Host string
		Port int
	}

	type Copy struct {
		Host string
		Port int
	}

	type S struct{ Inner Inner }

	t.Run("from a map, keeping fields without a key", func(t *testing.T) {
		t.Parallel()

		got := S{Inner: Inner{Host: "default", Port: 1}}
		require.NoError(t, maputil.Bind(m{"inner": m{"port": 2}}, &got))
		assert.Equal(t, Inner{Host: "default", Port: 2}, got.Inner)
	})

	t.Run("from a struct Go converts", func(t *testing.T) {
		t.Parallel()

		var got S

		require.NoError(t, maputil.Bind(m{"inner": Copy{Host: "h", Port: 2}}, &got))
		assert.Equal(t, Inner{Host: "h", Port: 2}, got.Inner)
	})

	t.Run("from a string", func(t *testing.T) {
		t.Parallel()

		var got S

		err := maputil.Bind(m{"inner": "x"}, &got)
		require.ErrorIs(t, err, maputil.ErrConvert)
		require.EqualError(t, err, "inner: cannot convert string to maputil_test.Inner")
	})
}

type KeyName string

// MapFields has a map field for each kind of key Bind converts to.
type MapFields struct {
	Labels map[string]string
	Ports  map[int]string
	Flags  map[bool]int
	Names  map[KeyName]int
	Counts map[string]int
}

func TestBind_Map(t *testing.T) {
	t.Parallel()

	var got MapFields

	require.NoError(t, maputil.Bind(m{
		"labels": m{"team": "x", "n": 1},
		"ports":  m{"80": "http", "-1": "none"},
		"flags":  m{"true": 1, "0": 2},
		"names":  m{"a": "1"},
		"counts": map[string]string{"a": "1"},
	}, &got))
	assert.Equal(t, MapFields{
		Labels: map[string]string{"team": "x", "n": "1"},
		Ports:  map[int]string{80: "http", -1: "none"},
		Flags:  map[bool]int{true: 1, false: 2},
		Names:  map[KeyName]int{"a": 1},
		Counts: map[string]int{"a": 1},
	}, got)
}

func TestBind_MapReplacedAsAWhole(t *testing.T) {
	t.Parallel()

	got := MapFields{Labels: map[string]string{"default": "x"}}
	require.NoError(t, maputil.Bind(m{"labels": m{"team": "y"}}, &got))
	assert.Equal(t, map[string]string{"team": "y"}, got.Labels)
}

func TestBind_MapErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		src     m
		wantErr error
		name    string
		wantMsg string
	}{
		{
			name:    "key does not convert",
			src:     m{"ports": m{"http": "x"}},
			wantErr: maputil.ErrConvert,
			wantMsg: "ports.http: key: cannot convert string to int: invalid syntax",
		},
		{
			name:    "keys convert to the same key",
			src:     m{"ports": m{"1": "a", "01": "b"}},
			wantErr: maputil.ErrKeyConflict,
			wantMsg: `ports: conflicting keys: "01" and "1"`,
		},
		{
			name:    "value does not convert",
			src:     m{"counts": m{"a": "x"}},
			wantErr: maputil.ErrConvert,
			wantMsg: "counts.a: cannot convert string to int: invalid syntax",
		},
		{
			name:    "not a map",
			src:     m{"labels": "a=b"},
			wantErr: maputil.ErrConvert,
			wantMsg: "labels: cannot convert string to map[string]string",
		},
		{
			name:    "map without string keys",
			src:     m{"labels": map[int]string{1: "a"}},
			wantErr: maputil.ErrConvert,
			wantMsg: "labels: cannot convert map[int]string to map[string]string",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var got MapFields

			err := maputil.Bind(tt.src, &got)
			require.ErrorIs(t, err, tt.wantErr)
			require.EqualError(t, err, tt.wantMsg)
		})
	}
}

// The reported error is the first in sorted key order, whatever order Go
// iterates the map in.
func TestBind_MapErrorIsDeterministic(t *testing.T) {
	t.Parallel()

	src := m{"m": m{"x": "1", "y": "2", "z": "3"}}

	for range 50 {
		var got struct{ M map[int]string }

		require.EqualError(
			t,
			maputil.Bind(src, &got),
			"m.x: key: cannot convert string to int: invalid syntax",
		)
	}
}

// SliceFields has slice fields of a few element types.
type SliceFields struct {
	Strings []string
	Ints    []int
	Bytes   []byte
}

func TestBind_Slice(t *testing.T) {
	t.Parallel()

	tests := []struct {
		src  m
		name string
		want SliceFields
	}{
		{
			name: "list",
			src:  m{"strings": []any{"a", "b"}, "ints": []any{1.0, "2"}},
			want: SliceFields{Strings: []string{"a", "b"}, Ints: []int{1, 2}},
		},
		{
			name: "list of another type",
			src:  m{"ints": []string{"1", "2"}, "strings": [2]int{1, 2}},
			want: SliceFields{Ints: []int{1, 2}, Strings: []string{"1", "2"}},
		},
		{
			name: "comma-separated string, elements trimmed",
			src:  m{"strings": "a, b ,c", "ints": "1,2, 3"},
			want: SliceFields{Strings: []string{"a", "b", "c"}, Ints: []int{1, 2, 3}},
		},
		{
			name: "empty element kept",
			src:  m{"strings": "a,,b"},
			want: SliceFields{Strings: []string{"a", "", "b"}},
		},
		{
			name: "empty string is an empty slice",
			src:  m{"strings": "", "ints": "  ", "bytes": ""},
			want: SliceFields{Strings: []string{}, Ints: []int{}, Bytes: []byte{}},
		},
		{
			name: "byte slice is a slice like any other",
			src:  m{"bytes": "104,105"},
			want: SliceFields{Bytes: []byte("hi")},
		},
		{
			name: "empty list",
			src:  m{"strings": []any{}},
			want: SliceFields{Strings: []string{}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var got SliceFields

			require.NoError(t, maputil.Bind(tt.src, &got))
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestBind_SliceErrors(t *testing.T) {
	t.Parallel()

	var got SliceFields

	err := maputil.Bind(m{"ints": "1,x"}, &got)
	require.ErrorIs(t, err, maputil.ErrConvert)
	require.EqualError(t, err, "ints[1]: cannot convert string to int: invalid syntax")

	err = maputil.Bind(m{"ints": 5}, &got)
	require.ErrorIs(t, err, maputil.ErrConvert)
	require.EqualError(t, err, "ints: cannot convert int to []int")
}

// A slice is replaced as a whole: a shorter value leaves no elements of the
// old slice behind, and the old slice's backing array is not written to.
func TestBind_SliceReplacedAsAWhole(t *testing.T) {
	t.Parallel()

	for _, value := range []any{"x", []any{"x"}, []string{"x"}} {
		defaults := []string{"a", "b", "c"}
		got := struct{ Hosts []string }{Hosts: defaults}
		require.NoError(t, maputil.Bind(m{"hosts": value}, &got), "%T", value)
		assert.Equal(t, []string{"x"}, got.Hosts, "%T", value)
		assert.Equal(t, []string{"a", "b", "c"}, defaults, "%T", value)
	}
}

// ArrayField has a fixed-size array field.
type ArrayField struct{ Ports [3]int }

func TestBind_Array(t *testing.T) {
	t.Parallel()

	tests := []struct {
		src  m
		name string
		want [3]int
	}{
		{name: "full list", src: m{"ports": []any{1, 2, 3}}, want: [3]int{1, 2, 3}},
		{name: "shorter list zeroes the rest", src: m{"ports": []any{1, 2}}, want: [3]int{1, 2, 0}},
		{name: "list of another type", src: m{"ports": []string{"1"}}, want: [3]int{1, 0, 0}},
		{name: "array", src: m{"ports": [2]int{4, 5}}, want: [3]int{4, 5, 0}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := ArrayField{Ports: [3]int{9, 9, 9}}
			require.NoError(t, maputil.Bind(tt.src, &got))
			assert.Equal(t, tt.want, got.Ports)
		})
	}
}

func TestBind_ArrayErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		src     m
		wantErr error
		name    string
		wantMsg string
	}{
		{
			name:    "longer list",
			src:     m{"ports": []any{1, 2, 3, 4}},
			wantErr: maputil.ErrArrayTooSmall,
			wantMsg: "ports: too many elements for array [3]int: got 4",
		},
		{
			name:    "string is not split",
			src:     m{"ports": "1,2,3"},
			wantErr: maputil.ErrConvert,
			wantMsg: "ports: cannot convert string to [3]int",
		},
		{
			name:    "element error",
			src:     m{"ports": []any{1, "x"}},
			wantErr: maputil.ErrConvert,
			wantMsg: "ports[1]: cannot convert string to int: invalid syntax",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := ArrayField{Ports: [3]int{9, 9, 9}}
			err := maputil.Bind(tt.src, &got)
			require.ErrorIs(t, err, tt.wantErr)
			require.EqualError(t, err, tt.wantMsg)
			assert.Equal(t, [3]int{9, 9, 9}, got.Ports, "a failed array is left as it was")
		})
	}
}

// An interface field takes only values that implement it, and reports the
// rest rather than panicking.
func TestBind_Interface(t *testing.T) {
	t.Parallel()

	var got struct{ S fmt.Stringer }

	err := maputil.Bind(m{"s": "x"}, &got)
	require.ErrorIs(t, err, maputil.ErrConvert)
	require.EqualError(t, err, "s: cannot convert string to fmt.Stringer")
}
