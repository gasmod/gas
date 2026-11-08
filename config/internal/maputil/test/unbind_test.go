package maputil_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/config/internal/maputil"
)

func TestUnbind_Invalid(t *testing.T) {
	t.Parallel()

	type S struct{ A int }

	var nilStruct *S

	tests := []struct {
		src     any
		dest    m
		wantErr error
		name    string
		wantMsg string
	}{
		{
			name:    "nil dest",
			src:     S{},
			dest:    nil,
			wantErr: maputil.ErrInvalidDest,
			wantMsg: "invalid destination: dest map is nil",
		},
		{
			name:    "nil src",
			src:     nil,
			dest:    m{},
			wantErr: maputil.ErrInvalidSrc,
			wantMsg: "invalid source: want a struct or a non-nil pointer to one, got nil",
		},
		{
			name:    "nil pointer",
			src:     nilStruct,
			dest:    m{},
			wantErr: maputil.ErrInvalidSrc,
			wantMsg: "invalid source: want a struct or a non-nil pointer to one, got nil *maputil_test.S",
		},
		{
			name:    "not a struct",
			src:     m{"a": 1},
			dest:    m{},
			wantErr: maputil.ErrInvalidSrc,
			wantMsg: "invalid source: want a struct or a non-nil pointer to one, got map[string]any",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := maputil.Unbind(tt.src, tt.dest)
			require.ErrorIs(t, err, tt.wantErr)
			require.EqualError(t, err, tt.wantMsg)
		})
	}
}

func TestUnbind_Fields(t *testing.T) {
	t.Parallel()

	type Base struct{ Region string }

	type Settings struct {
		Host string `json:"host"`
		Port int
	}

	type S struct {
		Base

		Ptr       *Settings
		NilPtr    *Settings
		Tagged    string `json:"db_name,omitempty"`
		Skipped   string `json:"-"`
		CamelCase string
		hidden    string
		Settings  Settings
	}

	src := &S{
		Region:    "eu",
		Settings:  Settings{Host: "h", Port: 1},
		Ptr:       &Settings{Host: "p"},
		Tagged:    "t",
		Skipped:   "s",
		CamelCase: "c",
		hidden:    "x",
	}

	got := m{"Existing": 1, "Tagged": "kept"}
	require.NoError(t, maputil.Unbind(src, got))
	assert.Equal(t, m{
		"Existing":  1,
		"Tagged":    "kept",
		"Region":    "eu",
		"Settings":  m{"host": "h", "Port": 1},
		"Ptr":       m{"host": "p", "Port": 0},
		"NilPtr":    nil,
		"db_name":   "t",
		"CamelCase": "c",
	}, got)
}

// The fields of a nil embedded pointer are left out.
func TestUnbind_NilEmbeddedPointer(t *testing.T) {
	t.Parallel()

	type Base struct{ Region string }

	type S struct {
		*Base

		Name string
	}

	got := m{}
	require.NoError(t, maputil.Unbind(S{Name: "n"}, got))
	assert.Equal(t, m{"Name": "n"}, got)

	got = m{}
	require.NoError(t, maputil.Unbind(S{Base: &Base{Region: "eu"}}, got))
	assert.Equal(t, m{"Name": "", "Region": "eu"}, got)
}

// emptyEmbed is an unexported struct with no fields to write.
type emptyEmbed struct{}

// Unbind cannot read a tagged embedded struct of an unexported type as a
// whole, so it writes the struct's fields, even when there are none, and
// never panics.
func TestUnbind_TaggedEmbeddedUnexported(t *testing.T) {
	t.Parallel()

	type S struct {
		emptyEmbed `json:"marker"`
	}

	assert.NotPanics(t, func() { _ = maputil.Unbind(S{emptyEmbed: emptyEmbed{}}, m{}) })
}

func TestUnbind_Values(t *testing.T) {
	t.Parallel()

	type Point struct{ X, Y int }

	type S struct {
		Start    time.Time
		Any      any
		NilAny   any
		Labels   map[string]string
		Ports    map[int]string
		NilMap   map[string]int
		Hosts    []string
		NilList  []string
		Matrix   [][]int
		Pair     [2]Point
		Timeout  time.Duration
		SameSite http.SameSite
	}

	start := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	src := S{
		Any:      Point{X: 1},
		Labels:   map[string]string{"Team": "x"},
		Ports:    map[int]string{80: "http"},
		Hosts:    []string{"a"},
		Matrix:   [][]int{{1, 2}},
		Pair:     [2]Point{{X: 1}, {Y: 2}},
		Start:    start,
		Timeout:  time.Second,
		SameSite: http.SameSiteLaxMode,
	}

	got := m{}
	require.NoError(t, maputil.Unbind(src, got))
	assert.Equal(t, m{
		"Any":      Point{X: 1},
		"NilAny":   nil,
		"Labels":   m{"Team": "x"},
		"Ports":    m{"80": "http"},
		"NilMap":   nil,
		"Hosts":    []any{"a"},
		"NilList":  nil,
		"Matrix":   []any{[]any{1, 2}},
		"Pair":     []any{m{"X": 1, "Y": 0}, m{"X": 0, "Y": 2}},
		"Start":    start,
		"Timeout":  time.Second,
		"SameSite": http.SameSiteLaxMode,
	}, got)
}

func TestUnbind_MapKeyConflict(t *testing.T) {
	t.Parallel()

	src := struct{ Keys map[any]int }{Keys: map[any]int{1: 1, "1": 2}}

	err := maputil.Unbind(src, m{})
	require.ErrorIs(t, err, maputil.ErrKeyConflict)
	require.EqualError(t, err, `Keys: conflicting keys: two keys format as "1"`)
}

