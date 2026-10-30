package maputil_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/config/internal/maputil"
)

func TestBind_InvalidDest(t *testing.T) {
	t.Parallel()

	type S struct{ A int }

	var nilStruct *S

	tests := []struct {
		dest    any
		name    string
		wantMsg string
	}{
		{name: "nil", dest: nil, wantMsg: "got nil"},
		{name: "struct value", dest: S{}, wantMsg: "got maputil_test.S"},
		{name: "nil pointer", dest: nilStruct, wantMsg: "got nil *maputil_test.S"},
		{name: "pointer to int", dest: new(int), wantMsg: "got *int"},
		{name: "pointer to pointer", dest: new(*S), wantMsg: "got **maputil_test.S"},
		{name: "map", dest: m{}, wantMsg: "got map[string]any"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := maputil.Bind(m{"a": 1}, tt.dest)
			require.ErrorIs(t, err, maputil.ErrInvalidDest)
			require.EqualError(
				t,
				err,
				"invalid destination: want a non-nil pointer to a struct, "+tt.wantMsg,
			)
		})
	}
}

// A field without a key keeps its value, and a key without a field is
// ignored.
func TestBind_OnlyMatchingFieldsSet(t *testing.T) {
	t.Parallel()

	type S struct {
		Name string
		Port int
	}

	dest := S{Name: "default", Port: 1}
	require.NoError(t, maputil.Bind(m{"port": 2, "unknown": "x"}, &dest))
	assert.Equal(t, S{Name: "default", Port: 2}, dest)

	require.NoError(t, maputil.Bind(nil, &dest))
	assert.Equal(t, S{Name: "default", Port: 2}, dest)
}

func TestBind_FieldNames(t *testing.T) {
	t.Parallel()

	type S struct {
		Plain    string
		Tagged   string `json:"db_host"`
		Options  string `json:"port,omitempty"`
		NoName   string `json:",omitempty"`
		Skipped  string `json:"-"`
		Dash     string `json:"-,"` //nolint:staticcheck // a key named "-" on purpose
		unexport string //nolint:unused // Bind must leave it out
	}

	tests := []struct {
		src  m
		want S
		name string
	}{
		{name: "Go name, lowercase", src: m{"plain": "x"}, want: S{Plain: "x"}},
		{name: "Go name, uppercase", src: m{"PLAIN": "x"}, want: S{Plain: "x"}},
		{name: "Go name, as written", src: m{"Plain": "x"}, want: S{Plain: "x"}},
		{name: "tag name", src: m{"db_host": "x"}, want: S{Tagged: "x"}},
		{name: "tag name in another case", src: m{"DB_HOST": "x"}, want: S{Tagged: "x"}},
		{name: "tagged field ignores its Go name", src: m{"tagged": "x"}, want: S{}},
		{name: "tag options ignored", src: m{"port": "x"}, want: S{Options: "x"}},
		{name: "tag without a name", src: m{"noname": "x"}, want: S{NoName: "x"}},
		{name: "dash skips the field", src: m{"skipped": "x", "-": "y"}, want: S{Dash: "y"}},
		{name: "unexported field left out", src: m{"unexport": "x"}, want: S{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var got S

			require.NoError(t, maputil.Bind(tt.src, &got))
			assert.Equal(t, tt.want, got)
		})
	}
}

type unexportedEmbed struct {
	Inner string
}

type withUnexportedEmbed struct {
	unexportedEmbed

	Outer string
}

type withUnexportedEmbedPointer struct {
	*unexportedEmbed

	Outer string
}

// SelfEmbed embeds a pointer to itself, which a field walk must not follow
// without end.
type SelfEmbed struct {
	*SelfEmbed

	Value int
}

func TestBind_EmbeddedFields(t *testing.T) {
	t.Parallel()

	type Base struct{ Host string }

	type S struct {
		Base

		Port int
	}

	type Tagged struct {
		Base `json:"base"`
	}

	var got S

	require.NoError(t, maputil.Bind(m{"host": "h", "port": 1}, &got))
	assert.Equal(t, S{Host: "h", Port: 1}, got)

	// An embedded struct with a tag name is a field like any other.
	var tagged Tagged

	require.NoError(t, maputil.Bind(m{"host": "x", "base": m{"host": "h"}}, &tagged))
	assert.Equal(t, Tagged{Host: "h"}, tagged)

	var self SelfEmbed

	require.NoError(t, maputil.Bind(m{"value": 1}, &self))
	assert.Equal(t, SelfEmbed{Value: 1}, self)
}

