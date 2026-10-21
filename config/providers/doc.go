// Package providers defines the interface a configuration source implements
// for gas/config, and provides the built-in sources: environment variables,
// JSON files and .env files.
//
// # Role in the framework
//
// Provider is the interface config.New and Config.LoadProvider accept. A
// source that can honor cancellation also
// implements ContextProvider, which Config.LoadWithContext prefers. Each
// source returns a nested map; gas/config lowercases its keys and merges it
// with the others.
//
// # The sources
//
// EnvProvider reads the process environment. "__" (change it with
// WithEnvSeparator) splits a name into levels, so DATABASE__DSN becomes
// database.dsn; an empty separator keeps names flat. WithEnvPrefix keeps only
// variables with the prefix and strips it, literally, so include the
// trailing separator ("APP_"). Variables that describe the user, shell or
// session (PATH, HOME, USER, SHELL, LD_PRELOAD, the common SSH_ and SUDO_
// session variables such as SSH_AUTH_SOCK and SUDO_USER, GITHUB_TOKEN, CI and
// others) are always skipped.
//
// JSONProvider reads one JSON object from the file set with WithJSONFilePath;
// there is no default path, and Load without one returns
// ErrJSONFilePathNotSet.
//
// DotEnvProvider reads KEY=VALUE lines from a .env file (".env" by default)
// and turns them into nested keys the same way EnvProvider does. Quoted
// values may span lines and keep "#"; outside quotes "#" starts a comment. A
// missing file is an error unless WithDotEnvFileNotFoundPanic(false) is
// passed, in which case it yields no values; despite the option's name,
// nothing panics. WithDotEnvFileAppendToOSEnv(true) also copies the entries
// into the process environment, except the skipped variables above, so a
// .env entry cannot replace PATH or LD_PRELOAD.
//
// # Files
//
// By default JSONProvider and DotEnvProvider read through a file system that
// accepts only paths inside the working directory, rejects a path whose last
// element is a symlink, and rejects files over 1 MB. The working-directory
// check is made on the path as written and again after resolving symlinked
// directories along it, so a symlinked directory cannot lead outside the
// working directory. WithJSONFileFS and WithDotEnvFileFS read from any fs.FS
// instead, such as an embed.FS, without those checks.
//
// # Wiring and lifecycle
//
// The package registers nothing and reads no configuration of its own; the
// app passes providers to config.New or Config.LoadProvider. Providers hold
// no resources and have no lifecycle: each Load reads its source again.
//
// # Errors
//
// JSONProvider returns ErrJSONFilePathNotSet, ErrJSONFileReadFailed or
// ErrJSONDecodeFailed; DotEnvProvider returns ErrDotEnvFilePathNotSet,
// ErrDotEnvFileReadFailed, ErrDotEnvParseFailed or ErrSetEnv. The read,
// decode, parse and set errors wrap the underlying error and name the file
// or variable. EnvProvider never fails.
//
// SPDX-License-Identifier: MIT
package providers
