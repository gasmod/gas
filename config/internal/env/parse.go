package env

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// ErrKeyConflict indicates two variables that map to the same key, or one
// variable that maps to a key another one needs as a nested map.
var ErrKeyConflict = errors.New("conflicting variable names")

// pathSep joins the levels of a key for bookkeeping. It cannot occur in an
// environment variable name, which ends at the first NUL.
const pathSep = "\x00"

// ParseVariables turns variable names and values into a nested map, as
// described in the package documentation. Only names starting with prefix
// are kept, and the prefix is removed; an empty prefix keeps every name. sep
// splits a name into levels; an empty sep keeps names flat. Both are matched
// without regard to case.
//
// On a conflict ParseVariables returns a nil map and an error wrapping
// [ErrKeyConflict] that names both variables. No error includes a value.
func ParseVariables(vars map[string]string, prefix, sep string) (map[string]any, error) {
	prefix = strings.ToLower(prefix)
	sep = strings.ToLower(sep)

	data := make(map[string]any)
	// owners maps each key path set so far to the variable that set it, or
	// that first needed it as a map, to name both sides of a conflict.
	owners := make(map[string]string)

	for _, name := range slices.Sorted(maps.Keys(vars)) {
		if IsUnsafeVar(name) {
			continue
		}

		parts, ok := splitName(name, prefix, sep)
		if !ok {
			continue
		}

		err := insert(data, owners, parts, name, vars[name])
		if err != nil {
			return nil, err
		}
	}

	return data, nil
}

// splitName lowercases name, removes prefix and splits the rest on sep. It
// reports false when name does not start with prefix or any level is empty.
func splitName(name, prefix, sep string) ([]string, bool) {
	key, ok := strings.CutPrefix(strings.ToLower(name), prefix)
	if !ok || key == "" {
		return nil, false
	}

	parts := []string{key}
	if sep != "" {
		parts = strings.Split(key, sep)
	}

	if slices.Contains(parts, "") {
		return nil, false
	}

	return parts, true
}

// insert stores value at the key path parts in data, creating nested maps
// on the way. It fails if a level on the way already holds a value, or if
// the key path itself is already set.
func insert(
	data map[string]any,
	owners map[string]string,
	parts []string,
	name, value string,
) error {
	current := data
	// Each level's key path is a prefix of the full one, so slicing it
	// keeps the bookkeeping linear in the name's length.
	full := strings.Join(parts, pathSep)
	end := 0

	for _, part := range parts[:len(parts)-1] {
		end += len(part)

		path := full[:end]

		end += len(pathSep)

		switch next := current[part].(type) {
		case nil:
			m := make(map[string]any)

			current[part] = m
			owners[path] = name
			current = m
		case map[string]any:
			current = next
		default:
			return conflict(owners[path], name)
		}
	}

	if owner, ok := owners[full]; ok {
		return conflict(owner, name)
	}

	current[parts[len(parts)-1]] = value
	owners[full] = name

	return nil
}

func conflict(first, second string) error {
	return fmt.Errorf("%w: %q and %q", ErrKeyConflict, first, second)
}