// An embedded pointer is allocated only when one of its fields is set.
func TestBind_EmbeddedPointer(t *testing.T) {
	t.Parallel()

	type Base struct{ Host string }

	type S struct {
		*Base

		Port int
	}

	var got S

	require.NoError(t, maputil.Bind(m{"port": 1}, &got))
	assert.Nil(t, got.Base)

	require.NoError(t, maputil.Bind(m{"host": "h"}, &got))
	require.NotNil(t, got.Base)
	assert.Equal(t, "h", got.Host)
}

// The exported fields of an embedded struct of an unexported type are
// promoted, but a nil pointer to one cannot be allocated.
func TestBind_EmbeddedUnexported(t *testing.T) {
	t.Parallel()

	var got withUnexportedEmbed

	require.NoError(t, maputil.Bind(m{"inner": "i", "outer": "o"}, &got))
	assert.Equal(t, "i", got.Inner)
	assert.Equal(t, "o", got.Outer)

	var ptr withUnexportedEmbedPointer

	require.NoError(t, maputil.Bind(m{"outer": "o"}, &ptr))
	assert.Equal(t, "o", ptr.Outer)

	err := maputil.Bind(m{"inner": "i"}, &ptr)
	require.ErrorIs(t, err, maputil.ErrInvalidDest)
	require.EqualError(
		t,
		err,
		"inner: invalid destination: embedded pointer to unexported struct maputil_test.unexportedEmbed",
	)

	ptr.unexportedEmbed = &unexportedEmbed{}
	require.NoError(t, maputil.Bind(m{"inner": "i"}, &ptr))
	assert.Equal(t, "i", ptr.Inner)
}

// A tagged embedded struct of an unexported type cannot be set as a whole,
// only its exported fields can: it takes a map, also through a non-nil
// pointer, and anything else, or a nil pointer, which cannot be allocated,
// is an error wrapping ErrInvalidDest. Bind never panics.
func TestBind_TaggedEmbeddedUnexported(t *testing.T) {
	t.Parallel()

	type Value struct {
		unexportedEmbed `json:"base"`
	}

	type Pointer struct {
		*unexportedEmbed `json:"base"`
	}

	var value Value

	require.NoError(t, maputil.Bind(m{"base": m{"inner": "i"}}, &value))
	assert.Equal(t, "i", value.Inner)

	ptr := Pointer{unexportedEmbed: &unexportedEmbed{}}

	require.NoError(t, maputil.Bind(m{"base": m{"inner": "p"}}, &ptr))
	assert.Equal(t, "p", ptr.Inner)

	for _, tc := range []struct {
		dest any
		src  m
		want string
	}{
		{
			dest: &Value{},
			src:  m{"base": nil},
			want: "base: invalid destination: embedded struct of unexported type " +
				"maputil_test.unexportedEmbed takes only a map",
		},
		{
			dest: &Value{},
			src:  m{"base": "x"},
			want: "base: invalid destination: embedded struct of unexported type " +
				"maputil_test.unexportedEmbed takes only a map",
		},
		{
			dest: &Pointer{},
			src:  m{"base": m{"inner": "i"}},
			want: "base: invalid destination: embedded pointer to unexported struct " +
				"maputil_test.unexportedEmbed",
		},
	} {
		var err error

		assert.NotPanics(t, func() { err = maputil.Bind(tc.src, tc.dest) })
		require.ErrorIs(t, err, maputil.ErrInvalidDest)
		require.EqualError(t, err, tc.want)
	}
}

