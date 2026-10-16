// Package maputils holds the map operations behind gas/config: binding a
// nested map into a struct and back, merging maps, normalizing keys and
// walking dotted paths.
//
// Bind fills a struct from a map[string]any. A field matches by its json tag
// first, then by its lowercased name; nested structs, embedded structs,
// slices, arrays, maps and pointers are filled recursively, pointers are
// allocated as needed, and common scalar types, time.Duration and strings
// convert into one another. A key with no matching field is ignored, and a
// field with no key is left as it is. Unbind does the reverse, for
// Config.SetDefaults.
//
// Merge deep-merges one map into another, later values winning;
// MergeWithoutOverride only fills keys that are missing. NormalizeKeys
// lowercases and trims keys at every level, and FindNestedMap walks a dotted
// path, optionally creating the maps along it. None of these functions lock;
// gas/config holds its own lock around them.
package maputils
