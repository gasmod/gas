// Package reflection holds reflection helpers for gas/config.
//
// Clone returns a deep copy of maps, slices and pointers, recursively, which
// is how Config.Get, Find and Values keep callers from changing the stored
// configuration. Other values are returned as they are; a struct or array is
// copied by value, so a map or slice inside one is still shared. IsNil
// reports whether a value is nil, including a typed nil pointer, slice, map,
// channel, function or interface.
package reflection