// Structs whose fields share a name, for the dominance tests.
type (
	Named       struct{ Name string }
	AlsoNamed   struct{ Name string }
	TaggedLower struct {
		Label string `json:"name"`
	}
	TaggedExact struct {
		Label string `json:"Name"`
	}

	// Shadow and ShadowLast have an outer field that shadows an embedded
	// one, declared before it and after it.
	Shadow struct {
		Named

		Name string
	}
	ShadowLast struct {
		Name        string `json:"name"`
		TaggedLower        //nolint:embeddedstructfieldcheck // declared after Name on purpose
	}
	// Tie has two fields named Name at one depth, one of them tagged.
	Tie struct {
		Named
		TaggedExact
	}
	// CaseVariants has fields named Name and name, which are two names.
	CaseVariants struct {
		Named
		TaggedLower
	}
	// Ambiguous has two untagged fields named Name at one depth.
	Ambiguous struct {
		Named
		AlsoNamed
	}
	// Twice embeds Named twice at one depth.
	Twice struct {
		NamedA
		NamedB
	}
	NamedA struct{ Named }
	NamedB struct{ Named }
)

// Of several fields with one name, the shallowest wins, a tagged one breaks
// a tie at the same depth, and otherwise the name is ambiguous and none of
// them binds.
func TestBind_FieldDominance(t *testing.T) {
	t.Parallel()

	tests := []struct {
		want any
		dest func() any
		src  m
		name string
	}{
		{
			name: "outer field shadows embedded, embedded first",
			dest: func() any { return new(Shadow) },
			src:  m{"name": "v"},
			want: &Shadow{Name: "v"},
		},
		{
			name: "outer field shadows embedded, embedded last",
			dest: func() any { return new(ShadowLast) },
			src:  m{"name": "v"},
			want: &ShadowLast{Name: "v"},
		},
		{
			name: "tagged wins at equal depth",
			dest: func() any { return new(Tie) },
			src:  m{"name": "v"},
			want: &Tie{Label: "v"},
		},
		{
			name: "names that differ in case are two fields",
			dest: func() any { return new(CaseVariants) },
			src:  m{"name": "a", "Name": "b"},
			want: &CaseVariants{Name: "b", Label: "a"},
		},
		{
			name: "ambiguous at equal depth",
			dest: func() any { return new(Ambiguous) },
			src:  m{"name": "v"},
			want: &Ambiguous{},
		},
		{
			name: "type embedded twice at one depth",
			dest: func() any { return new(Twice) },
			src:  m{"name": "v"},
			want: &Twice{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := tt.dest()
			require.NoError(t, maputil.Bind(tt.src, got))
			assert.Equal(t, tt.want, got)
		})
	}
}

// JSONNames has a field for each way a json tag names a field.
type JSONNames struct {
	Plain   string
	Tagged  string `json:"db_host"`
	Options string `json:"port,omitempty"`
	NoName  string `json:",omitempty"`
	Skipped string `json:"-"`
	Dash    string `json:"-,"` //nolint:staticcheck // a key named "-" on purpose
}

// TaggedEmbed embeds a struct under a tag name.
type TaggedEmbed struct {
	Named `json:"base"`
}

// Bind picks the same fields as encoding/json, under the same names. Each
// source uses the exact names, where both match a key to a field the same
// way.
func TestBind_FieldsMatchEncodingJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		dest func() any
		src  m
		name string
	}{
		{
			name: "names",
			dest: func() any { return new(JSONNames) },
			src: m{
				"Plain": "1", "db_host": "2", "Tagged": "3", "port": "4", "NoName": "5",
				"Skipped": "6", "-": "7",
			},
		},
		{name: "shadow", dest: func() any { return new(Shadow) }, src: m{"Name": "1"}},
		{name: "shadow last", dest: func() any { return new(ShadowLast) }, src: m{"name": "1"}},
		{name: "tie", dest: func() any { return new(Tie) }, src: m{"Name": "1"}},
		{name: "ambiguous", dest: func() any { return new(Ambiguous) }, src: m{"Name": "1"}},
		{name: "twice", dest: func() any { return new(Twice) }, src: m{"Name": "1"}},
		{
			name: "tagged embed",
			dest: func() any { return new(TaggedEmbed) },
			src:  m{"Name": "1", "base": m{"Name": "2"}},
		},
		{
			name: "unexported embed",
			dest: func() any { return new(withUnexportedEmbed) },
			src:  m{"Inner": "1", "Outer": "2"},
		},
		{name: "self embed", dest: func() any { return new(SelfEmbed) }, src: m{"Value": 1}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			data, err := json.Marshal(tt.src)
			require.NoError(t, err)

			viaJSON, viaBind := tt.dest(), tt.dest()
			require.NoError(t, json.Unmarshal(data, viaJSON))
			require.NoError(t, maputil.Bind(tt.src, viaBind))
			assert.Equal(t, viaJSON, viaBind)
		})
	}
}

