package internal

import (
	"iter"
	"maps"
	"slices"
)

// OrderedMap is a map that iterates in insertion order. The zero value is an
// empty map ready to use. It is not safe for concurrent use.
//
// Copying an OrderedMap by value shares its storage with the original; use
// Clone for an independent copy.
type OrderedMap[K comparable, V any] struct {
	m    map[K]V
	keys []K
}

// Len returns the number of entries.
func (m *OrderedMap[K, V]) Len() int {
	return len(m.m)
}

// Has reports whether k is present.
func (m *OrderedMap[K, V]) Has(k K) bool {
	_, ok := m.m[k]
	return ok
}

// Get returns the value for k and whether k is present.
func (m *OrderedMap[K, V]) Get(k K) (V, bool) {
	v, ok := m.m[k]
	return v, ok
}

// Set stores v under k. A new key goes to the end of the iteration order; an
// existing key keeps its position.
func (m *OrderedMap[K, V]) Set(k K, v V) {
	if m.m == nil {
		m.m = make(map[K]V)
	}
	if _, ok := m.m[k]; !ok {
		m.keys = append(m.keys, k)
	}
	m.m[k] = v
}

// Remove deletes k, if present. It takes time linear in Len.
func (m *OrderedMap[K, V]) Remove(k K) {
	if _, ok := m.m[k]; !ok {
		return
	}
	delete(m.m, k)
	if i := slices.Index(m.keys, k); i >= 0 {
		m.keys = slices.Delete(m.keys, i, i+1)
	}
}

// All returns an iterator over the entries in insertion order.
func (m *OrderedMap[K, V]) All() iter.Seq2[K, V] {
	return func(yield func(K, V) bool) {
		for _, k := range m.keys {
			if !yield(k, m.m[k]) {
				return
			}
		}
	}
}

// Keys returns an iterator over the keys in insertion order.
func (m *OrderedMap[K, V]) Keys() iter.Seq[K] {
	return func(yield func(K) bool) {
		for _, k := range m.keys {
			if !yield(k) {
				return
			}
		}
	}
}

// Values returns an iterator over the values in insertion order.
func (m *OrderedMap[K, V]) Values() iter.Seq[V] {
	return func(yield func(V) bool) {
		for _, k := range m.keys {
			if !yield(m.m[k]) {
				return
			}
		}
	}
}

// Clone returns a copy that shares no storage with m. The values themselves
// are copied by assignment.
func (m *OrderedMap[K, V]) Clone() *OrderedMap[K, V] {
	return &OrderedMap[K, V]{m: maps.Clone(m.m), keys: slices.Clone(m.keys)}
}