// Changing the struct after Unbind does not change the map.
func TestUnbind_SharesNothingWithSrc(t *testing.T) {
	t.Parallel()

	type S struct {
		Any   any
		Ptr   *[]int
		Hosts []string
	}

	ptr := []int{1}
	src := S{Any: m{"k": []any{1}}, Hosts: []string{"a"}, Ptr: &ptr}

	got := m{}
	require.NoError(t, maputil.Unbind(src, got))

	src.Any.(m)["k"].([]any)[0] = "mutated"
	src.Hosts[0] = "mutated"
	ptr[0] = 99

	assert.Equal(t, m{"Any": m{"k": []any{1}}, "Hosts": []any{"a"}, "Ptr": []any{1}}, got)
}

// Bind reads what Unbind writes back into an equal struct, before and after
// the keys are normalized. A map key of a number type with a String method,
// such as a time.Weekday, is written by its kind and reads back too.
func TestUnbind_RoundTrip(t *testing.T) {
	t.Parallel()

	type Base struct{ Region string }

	type Server struct {
		Host string `json:"host"`
		Port uint16
	}

	type Config struct {
		Base

		Start    time.Time
		Any      any
		Server   *Server
		Labels   map[string]string
		Ports    map[int]string
		Hours    map[time.Weekday]string
		Name     string `json:"app_name"`
		Servers  []Server
		Hosts    []string
		Matrix   [2][2]int
		Ratio    float64
		Timeout  time.Duration
		SameSite http.SameSite
		Level    int8
		Debug    bool
	}

	want := Config{
		Region:   "eu",
		Start:    time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC),
		Any:      []any{"x"},
		Server:   &Server{Host: "h", Port: 8080},
		Labels:   map[string]string{"team": "x"},
		Ports:    map[int]string{80: "http"},
		Hours:    map[time.Weekday]string{time.Monday: "9-17"},
		Servers:  []Server{{Host: "a", Port: 1}},
		Hosts:    []string{"a", "b"},
		Name:     "app",
		Matrix:   [2][2]int{{1, 2}, {3, 4}},
		Ratio:    0.5,
		Timeout:  time.Minute,
		SameSite: http.SameSiteStrictMode,
		Level:    -1,
		Debug:    true,
	}

	raw := m{}
	require.NoError(t, maputil.Unbind(want, raw))

	normalized, err := maputil.NormalizeKeys(raw)
	require.NoError(t, err)

	for _, src := range []m{raw, normalized} {
		var got Config

		require.NoError(t, maputil.Bind(src, &got))
		assert.Equal(t, want, got)
	}
}

// A time.Duration map key is written with its unit, such as `1m30s`, because
// Bind takes a duration only with one and rejects a bare number.
func TestUnbind_DurationMapKey(t *testing.T) {
	t.Parallel()

	type Config struct {
		Backoff map[time.Duration]string
	}

	want := Config{Backoff: map[time.Duration]string{90 * time.Second: "x"}}

	raw := m{}
	require.NoError(t, maputil.Unbind(want, raw))
	assert.Equal(t, m{"Backoff": m{"1m30s": "x"}}, raw)

	var got Config

	require.NoError(t, maputil.Bind(raw, &got))
	assert.Equal(t, want, got)
}

// A map key that is not a string is written by its kind: a bool or number
// as strconv formats it, ignoring a String method, a time.Duration with its
// unit, the value of an interface as that value, and anything else as
// fmt.Sprint formats it. Bind reads each back.
func TestUnbind_MapKeyKinds(t *testing.T) {
	t.Parallel()

	type S struct {
		Any      map[any]int
		Bools    map[bool]int
		Uints    map[uint8]int
		Floats   map[float64]int
		Weekdays map[time.Weekday]int
		Arrays   map[[2]int]int
	}

	src := S{
		Any:      map[any]int{"a": 1, 2: 2, true: 3},
		Bools:    map[bool]int{true: 1, false: 0},
		Uints:    map[uint8]int{255: 1},
		Floats:   map[float64]int{1.5: 1, 1e21: 2},
		Weekdays: map[time.Weekday]int{time.Monday: 1},
		Arrays:   map[[2]int]int{{1, 2}: 1},
	}

	dest := m{}
	require.NoError(t, maputil.Unbind(src, dest))
	assert.Equal(t, m{
		"Any":      m{"a": 1, "2": 2, "true": 3},
		"Bools":    m{"true": 1, "false": 0},
		"Uints":    m{"255": 1},
		"Floats":   m{"1.5": 1, "1e+21": 2},
		"Weekdays": m{"1": 1},
		"Arrays":   m{"[1 2]": 1},
	}, dest)

	var back struct {
		Bools    map[bool]int
		Uints    map[uint8]int
		Floats   map[float64]int
		Weekdays map[time.Weekday]int
	}

	require.NoError(t, maputil.Bind(dest, &back))
	assert.Equal(t, src.Bools, back.Bools)
	assert.Equal(t, src.Uints, back.Uints)
	assert.Equal(t, src.Floats, back.Floats)
	assert.Equal(t, src.Weekdays, back.Weekdays)
}

// Map keys that format the same are reported, on every call, ahead of a
// conflict inside a value.
func TestUnbind_MapKeyConflictDeterministic(t *testing.T) {
	t.Parallel()

	src := struct{ M map[any]map[any]int }{
		M: map[any]map[any]int{1: {2: 1, "2": 2}, "1": {}},
	}

	for range 100 {
		err := maputil.Unbind(src, m{})
		require.EqualError(t, err, `M: conflicting keys: two keys format as "1"`)
	}
}
