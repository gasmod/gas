package maputils_test

import (
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/config/internal/maputils"
)

func TestBind_EmbeddedStruct(t *testing.T) {
	t.Parallel()

	type Embedded struct {
		MyKey string `json:"mykey"`
		Value int    `json:"value"`
	}

	type Parent struct {
		Name string `json:"name"`
		Embedded
	}

	src := map[string]any{
		"mykey": "embedded_value",
		"value": 42,
		"name":  "parent_name",
	}

	var dest Parent
	require.NoError(t, maputils.Bind(src, &dest))

	// These should work with embedded structs
	assert.Equal(t, "embedded_value", dest.MyKey)
	assert.Equal(t, 42, dest.Value)
	assert.Equal(t, "parent_name", dest.Name)
}

func TestBind_NestedEmbeddedStruct(t *testing.T) {
	t.Parallel()

	type Level1 struct {
		Field1 string `json:"field1"`
	}

	type Level2 struct {
		Level1

		Field2 string `json:"field2"`
	}

	type Parent struct {
		Level2

		ParentField string `json:"parent_field"`
	}

	src := map[string]any{
		"field1":       "level1_value",
		"field2":       "level2_value",
		"parent_field": "parent_value",
	}

	var dest Parent
	require.NoError(t, maputils.Bind(src, &dest))

	assert.Equal(t, "level1_value", dest.Field1)
	assert.Equal(t, "level2_value", dest.Field2)
	assert.Equal(t, "parent_value", dest.ParentField)
}

func TestBind_EmbeddedStructWithPointer(t *testing.T) {
	t.Parallel()

	type Embedded struct {
		MyKey string `json:"mykey"`
	}

	type Parent struct {
		*Embedded

		Name string `json:"name"`
	}

	src := map[string]any{
		"mykey": "embedded_value",
		"name":  "parent_name",
	}

	var dest Parent
	require.NoError(t, maputils.Bind(src, &dest))

	assert.Equal(t, "parent_name", dest.Name)
	// Embedded pointer should be initialized and populated
	require.NotNil(t, dest.Embedded)
	assert.Equal(t, "embedded_value", dest.MyKey)
}

func TestUnbind_EmbeddedStruct(t *testing.T) {
	t.Parallel()

	type Embedded struct {
		MyKey string `json:"mykey"`
		Value int    `json:"value"`
	}

	type Parent struct {
		Name string `json:"name"`
		Embedded
	}

	src := Parent{
		Embedded: Embedded{
			MyKey: "embedded_value",
			Value: 42,
		},
		Name: "parent_name",
	}

	dest := make(map[string]any)
	require.NoError(t, maputils.Unbind(&src, dest))

	// All fields should be available at the top level
	assert.Equal(t, "embedded_value", dest["mykey"])
	assert.Equal(t, 42, dest["value"])
	assert.Equal(t, "parent_name", dest["name"])
}

func TestBind_DurationField(t *testing.T) {
	t.Parallel()

	type Struct struct {
		Int64    time.Duration
		Duration time.Duration
		String0  time.Duration
		String1  time.Duration
		String2  time.Duration
	}

	src := map[string]any{
		"int64":    int64(1 * time.Second),
		"duration": 2 * time.Minute,
		"string0":  "300ms",
		"string1":  "-1.5h",
		"string2":  "2h45m",
	}

	var dest Struct
	require.NoError(t, maputils.Bind(src, &dest))

	assert.Equal(t, 1*time.Second, dest.Int64)
	assert.Equal(t, 2*time.Minute, dest.Duration)
	assert.Equal(t, 300*time.Millisecond, dest.String0)
	assert.Equal(t, -(90 * time.Minute), dest.String1)
	assert.Equal(t, 165*time.Minute, dest.String2)
}

func TestBind_Array(t *testing.T) {
	t.Parallel()

	type Struct struct {
		Str []string
		Int []int
	}

	src := map[string]any{
		"str": "a,b,c",
		"int": "1,2,3",
	}

	var dest Struct
	require.NoError(t, maputils.Bind(src, &dest))

	assert.Equal(t, []string{"a", "b", "c"}, dest.Str)
	assert.Equal(t, []int{1, 2, 3}, dest.Int)
}

func TestBind_DurationFieldEmptyString(t *testing.T) {
	t.Parallel()

	type Struct struct {
		Timeout time.Duration
	}

	src := map[string]any{"timeout": ""}

	var dest Struct

	var err error

	require.NotPanics(t, func() {
		err = maputils.Bind(src, &dest)
	})
	require.Error(t, err, "an empty duration string must be reported, not panic")
	assert.ErrorIs(t, err, strconv.ErrSyntax)
	assert.Equal(t, time.Duration(0), dest.Timeout)
}

func TestBind_FixedSizeArray(t *testing.T) {
	t.Parallel()

	type Struct struct {
		Ports [3]int
	}

	src := map[string]any{"ports": []any{1, 2, 3}}

	var dest Struct
	require.NoError(t, maputils.Bind(src, &dest))

	assert.Equal(t, [3]int{1, 2, 3}, dest.Ports)
}

