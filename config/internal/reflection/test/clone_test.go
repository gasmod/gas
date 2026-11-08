package reflection_test

import (
	"math"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/config/internal/reflection"
)

type m = map[string]any

type (
	names  map[string][]string
	list   []names
	intPtr *int
)

func TestClone_NilInterface(t *testing.T) {
	t.Parallel()

	assert.Nil(t, reflection.Clone[any](nil))
}

// A nil map, slice or pointer stays nil, of its type.
func TestClone_TypedNil(t *testing.T) {
	t.Parallel()

	for _, v := range []any{(map[string]int)(nil), ([]string)(nil), (*int)(nil)} {
		got := reflection.Clone(v)
		assert.IsType(t, v, got)
		assert.Nil(t, got)
	}
}

// Anything but a map, slice or pointer is returned as it is.
func TestClone_OtherValuesAsIs(t *testing.T) {
	t.Parallel()

	ch := make(chan int)
	for _, v := range []any{"s", 42, 1.5, true, [2]int{1, 2}, struct{ A int }{A: 1}, ch} {
		assert.Equal(t, v, reflection.Clone(v))
	}

	f := func() {}
	assert.Equal(
		t,
		reflect.ValueOf(f).Pointer(),
		reflect.ValueOf(reflection.Clone(any(f))).Pointer(),
	)
}

// A map is copied at every depth, so changing the copy leaves the original
// as it was.
func TestClone_MapIsDeep(t *testing.T) {
	t.Parallel()

	src := m{"db": m{"hosts": []any{"a", "b"}, "port": 5432}}

	got := reflection.Clone(src)
	require.Equal(t, src, got)

	db := got["db"].(m)

	db["port"] = 1
	db["hosts"].([]any)[0] = "x"
	got["new"] = true

	assert.Equal(t, m{"db": m{"hosts": []any{"a", "b"}, "port": 5432}}, src)
}

func TestClone_MapWithNilValue(t *testing.T) {
	t.Parallel()

	src := map[string]any{"host": "localhost", "port": nil}

	got := reflection.Clone(src)

	require.Contains(t, got, "port", "key with a nil value must survive cloning")
	assert.Nil(t, got["port"])
	assert.Equal(t, src, got)
}

func TestClone_NestedMapWithNilValue(t *testing.T) {
	t.Parallel()

	src := map[string]any{"db": map[string]any{"host": "localhost", "port": nil}}

	got := reflection.Clone(src)

	inner, ok := got["db"].(map[string]any)
	require.True(t, ok)
	require.Contains(t, inner, "port", "nested key with a nil value must survive cloning")
	assert.Equal(t, src, got)
}

func TestClone_MapWithNilSliceAndMapValues(t *testing.T) {
	t.Parallel()

	var (
		nilSlice []string
		nilMap   map[string]string
	)

	src := map[string]any{"slice": nilSlice, "map": nilMap}

	got := reflection.Clone(src)

	require.Contains(t, got, "slice", "key with a typed-nil slice must survive cloning")
	require.Contains(t, got, "map", "key with a typed-nil map must survive cloning")
	assert.Equal(t, src, got)
}

// Every entry is copied, including one under a NaN key, which no lookup
// can find.
func TestClone_MapNaNKeys(t *testing.T) {
	t.Parallel()

	src := map[float64]int{math.NaN(): 1, math.NaN(): 2}

	got := reflection.Clone(src)
	require.Len(t, got, 2)

	values := make([]int, 0, len(got))
	for k, v := range got {
		assert.True(t, math.IsNaN(k))

		values = append(values, v)
	}

	assert.ElementsMatch(t, []int{1, 2}, values)
}

// Keys are used as they are, so a pointer key still finds its entry, and
// the copy is equal to the original.
func TestClone_MapKeysAsIs(t *testing.T) {
	t.Parallel()

	k := new(int)

	src := map[*int]string{k: "a"}
	got := reflection.Clone(src)
	assert.Equal(t, "a", got[k])
	assert.Equal(t, src, got)

	srcAny := map[any]string{k: "a"}
	gotAny := reflection.Clone(srcAny)
	assert.Equal(t, "a", gotAny[k])
	assert.Equal(t, srcAny, gotAny)
}

// A slice is copied to one of the same length and capacity, at every
// depth, with its own backing array.
func TestClone_SliceIsDeep(t *testing.T) {
	t.Parallel()

	src := make([]any, 2, 5)

	src[0] = m{"a": 1}
	src[1] = []string{"x"}

	got := reflection.Clone(src)
	require.Equal(t, src, got)
	assert.Equal(t, 5, cap(got))
	assert.NotSame(t, &src[0], &got[0])

	got[0].(m)["a"] = 2
	got[1].([]string)[0] = "y"

	assert.Equal(t, m{"a": 1}, src[0])
	assert.Equal(t, []string{"x"}, src[1])
}