// Conflicting has fields whose names a key can match in more than one way.
type Conflicting struct {
	Host  string
	Inner struct{ Host string }
	URL   string
	Url   string //nolint:revive // the name collides with URL on purpose
	Label string `json:"name"`
	Name  string
}

func TestBind_Conflict(t *testing.T) {
	t.Parallel()

	tests := []struct {
		src     m
		name    string
		wantMsg string
	}{
		{
			name:    "two keys for one field",
			src:     m{"Host": "a", "host": "b"},
			wantMsg: `conflicting keys: "Host" and "host"`,
		},
		{
			name:    "nested",
			src:     m{"inner": m{"HOST": "a", "host": "b"}},
			wantMsg: `inner: conflicting keys: "HOST" and "host"`,
		},
		{
			name:    "key for two Go names",
			src:     m{"url": "x"},
			wantMsg: `conflicting keys: "url" matches fields "URL" and "Url"`,
		},
		{
			name:    "key for a tag and a Go name",
			src:     m{"NAME": "x"},
			wantMsg: `conflicting keys: "NAME" matches fields "name" and "Name"`,
		},
		{
			name:    "exact key and a folded key for one field",
			src:     m{"Host": "a", "HOST": "b"},
			wantMsg: `conflicting keys: "HOST" and "Host"`,
		},
	}

	// An exact name wins over one that matches without regard to case.
	t.Run("exact match", func(t *testing.T) {
		t.Parallel()

		var got Conflicting

		require.NoError(t, maputil.Bind(m{"name": "tag", "Name": "go", "URL": "u"}, &got))
		assert.Equal(t, Conflicting{Label: "tag", Name: "go", URL: "u"}, got)
	})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dest := Conflicting{Host: "default"}
			err := maputil.Bind(tt.src, &dest)
			require.ErrorIs(t, err, maputil.ErrKeyConflict)
			require.EqualError(t, err, tt.wantMsg)
			assert.Equal(t, "default", dest.Host, "a conflict is found before any field is set")
		})
	}
}

// An error names the key path, as the keys are written in the source, and
// Bind reports the first failing field in declaration order.
func TestBind_ErrorPath(t *testing.T) {
	t.Parallel()

	type Server struct {
		Port uint16
	}

	type S struct {
		Labels  map[string]int
		Servers []Server
		Matrix  [][]int
		First   int
		Last    int
	}

	tests := []struct {
		src     m
		name    string
		wantMsg string
	}{
		{
			name:    "field in a list",
			src:     m{"Servers": []any{m{"port": 1}, m{"PORT": "x"}}},
			wantMsg: "Servers[1].PORT: cannot convert string to uint16: invalid syntax",
		},
		{
			name:    "map value",
			src:     m{"labels": m{"team": "x"}},
			wantMsg: "labels.team: cannot convert string to int: invalid syntax",
		},
		{
			name:    "list in a list",
			src:     m{"matrix": []any{[]any{1}, []any{2, "x"}}},
			wantMsg: "matrix[1][1]: cannot convert string to int: invalid syntax",
		},
		{
			name:    "first field in declaration order",
			src:     m{"last": "x", "first": "y"},
			wantMsg: "first: cannot convert string to int: invalid syntax",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var got S

			require.EqualError(t, maputil.Bind(tt.src, &got), tt.wantMsg)
		})
	}
}

// Fields set before the failing one keep their new values.
func TestBind_ErrorLeavesEarlierFieldsSet(t *testing.T) {
	t.Parallel()

	type S struct {
		A int
		B int
		C int
	}

	dest := S{A: 1, B: 1, C: 1}
	require.ErrorIs(t, maputil.Bind(m{"a": 2, "b": "x", "c": 2}, &dest), maputil.ErrConvert)
	assert.Equal(t, S{A: 2, B: 1, C: 1}, dest)
}
