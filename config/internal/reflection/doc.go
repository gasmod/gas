// Package reflection holds [Clone], the deep copy behind the config
// module. Config.Get, Find and Values return a copy of what the
// configuration holds, so a caller that changes the result does not change
// the configuration, and package maputil uses it to copy values into the
// structs and maps it builds.
//
// # Usage
//
// Clone takes a value and returns a copy of the same type:
//
//	tree := map[string]any{"db": map[string]any{"hosts": []any{"a", "b"}}}
//	c := reflection.Clone(tree)
//	c["db"].(map[string]any)["hosts"].([]any)[0] = "x" // tree is unchanged
//
// Clone does not lock. Nothing may change v while it is copied.
//
// # What is copied
//
// Clone copies maps, slices and pointers, at every depth, and keeps the
// type of each, named types included:
//
//   - A map becomes a new map with the same keys and a copy of each value.
//     Every entry is copied, including one under a NaN key, which no lookup
//     can find.
//   - A slice becomes a new slice of the same length and capacity, with a
//     copy of each element up to its length.
//   - A pointer becomes a new pointer to a copy of what it points to. A
//     pointer to a type Go cannot allocate, such as an incomplete C type,
//     is returned as it is.
//   - An interface inside a map, slice or pointer, such as an element of a
//     []any, holds a copy of the value it held.
//   - A nil map, slice, pointer or interface stays nil, of its type.
//
// Map keys are kept as they are, not copied, so the copy is equal to the
// original and a pointer key still finds its entry.
//
// Anything else is returned as it is: a string, number, bool, func or
// channel, and a struct or array, which is copied the way Go copies it. A
// map, slice or pointer inside a struct or array is therefore shared with
// the original, also when the struct is behind a pointer. For the same
// reason, a pointer to a type that must not be copied by value yields a
// broken copy: changing a copied [math/big.Int] can change the original,
// and a copy of a non-zero [strings.Builder] panics when written to.
//
// Two references to one map, slice or pointer target in v become two
// separate copies; the copy does not keep that they were one.
//
// # Limits
//
// v must not contain itself: a map, slice or pointer that leads back to
// itself recurses without end, and the program crashes when the stack runs
// out. Clone returns no error and does not panic otherwise.
package reflection
