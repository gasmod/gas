package internal_test

import (
	"slices"
	"testing"

	"github.com/gasmod/gas/internal"
)

func keys[K comparable, V any](m *internal.OrderedMap[K, V]) []K {
	return slices.Collect(m.Keys())
}

func TestOrderedMap_ZeroValue(t *testing.T) {
	var m internal.OrderedMap[string, int]

	if got := m.Len(); got != 0 {
		t.Errorf("Len() = %d, want 0", got)
	}
	if m.Has("a") {
		t.Error(`Has("a") = true, want false`)
	}
	if v, ok := m.Get("a"); v != 0 || ok {
		t.Errorf(`Get("a") = (%d, %t), want (0, false)`, v, ok)
	}
	m.Remove("a")
	if got := keys(&m); len(got) != 0 {
		t.Errorf("Keys() = %v, want empty", got)
	}
}

func TestOrderedMap_SetAppendsInInsertionOrder(t *testing.T) {
	var m internal.OrderedMap[string, int]
	m.Set("c", 3)
	m.Set("a", 1)
	m.Set("b", 2)

	if got := m.Len(); got != 3 {
		t.Errorf("Len() = %d, want 3", got)
	}
	if got, want := keys(&m), []string{"c", "a", "b"}; !slices.Equal(got, want) {
		t.Errorf("Keys() = %v, want %v", got, want)
	}
	if got, want := slices.Collect(m.Values()), []int{3, 1, 2}; !slices.Equal(got, want) {
		t.Errorf("Values() = %v, want %v", got, want)
	}
}

func TestOrderedMap_SetExistingKeepsPosition(t *testing.T) {
	var m internal.OrderedMap[string, int]
	m.Set("a", 1)
	m.Set("b", 2)
	m.Set("c", 3)
	m.Set("a", 10)

	if got := m.Len(); got != 3 {
		t.Errorf("Len() = %d, want 3", got)
	}
	if got, want := keys(&m), []string{"a", "b", "c"}; !slices.Equal(got, want) {
		t.Errorf("Keys() = %v, want %v", got, want)
	}
	if v, ok := m.Get("a"); v != 10 || !ok {
		t.Errorf(`Get("a") = (%d, %t), want (10, true)`, v, ok)
	}
}

func TestOrderedMap_GetAndHas(t *testing.T) {
	var m internal.OrderedMap[string, *int]
	one := 1
	m.Set("a", &one)
	m.Set("nil", nil)

	if v, ok := m.Get("a"); v != &one || !ok {
		t.Errorf(`Get("a") = (%v, %t), want (%v, true)`, v, ok, &one)
	}
	// A present key with a zero value is distinguishable from a missing key.
	if v, ok := m.Get("nil"); v != nil || !ok {
		t.Errorf(`Get("nil") = (%v, %t), want (nil, true)`, v, ok)
	}
	if !m.Has("nil") {
		t.Error(`Has("nil") = false, want true`)
	}
	if v, ok := m.Get("missing"); v != nil || ok {
		t.Errorf(`Get("missing") = (%v, %t), want (nil, false)`, v, ok)
	}
	if m.Has("missing") {
		t.Error(`Has("missing") = true, want false`)
	}
}

func TestOrderedMap_Remove(t *testing.T) {
	tests := []struct {
		name   string
		remove string
		want   []string
	}{
		{"first", "a", []string{"b", "c", "d"}},
		{"middle", "b", []string{"a", "c", "d"}},
		{"last", "d", []string{"a", "b", "c"}},
		{"missing", "z", []string{"a", "b", "c", "d"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var m internal.OrderedMap[string, int]
			for i, k := range []string{"a", "b", "c", "d"} {
				m.Set(k, i)
			}

			m.Remove(tt.remove)

			if got := keys(&m); !slices.Equal(got, tt.want) {
				t.Errorf("Keys() = %v, want %v", got, tt.want)
			}
			if got := m.Len(); got != len(tt.want) {
				t.Errorf("Len() = %d, want %d", got, len(tt.want))
			}
			if tt.remove != "z" && m.Has(tt.remove) {
				t.Errorf("Has(%q) = true after Remove", tt.remove)
			}
		})
	}
}

