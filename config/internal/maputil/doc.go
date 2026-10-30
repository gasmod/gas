// Package maputil holds the map operations behind the config module. It
// normalizes the keys of the maps that providers return, merges them into
// one tree, finds a nested map in that tree by its path, and binds the tree
// into structs and back.
//
// # Usage
//
// The config module uses the package in this order:
//
//	values, err := maputil.NormalizeKeys(loaded) // keys lowercased, a copy
//	maputil.Merge(tree, values)                  // values win over tree
//	err = maputil.Bind(tree, &cfg)               // fill a struct from tree
//
// None of the functions lock; the config module holds its own lock around
// them. The maps and values passed in must not contain themselves: a cycle
// recurses without end.
//
// # Keys
//
// [NormalizeKeys] returns a copy of a map with each key trimmed with
// [strings.TrimSpace] and lowercased with [strings.ToLower], at every
// depth:
//
//   - A map of any type becomes a map[string]any. A key that is not a string
//     is formatted first: a [time.Duration] with its unit, such as `1m30s`,
//     as Bind takes no duration without one; a bool or a number by its
//     kind, as [strconv] formats it, so a [time.Weekday] key is `1` and not
//     `Monday`; and anything else with [fmt.Sprint].
//   - The maps inside a []any are normalized too, at any depth. JSON decodes
//     to maps and []any only, so a decoded document is normalized all the
//     way down.
//   - Any other value is copied as it is, including a slice of another type:
//     the maps in a []map[string]any keep their keys.
//   - A key that is empty after trimming is left out with its value.
//
// The copy shares no maps, slices or pointers with the input, except a
// pointer used as a map key, which stays the same key, so a provider may
// keep and change what it returned. A struct or array value is copied the
// way Go copies it.
//
// Two keys of one map conflict when they become the same key, such as
// `Host` and `host`, or `host` and ` host`. A conflict is an error rather
// than a guess at which value wins. Keys are compared in sorted order, and
// all of a map's keys before any of its values, so the same input always
// reports the same conflict: one among a map's keys before one inside its
// values.
//
// # Merging
//
// [Merge] merges one tree into another, the second winning. Where both
// trees hold a map[string]any under a key, the two maps are merged the same
// way, key by key. Anywhere else the second tree's value replaces the
// first's: a map replaces a value, a value replaces a map, a list replaces a
// list as a whole, and nil replaces anything. A nil map[string]any in the
// second tree holds nothing, so it leaves a map in the first tree as it is.
//
// [MergeWithoutOverride] is the reverse, the first tree winning. It adds
// the keys the first tree does not hold, merges two maps under one key, and
// leaves every other key the first tree holds as it is, even one holding
// nil.
//
// In both, a nil map[string]any in the first tree holds nothing: the second
// tree's map under its key takes its place, as it would fill an empty map.
//
// Neither function normalizes keys, so the config module normalizes each
// source before merging it, and each source is checked for conflicts on its
// own: `Host` in one provider and `host` in a later one are not a conflict,
// and the later one wins. Neither copies, so the tree takes the values it
// is given.
//
// # Paths
//
// [FindNestedMap] follows a path, one key per level, and returns the
// map[string]any at its end. It can add the maps that are missing along the
// way, and replace a nil map[string]any, which holds nothing, with an empty
// one, but it never replaces a value that is not a map, and it returns nil
// instead.
//
// # Binding
//
// [Bind] fills a struct from a tree. Each key that matches a field sets
// that field; a field without a key keeps its value, and a key without a
// field is ignored. The usual pattern is to fill a struct with defaults and
// bind over it.
//
// The fields and their names are the ones [encoding/json] uses:
//
//   - A field's name is its json tag name, the text before the first comma,
//     or its Go name when it has no tag or the tag has no name. A tagged
//     field matches its tag name only. Tag options such as `omitempty` are
//     ignored. A tag name with a quote or a backslash is taken as written,
//     where encoding/json reads it by rules of its own, which differ
//     between its versions; such a name is the one place the two differ.
//   - A field tagged `json:"-"` and an unexported field are left out.
//   - The fields of an embedded struct are promoted to the struct that embeds
//     it, as Go promotes them, so they bind at the same level as its own
//     fields. This holds for an embedded pointer to a struct, which is
//     allocated when one of its fields is set, and for an embedded struct of
//     an unexported type, whose exported fields are promoted. An embedded
//     struct with a tag name is a field like any other, even of an
//     unexported type, as encoding/json keeps it. Such a field cannot be
//     set as a whole, only its exported fields can, so it takes only a map,
//     and a nil pointer to one cannot be allocated.
//   - Of several fields with one name, the shallowest one wins. At equal
//     depth, a tagged field wins over untagged ones, and otherwise none of
//     them is a field, as Go makes such a name ambiguous.
//
// A key matches the field with exactly its name, or else the field whose
// name it equals without regard to case, comparing both lowercased with
// [strings.ToLower]. So `host`, `HOST` and `Host` all match a field named
// `Host`, and `name` matches a field tagged `json:"name"` rather than one
// named `Name`. Two keys that match one field, such as `Host` and `host`,
// are a conflict and an error, as is a key that matches several fields
// without regard to case and none of them exactly, such as `url` for fields
// named `URL` and `Url`.
//
// # Values
//
// A value whose Go type is assignable to the field's type, such as a
// `[]string` for a `[]string` field or an `http.SameSite` for an
// `http.SameSite` field, is copied into the field as it is. Bind copies
// every map, slice and pointer it takes from the tree, at every depth,
// except a pointer used as a map key, which stays the same key, so changing
// the struct afterwards does not change the tree. A struct or array value
// is copied the way Go copies it.
//
// Any other value is converted to the field's type:
//
//   - nil sets the field to its zero value: a pointer, map or slice to nil.
//   - A pointer field is allocated when it is nil, and the value is bound
//     into what it points to. A pointer that is not nil is reused.
//   - A struct field takes a map[string]any, bound into it by the same
//     rules, so fields without a key keep their values. It also takes a
//     struct of a type that Go converts to the field's type.
//   - A map field takes a map with string keys, which replaces the field's
//     map as a whole. Each key is converted to the map's key type the way a
//     string value is converted below, and two keys that convert to the same
//     one, such as `1` and `01` for a map[int]string, are a conflict.
//   - A slice field takes a list, which replaces the field's slice as a
//     whole, element by element. It also takes a string, split on `,` with
//     each element trimmed: `"a, b ,c"` is three elements and `"a,,b"`
//     three with an empty one in the middle. A string that is empty after
//     trimming is an empty slice, of any element type. A []byte is a slice
//     like any other.
//   - An array field takes a list no longer than the array, which replaces
//     the array as a whole; elements past the end of the list are zero. A
//     string is not split for an array.
//   - An interface field takes only values of types that implement it.
//
// The kinds below convert by kind, so a named type converts like its
// underlying type:
//
//   - A bool takes a bool, a string that [strconv.ParseBool] accepts, or a
//     number, which is true when it is not zero.
//   - A string takes a string or a []byte as its text, and formats a bool or
//     a number with [fmt.Sprint], except a float without a String method,
//     which is written without an exponent: 1234567.0 is `1234567` and
//     1e-7 is `0.0000001`. A complex number is not converted, to a string
//     or any other type.
//   - An integer takes an integer, a float with no fractional part, or a
//     decimal string with an optional sign, such as `8080`, `+8080` or
//     `-1`, for signed and unsigned types alike. A number that does not fit
//     the field's type, or a negative one for an unsigned type, is an
//     error, as is a float with a fractional part or one that is not
//     finite. `-0` is 0.
//   - A float takes a number or a string that [strconv.ParseFloat] accepts,
//     rounded to the field's size. A finite number that rounds past the
//     largest float32 is an error for a float32 field; one that rounds to
//     it, such as `3.4028235e+38`, the shortest text for
//     [math.MaxFloat32], is not.
//   - A [time.Duration] takes a string that [time.ParseDuration] accepts,
//     so it needs a unit: `"30s"`, `"250ms"` and `"1h30m"` work, and `"0"`
//     is zero. A string without a unit such as `"30"`, or a number other than
//     0, is an error that says to add one. A [time.Duration] value is used
//     as it is.
//
// Anything else is an error, such as a map for a string field, a string for
// a struct field, or a bool for an integer.
//
// # Unbinding
//
// [Unbind] writes a struct into a map, the reverse of Bind, for structs
// passed as defaults. It writes the fields Bind reads, each under its name,
// with its case kept. Bind reads the map back into a struct with the same
// fields Bind reads, as long as every map field has keys of a string, bool
// or real number type and no tagged embedded field is a pointer to a struct
// of an unexported type, which Bind cannot allocate. Unexported fields are
// not written, so they do not come back. A map or slice inside a struct or
// array value is shared, as that value is copied the way Go copies it.
// After [NormalizeKeys], the keys of a map field are lowercased like any
// other.
//
//   - A struct becomes a map[string]any. A struct with no fields to write,
//     such as [time.Time], is copied as it is instead, unless it is an
//     embedded struct of an unexported type, which cannot be read as a
//     whole and becomes an empty map.
//   - A map becomes a map[string]any, with a key that is not a string
//     formatted as [NormalizeKeys] formats it, so a duration, bool or number
//     key reads back. Two keys that format the same, such as the
//     int 1 and the string "1", are a conflict.
//   - A slice or an array becomes a []any.
//   - A pointer is followed, and a nil pointer, map, slice or interface
//     becomes nil.
//   - The fields of a nil embedded pointer are left out.
//   - Anything else is copied as it is, including the value of an interface
//     field.
//
// # Errors
//
// Errors wrap one of the package's sentinel errors, to be tested with
// [errors.Is]:
//
//   - [ErrKeyConflict]: two keys become the same key, as described above.
//   - [ErrInvalidDest]: Bind's destination is not a non-nil pointer to a
//     struct, Unbind's map is nil, an embedded pointer to an unexported
//     struct is nil and cannot be allocated, or a tagged embedded struct of
//     an unexported type gets something other than a map.
//   - [ErrInvalidSrc]: Unbind's source is not a struct or a non-nil pointer
//     to one.
//   - [ErrConvert]: a value has no conversion to its field's type, a string
//     does not parse as one, or a float for an integer is not a whole
//     number.
//   - [ErrOutOfRange]: a number does not fit its field's type.
//   - [ErrDurationWithoutUnit]: a duration has no unit.
//   - [ErrArrayTooSmall]: a list is longer than its array.
//
// An error starts with the path of the key it is about, with `.` between
// keys and `[i]` for a list element, as the keys are written in the tree.
// It names Go types but never includes a value, so a secret bound to the
// wrong field does not leak into logs:
//
//	conflicting keys: "Host" and "host"
//	database: conflicting keys: "Host" and "host"
//	notes.timeout: duration has no unit; add one such as "s" or "ms"
//	server.port: cannot convert string to int: invalid syntax
//	server.port: value out of range for uint16
//	ui.staticpaths[2]: cannot convert map[string]any to string
//	ports: too many elements for array [2]int: got 3
//
// Bind stops at the first error, and fields set before it keep their new
// values. Fields are set in the order they are declared, so the same input
// always reports the same error.
package maputil