// A slice of maps, slices or pointers gets a copy of each element, so
// changing one in the copy leaves the original as it was.
func TestClone_SliceOfReferencesIsDeep(t *testing.T) {
	t.Parallel()

	maps := []map[string]int{{"a": 1}}

	reflection.Clone(maps)[0]["a"] = 2
	assert.Equal(t, 1, maps[0]["a"])

	slices := [][]int{{1}}

	reflection.Clone(slices)[0][0] = 2
	assert.Equal(t, 1, slices[0][0])

	n := 1
	ptrs := []*int{&n}

	*reflection.Clone(ptrs)[0] = 2

	assert.Equal(t, 1, n)
}

// A slice of bytes keeps its length and capacity and gets its own backing
// array.
func TestClone_ByteSlice(t *testing.T) {
	t.Parallel()

	src := make([]byte, 3, 8)
	copy(src, "abc")

	got := reflection.Clone(src)
	require.Equal(t, src, got)
	assert.Len(t, got, 3)
	assert.Equal(t, 8, cap(got))

	got[0] = 'x'

	assert.Equal(t, []byte("abc"), src)
}

func TestClone_SliceWithNilElement(t *testing.T) {
	t.Parallel()

	src := []any{"a", nil, "b"}

	require.NotPanics(t, func() {
		got := reflection.Clone(src)
		assert.Equal(t, src, got)
	})
}

// A pointer is copied to a new pointer to a copy of its target, at every
// depth.
func TestClone_Pointer(t *testing.T) {
	t.Parallel()

	n := 1
	got := reflection.Clone(&n)
	require.Equal(t, 1, *got)

	*got = 2

	assert.Equal(t, 1, n)

	inner := m{"a": 1}
	outer := &inner
	gotPP := reflection.Clone(&outer)
	require.NotSame(t, outer, *gotPP)

	(**gotPP)["a"] = 2

	assert.Equal(t, m{"a": 1}, inner)
}

// A pointer to an interface is copied with a copy of the value the
// interface holds, and one that holds nil still holds nil.
func TestClone_PointerToInterface(t *testing.T) {
	t.Parallel()

	empty := new(any)
	got := reflection.Clone(empty)
	require.NotSame(t, empty, got)
	assert.Nil(t, *got)

	full := new(any)

	*full = m{"a": 1}

	gotFull := reflection.Clone(full)

	(*gotFull).(m)["a"] = 2

	assert.Equal(t, m{"a": 1}, *full)
}

// The copy has the types of the original at every depth, also when it is
// passed as an interface.
func TestClone_KeepsTypes(t *testing.T) {
	t.Parallel()

	src := list{names{"a": {"x"}}}

	got := reflection.Clone(src)
	assert.Equal(t, src, got)

	gotAny := reflection.Clone(any(src))
	assert.IsType(t, list{}, gotAny)
	assert.Equal(t, src, gotAny)
}

// A named pointer type is kept like a named map or slice type, also when
// it is passed as an interface or held in one.
func TestClone_KeepsNamedPointerType(t *testing.T) {
	t.Parallel()

	n := 1
	src := intPtr(&n)

	assert.NotPanics(t, func() {
		got := reflection.Clone(src)

		*got = 2

		assert.Equal(t, 1, n)
	})

	assert.IsType(t, src, reflection.Clone(any(src)))
	assert.IsType(t, src, reflection.Clone([]any{src})[0])
	assert.IsType(t, src, reflection.Clone(map[string]any{"p": src})["p"])
}

// Two references to one map become two copies.
func TestClone_SharedMapBecomesTwoCopies(t *testing.T) {
	t.Parallel()

	inner := m{"a": 1}

	got := reflection.Clone([]any{inner, inner})

	got[0].(m)["a"] = 2

	assert.Equal(t, 1, got[1].(m)["a"])
}

// A struct or array is copied the way Go copies it, so a map inside it is
// shared with the original, also behind a pointer or in a slice.
func TestClone_StructAndArrayShareTheirMaps(t *testing.T) {
	t.Parallel()

	type withMap struct{ M map[string]int }

	src := withMap{M: map[string]int{"a": 1}}

	reflection.Clone(src).M["a"] = 2
	assert.Equal(t, 2, src.M["a"])

	ptr := &withMap{M: map[string]int{"a": 1}}
	got := reflection.Clone(ptr)
	require.NotSame(t, ptr, got)

	got.M["a"] = 2
	assert.Equal(t, 2, ptr.M["a"])

	arr := [1]map[string]int{{"a": 1}}

	reflection.Clone(arr)[0]["a"] = 2
	assert.Equal(t, 2, arr[0]["a"])

	elems := []withMap{{M: map[string]int{"a": 1}}}

	reflection.Clone(elems)[0].M["a"] = 2
	assert.Equal(t, 2, elems[0].M["a"])

	type hidden struct{ m map[string]int }

	hptr := &hidden{m: map[string]int{"a": 1}}
	gotHidden := reflection.Clone(hptr)
	require.NotSame(t, hptr, gotHidden)
	assert.Equal(t, hptr, gotHidden)
}