func TestOrderedMap_RemoveThenSetMovesToEnd(t *testing.T) {
	var m internal.OrderedMap[string, int]
	m.Set("a", 1)
	m.Set("b", 2)
	m.Set("c", 3)

	m.Remove("a")
	m.Set("a", 4)

	if got, want := keys(&m), []string{"b", "c", "a"}; !slices.Equal(got, want) {
		t.Errorf("Keys() = %v, want %v", got, want)
	}
	if v, _ := m.Get("a"); v != 4 {
		t.Errorf(`Get("a") = %d, want 4`, v)
	}
}

func TestOrderedMap_All(t *testing.T) {
	var m internal.OrderedMap[string, int]
	m.Set("b", 2)
	m.Set("a", 1)

	var gotK []string
	var gotV []int
	for k, v := range m.All() {
		gotK = append(gotK, k)
		gotV = append(gotV, v)
	}

	if want := []string{"b", "a"}; !slices.Equal(gotK, want) {
		t.Errorf("All() keys = %v, want %v", gotK, want)
	}
	if want := []int{2, 1}; !slices.Equal(gotV, want) {
		t.Errorf("All() values = %v, want %v", gotV, want)
	}
}

func TestOrderedMap_IteratorsStopOnBreak(t *testing.T) {
	var m internal.OrderedMap[string, int]
	m.Set("a", 1)
	m.Set("b", 2)
	m.Set("c", 3)

	t.Run("All", func(t *testing.T) {
		n := 0
		for range m.All() {
			n++
			break
		}
		if n != 1 {
			t.Errorf("visited %d entries, want 1", n)
		}
	})
	t.Run("Keys", func(t *testing.T) {
		n := 0
		for range m.Keys() {
			n++
			break
		}
		if n != 1 {
			t.Errorf("visited %d keys, want 1", n)
		}
	})
	t.Run("Values", func(t *testing.T) {
		n := 0
		for range m.Values() {
			n++
			break
		}
		if n != 1 {
			t.Errorf("visited %d values, want 1", n)
		}
	})
}

func TestOrderedMap_Clone(t *testing.T) {
	var m internal.OrderedMap[string, int]
	m.Set("b", 2)
	m.Set("a", 1)

	c := m.Clone()
	if got, want := keys(c), []string{"b", "a"}; !slices.Equal(got, want) {
		t.Errorf("clone Keys() = %v, want %v", got, want)
	}

	// Mutating either side must not show through on the other.
	c.Set("a", 10)
	c.Set("c", 3)
	c.Remove("b")
	m.Set("d", 4)

	if got, want := keys(&m), []string{"b", "a", "d"}; !slices.Equal(got, want) {
		t.Errorf("original Keys() = %v, want %v", got, want)
	}
	if v, _ := m.Get("a"); v != 1 {
		t.Errorf(`original Get("a") = %d, want 1`, v)
	}
	if got, want := keys(c), []string{"a", "c"}; !slices.Equal(got, want) {
		t.Errorf("clone Keys() = %v, want %v", got, want)
	}
	if v, _ := c.Get("a"); v != 10 {
		t.Errorf(`clone Get("a") = %d, want 10`, v)
	}
}

func TestOrderedMap_CloneZeroValue(t *testing.T) {
	var m internal.OrderedMap[string, int]

	c := m.Clone()
	if got := c.Len(); got != 0 {
		t.Errorf("Len() = %d, want 0", got)
	}
	c.Set("a", 1)
	if m.Has("a") {
		t.Error(`original Has("a") = true after Set on clone`)
	}
	if got, want := keys(c), []string{"a"}; !slices.Equal(got, want) {
		t.Errorf("clone Keys() = %v, want %v", got, want)
	}
}
