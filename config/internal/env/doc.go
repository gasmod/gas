// Package env turns environment variables into the nested map of keys to
// values that the config module merges. The environment and `.env`
// providers both use it, and the `.env` provider also uses [IsUnsafeVar] to
// keep those variables out of the process environment.
//
// # Usage
//
// [ParseVariables] takes variable names and values, a prefix and a
// separator:
//
//	vars := map[string]string{
//		"APP_DB__HOST": "localhost",
//		"APP_DB__PORT": "5432",
//		"APP_DEBUG":    "true",
//		"OTHER":        "x",
//	}
//	data, err := env.ParseVariables(vars, "APP_", "__")
//	// data is {"db": {"host": "localhost", "port": "5432"}, "debug": "true"}
//
// The input map is not modified. An input with no variables left after the
// steps below returns an empty, non-nil map; an error returns a nil map.
//
// # From name to key
//
// Each variable goes through these steps, in this order:
//
//  1. A variable on the unsafe list is left out; see below.
//  2. The name is lowercased. The prefix and the separator are lowercased
//     too, so all three are matched without regard to case.
//  3. With a non-empty prefix, a name that does not start with it is left
//     out, and the prefix is removed from the rest.
//  4. With a non-empty separator, the name is split on it into levels, and
//     each level but the last becomes a nested map. With an empty separator
//     the name is one level.
//  5. A name with an empty level is left out. That covers a name that is
//     empty after the prefix, and, for the separator `__`, names such as
//     `__A`, `A__` and `A____B`.
//
// Names are lowercased with [strings.ToLower] and not otherwise changed:
// spaces are not trimmed. Values are kept exactly as given.
//
// # Unsafe variables
//
// Some variables describe the user, shell or session rather than the
// program's configuration, and can leak secrets or redirect what the
// process runs: `PATH`, `HOME`, `USER`, `SHELL`, `LD_PRELOAD`, session
// variables such as `SSH_AUTH_SOCK` and `SUDO_USER`, `GITHUB_TOKEN`, `CI`
// and others. The list names each variable; it holds no prefixes, so a
// variable such as `SUDO_COMMAND` is kept. [IsUnsafeVar] reports whether a
// name is one of them, without regard to case.
//
// The check runs on the full name, before the prefix is removed, so with
// the prefix `APP_` the variable `APP_PATH` is kept as the key `path`.
//
// # Conflicts
//
// Two variables conflict when they map to the same key after the steps
// above, such as `Foo` and `FOO`, or when one maps to a key that another
// needs as a nested map, such as `A` and `A__B` with the separator `__`. A
// conflict is an error rather than a guess at which variable wins.
// Variables are processed in sorted order of their names, so the same input
// always reports the same conflict.
//
// For the `.env` provider, the parser already rejects keys that differ only
// in case, so only the nested-map kind of conflict reaches this package
// from a `.env` file.
//
// # Errors
//
// A conflict returns an error wrapping [ErrKeyConflict], to be tested with
// [errors.Is]. It names both variables and never includes a value:
//
//	conflicting variable names: "A" and "A__B"
package env
