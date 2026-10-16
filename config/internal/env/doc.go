// Package env turns environment variable names and values into the nested
// maps gas/config merges. EnvProvider and DotEnvProvider use it.
//
// ParseVariables lowercases and trims each name, drops variables that
// describe the user, shell or session (IsUnsafeVar: PATH, HOME, USER, SHELL,
// LD_PRELOAD, the SSH_ and SUDO_ variables, GITHUB_TOKEN, CI and others),
// keeps only names with the given prefix and strips it, then splits each
// name on the separator into levels with BuildNestedMap. An empty separator
// keeps names flat. IsUnsafeVar is also what stops DotEnvProvider from
// copying those variables into the process environment.
package env
