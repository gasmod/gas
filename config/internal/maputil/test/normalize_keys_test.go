package maputil_test

import (
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/config/internal/maputil"
)

type m = map[string]any

// normalizeCase is an input that must normalize, and the map it must
// normalize to.
type normalizeCase struct {
	in   m
	want m
	name string
}

func runNormalizeCases(t *testing.T, tests []normalizeCase) {
	t.Helper()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := maputil.NormalizeKeys(tt.in)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestNormalizeKeys(t *testing.T) {
	t.Parallel()

	runNormalizeCases(t, []normalizeCase{
		{name: "nil input", in: nil, want: m{}},
		{
			name: "lowercased and trimmed",
			in: m{
				" Host ":  "a", //nolint:gocritic // spaces on purpose
				"PORT":    1,
				"dEbUg\t": true,
			},
			want: m{"host": "a", "port": 1, "debug": true},
		},
		{
			name: "at every depth",
			in:   m{"DB": m{"Host": m{"Name": "x"}}},
			want: m{"db": m{"host": m{"name": "x"}}},
		},
		{
			name: "typed map becomes map[string]any",
			in:   m{"Labels": map[string]string{"Team": "x"}},
			want: m{"labels": m{"team": "x"}},
		},
		{
			name: "key that is not a string is formatted",
			in:   m{"ports": map[int]string{80: "http"}},
			want: m{"ports": m{"80": "http"}},
		},
	})
}

func TestNormalizeKeys_Values(t *testing.T) {
	t.Parallel()

	runNormalizeCases(t, []normalizeCase{
		{
			name: "maps inside []any at any depth",
			in:   m{"Servers": []any{m{"Host": "a"}, []any{m{"Name": "b"}}, "x"}},
			want: m{"servers": []any{m{"host": "a"}, []any{m{"name": "b"}}, "x"}},
		},
		{
			name: "maps inside other slice types keep their keys",
			in:   m{"servers": []map[string]any{{"Host": "a"}}},
			want: m{"servers": []map[string]any{{"Host": "a"}}},
		},
		{
			name: "empty keys left out with their values",
			in:   m{"": 1, "  ": m{"a": 2}, "a": m{"": 3}},
			want: m{"a": m{}},
		},
		{
			name: "values kept",
			in: m{
				"nil":      nil,
				"list":     []string{"x"},
				"nil list": []any(nil),
				"float":    1.5,
				"duration": time.Second,
			},
			want: m{
				"nil":      nil,
				"list":     []string{"x"},
				"nil list": []any(nil),
				"float":    1.5,
				"duration": time.Second,
			},
		},
	})
}

func TestNormalizeKeys_Conflict(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in      m
		name    string
		wantMsg string
	}{
		{
			name:    "case",
			in:      m{"Host": 1, "host": 2},
			wantMsg: `conflicting keys: "Host" and "host"`,
		},
		{
			name:    "space",
			in:      m{"host": 1, " host": 2}, //nolint:gocritic // a space on purpose
			wantMsg: `conflicting keys: " host" and "host"`,
		},
		{
			name:    "map and value",
			in:      m{"DB": m{"host": "a"}, "db": "b"},
			wantMsg: `conflicting keys: "DB" and "db"`,
		},
		{
			name:    "nested",
			in:      m{"Database": m{"HOST": 1, "Host": 2}},
			wantMsg: `database: conflicting keys: "HOST" and "Host"`,
		},
		{
			name:    "inside a list",
			in:      m{"servers": []any{m{}, m{"A": 1, "a": 2}}},
			wantMsg: `servers[1]: conflicting keys: "A" and "a"`,
		},
		{
			name:    "keys that format the same",
			in:      m{"m": map[any]any{1: "x", "1": "y"}},
			wantMsg: `m: conflicting keys: two keys format as "1"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := maputil.NormalizeKeys(tt.in)
			require.ErrorIs(t, err, maputil.ErrKeyConflict)
			assert.Nil(t, got)
			require.EqualError(t, err, tt.wantMsg)
		})
	}
}

// Keys that are left out cannot conflict.
func TestNormalizeKeys_EmptyKeysDoNotConflict(t *testing.T) {
	t.Parallel()

	got, err := maputil.NormalizeKeys(m{"": 1, " ": 2, "\t": 3})
	require.NoError(t, err)
	assert.Equal(t, m{}, got)
}

// A conflict names the keys, never their values.
func TestNormalizeKeys_ConflictOmitsValues(t *testing.T) {
	t.Parallel()

	_, err := maputil.NormalizeKeys(m{"TOKEN": "hunter2", "Token": "swordfish"})
	require.ErrorIs(t, err, maputil.ErrKeyConflict)
	assert.NotContains(t, err.Error(), "hunter2")
	assert.NotContains(t, err.Error(), "swordfish")
}

// The same input reports the same conflict on every call, whatever order Go
// iterates the input map in.
func TestNormalizeKeys_ConflictIsDeterministic(t *testing.T) {
	t.Parallel()

	in := m{"A": 1, "a": 2, "B": 3, "b": 4, "C": m{"X": 1, "x": 2}, "Z": 5}
	_, first := maputil.NormalizeKeys(in)
	// A map's keys are all checked, in sorted order, before any value is
	// normalized, so the conflict between "A" and "a" comes before the one
	// inside "C".
	require.EqualError(t, first, `conflicting keys: "A" and "a"`)

	for range 50 {
		_, err := maputil.NormalizeKeys(in)
		require.EqualError(t, err, first.Error())
	}
}

// The result shares no maps or slices with the input, in either direction,
// and the input is not modified.
func TestNormalizeKeys_Copies(t *testing.T) {
	t.Parallel()

	inner := m{"Host": "a"}
	list := []any{m{"Name": "b"}}
	hosts := []string{"x"}
	in := m{"DB": inner, "Servers": list, "Hosts": hosts}

	got, err := maputil.NormalizeKeys(in)
	require.NoError(t, err)

	inner["Host"] = "changed"
	list[0].(m)["Name"] = "changed"
	hosts[0] = "changed"

	assert.Equal(t, m{
		"db":      m{"host": "a"},
		"servers": []any{m{"name": "b"}},
		"hosts":   []string{"x"},
	}, got)

	got["db"].(m)["host"] = "mutated"
	got["hosts"].([]string)[0] = "mutated"

	assert.Equal(t, m{"Host": "changed"}, in["DB"])
	assert.Equal(t, []string{"changed"}, in["Hosts"])
}

// A pointer to an interface that holds nil, such as new(any), is copied as a
// new pointer to nil.
func TestNormalizeKeys_PointerToNilInterface(t *testing.T) {
	t.Parallel()

	var (
		got map[string]any
		err error
	)

	require.NotPanics(t, func() { got, err = maputil.NormalizeKeys(m{"p": new(any)}) })
	require.NoError(t, err)

	p, ok := got["p"].(*any)
	require.True(t, ok)
	assert.Nil(t, *p)
}

// A map with a NaN key inside a value NormalizeKeys copies as it is, such as
// a []map[float64]int, is copied with the key and its value.
func TestNormalizeKeys_NaNMapKey(t *testing.T) {
	t.Parallel()

	var (
		got map[string]any
		err error
	)

	require.NotPanics(t, func() {
		got, err = maputil.NormalizeKeys(m{"weights": []map[float64]int{{math.NaN(): 1}}})
	})
	require.NoError(t, err)

	list, ok := got["weights"].([]map[float64]int)
	require.True(t, ok)
	require.Len(t, list, 1)
	require.Len(t, list[0], 1)

	for k, v := range list[0] {
		assert.True(t, math.IsNaN(k))
		assert.Equal(t, 1, v)
	}
}

// FuzzNormalizeKeys checks that NormalizeKeys is deterministic and, when it
// succeeds, returns only trimmed, lowercased, non-empty keys, one for each
// input key that is not blank.
func FuzzNormalizeKeys(f *testing.F) {
	for _, seed := range []string{
		"a\nB\nc",
		"Host\nhost",
		" a\na",
		"\n \nA",
		"DB.Host\ndb.port",
		"Ä\nä",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, input string) {
		in := make(m)
		for line := range strings.Lines(input) {
			in[strings.TrimSuffix(line, "\n")] = m{line: 1}
		}

		got, err := maputil.NormalizeKeys(in)
		again, againErr := maputil.NormalizeKeys(in)

		if (err == nil) != (againErr == nil) || !reflect.DeepEqual(got, again) {
			t.Fatalf("NormalizeKeys(%q) is not deterministic", in)
		}

		if err != nil {
			if err.Error() != againErr.Error() {
				t.Fatalf("errors differ: %v, %v", err, againErr)
			}

			return
		}

		checkNormalized(t, in, got)
	})
}

// checkNormalized fails unless got holds one normalized key for each key of
// in that is not blank.
func checkNormalized(t *testing.T, in, got m) {
	t.Helper()

	blank := 0

	for key := range in {
		if strings.TrimSpace(key) == "" {
			blank++
		}
	}

	if len(got) != len(in)-blank {
		t.Fatalf("NormalizeKeys(%q) has %d keys, want %d", in, len(got), len(in)-blank)
	}

	for key := range got {
		if key == "" || key != strings.ToLower(strings.TrimSpace(key)) {
			t.Errorf("key %q is not normalized", key)
		}
	}
}

// Keys that format the same tie in the sort, so their order is the map's.
// A conflict between them is still the one reported, on every call, ahead
// of a conflict inside either value.
func TestNormalizeKeys_ConflictDeterministic(t *testing.T) {
	t.Parallel()

	src := m{"x": map[any]any{1: m{"A": 1, "a": 2}, "1": "v"}}

	for range 100 {
		_, err := maputil.NormalizeKeys(src)
		require.EqualError(t, err, `x: conflicting keys: two keys format as "1"`)
	}
}