func TestBind_FixedSizeArrayWithFewerElements(t *testing.T) {
	t.Parallel()

	type Struct struct {
		Ports [3]int
	}

	src := map[string]any{"ports": []any{1, 2}}

	dest := Struct{Ports: [3]int{9, 9, 9}}

	var err error

	require.NotPanics(t, func() {
		err = maputils.Bind(src, &dest)
	})
	require.NoError(t, err)
	assert.Equal(
		t,
		[3]int{1, 2, 0},
		dest.Ports,
		"missing elements must be zeroed, like slice binding replaces wholesale",
	)
}

func TestBind_FixedSizeArrayWithMoreElements(t *testing.T) {
	t.Parallel()

	type Struct struct {
		Ports [2]int
	}

	src := map[string]any{"ports": []any{1, 2, 3}}

	dest := Struct{Ports: [2]int{9, 9}}

	var err error

	require.NotPanics(t, func() {
		err = maputils.Bind(src, &dest)
	})
	require.Error(
		t,
		err,
		"a source longer than the array must be reported, not panic or silently truncate",
	)
	assert.ErrorIs(t, err, maputils.ErrArrayTooSmall)
	assert.Equal(t, [2]int{9, 9}, dest.Ports, "a failed bind must leave the destination untouched")
}

func TestBind_NumericAndCSVConversions(t *testing.T) {
	t.Parallel()

	type Struct struct {
		CSV []string
		F64 float64
		F32 float32
		U16 uint16
		U8  uint8
		I8  int8
	}

	src := map[string]any{
		"u8":  "200",
		"u16": 65535,
		"f32": "1.5",
		"f64": 2.25,
		"i8":  -128,
		"csv": " a , b ,c",
	}

	var dest Struct
	require.NoError(t, maputils.Bind(src, &dest))

	assert.Equal(t, uint8(200), dest.U8)
	assert.Equal(t, uint16(65535), dest.U16)
	assert.InDelta(t, float32(1.5), dest.F32, 0)
	assert.InDelta(t, 2.25, dest.F64, 0)
	assert.Equal(t, int8(-128), dest.I8)
	assert.Equal(t, []string{"a", "b", "c"}, dest.CSV)
}

func TestBind_NumericOverflow(t *testing.T) {
	t.Parallel()

	tests := []struct {
		wantErr error
		value   any
		bind    func(src map[string]any) error
		name    string
	}{
		{
			name:    "int8 above range",
			value:   200,
			wantErr: maputils.ErrIntegerOverflow,
			bind: func(src map[string]any) error {
				var d struct{ V int8 }
				return maputils.Bind(src, &d)
			},
		},
		{
			name:    "int16 below range",
			value:   "-40000",
			wantErr: maputils.ErrIntegerOverflow,
			bind: func(src map[string]any) error {
				var d struct{ V int16 }
				return maputils.Bind(src, &d)
			},
		},
		{
			name:    "uint8 above range",
			value:   256,
			wantErr: maputils.ErrUnsignedIntegerOverflow,
			bind: func(src map[string]any) error {
				var d struct{ V uint8 }
				return maputils.Bind(src, &d)
			},
		},
		{
			name:    "uint16 from string above range",
			value:   "70000",
			wantErr: maputils.ErrUnsignedIntegerOverflow,
			bind: func(src map[string]any) error {
				var d struct{ V uint16 }
				return maputils.Bind(src, &d)
			},
		},
		{
			name:    "uint from negative int",
			value:   -1,
			wantErr: maputils.ErrNegativeIntCannotConvert,
			bind: func(src map[string]any) error {
				var d struct{ V uint }
				return maputils.Bind(src, &d)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.bind(map[string]any{"v": tt.value})
			assert.ErrorIs(t, err, tt.wantErr)
		})
	}
}

func TestBind_TagMatchBeatsNameMatchDeterministically(t *testing.T) {
	t.Parallel()

	type Inner struct {
		Precedent string `json:"other"`
		Plain     string
	}

	type Outer struct {
		Precedent string `json:"other"`
		Inner     Inner  `json:"inner"`
	}

	tests := []struct {
		name      string
		src       map[string]any
		wantOuter string
		wantInner string
		wantPlain string
	}{
		{
			name: "tag beats name at top level and nested",
			src: map[string]any{
				"precedent": "by-name",
				"other":     "by-tag",
				"inner":     map[string]any{"precedent": "by-name", "other": "by-tag"},
			},
			wantOuter: "by-tag",
			wantInner: "by-tag",
		},
		{
			name: "case-folded tag beats exact name in nested struct",
			src: map[string]any{
				"inner": map[string]any{"precedent": "by-name", "OTHER": "by-tag"},
			},
			wantInner: "by-tag",
		},
		{
			name: "exact name beats case-folded name in nested struct",
			src: map[string]any{
				"inner": map[string]any{"PLAIN": "folded", "plain": "exact", "Plain": "folded"},
			},
			wantPlain: "exact",
		},
		{
			name: "smallest key wins among case-folded names in nested struct",
			src: map[string]any{
				"inner": map[string]any{"Plain": "second", "PLAIN": "first"},
			},
			wantPlain: "first",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			for range 100 {
				var got Outer
				require.NoError(t, maputils.Bind(tt.src, &got))
				assert.Equal(t, tt.wantOuter, got.Precedent)
				assert.Equal(t, tt.wantInner, got.Inner.Precedent)
				assert.Equal(t, tt.wantPlain, got.Inner.Plain)
			}
		})
	}
}
