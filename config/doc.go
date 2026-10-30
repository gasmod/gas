// Package config loads an app's configuration from providers, such as the
// environment, `.env` and JSON files, and AWS Secrets Manager, merges it
// into one tree of values, and binds that tree into structs.
//
// # Usage
//
// Build a [Config] with its providers, load it, and bind a struct:
//
//	cfg := config.New(
//		config.WithProvider(config.NewJSONProvider(config.WithJSONFilePath("config.json"))),
//		config.WithProvider(config.NewEnvProvider(config.WithEnvPrefix("APP_"))),
//	)
//	if err := cfg.Load(); err != nil {
//		return err
//	}
//
//	type AppConfig struct {
//		Database struct {
//			Host string `validate:"required"`
//			Port int
//		}
//	}
//	var app AppConfig
//	app.Database.Port = 5432 // kept unless the configuration sets it
//	if err := cfg.Bind(&app); err != nil {
//		return err
//	}
//
// With `APP_DATABASE__HOST=db` in the environment, app.Database.Host is
// `db`, and cfg.Get("database.host") returns it too.
//
// A Config holds nothing until it is loaded. Without a provider, New adds
// an [EnvProvider] with the default separator and no prefix. *Config
// implements [ConfigProvider], the interface services read configuration
// through.
//
// # Providers
//
// A [Provider] returns a map of values each time it loads. The package has
// three, and package secretsmanager reads AWS Secrets Manager.
//
// [EnvProvider] reads the process environment. `__` separates the levels of
// a name, so `DATABASE__HOST` is the key `host` under `database`;
// [WithEnvSeparator] changes it, and an empty separator keeps every name
// one level. [WithEnvPrefix] keeps only the names that start with the
// prefix, without regard to case, and removes it. The separator is not
// added to the prefix, so `APP_` turns `APP_HOST` into `host`, where `APP`
// would turn it into `_host`. Names are lowercased, and a name with an
// empty level, such as `A____B` with `__`, is left out. Variables that
// describe the user, shell or session rather than the app, and can leak
// secrets or change what the process runs, are always left out: `PATH`,
// `HOME`, `USER`, `SHELL`, `LD_PRELOAD`, session variables such as
// `SSH_AUTH_SOCK` and `SUDO_USER`, `GITHUB_TOKEN`, `CI` and others. Two
// names that become the same key, such as `Foo` and `FOO`, or where one
// needs the other's key as a map, such as `A` and `A__B`, are an error.
//
// [DotEnvProvider] reads a `.env` file, `.env` unless
// [WithDotEnvFilePath] says otherwise, and turns its names into keys as
// EnvProvider does, with [WithDotEnvSeparator] for the separator and no
// prefix. A missing file is an error, unless
// [WithDotEnvFileNotFoundPanic] is passed false: then it loads no values.
// The format is small and strict, and anything outside it is an error:
//
//	# a comment
//	DB__HOST=localhost
//	DB__PORT = 5432            # spaces around "=" are allowed
//	DB__NAME="app db"          # quotes keep spaces and "#", and may span lines
//	DB__URL=postgres://${DB__HOST}:${DB__PORT}/app
//	DB__PASSWORD='p@ss${not-a-ref}'
//
// A name is `[A-Za-z_][A-Za-z0-9_]*`, defined once, and not differing from
// another only in case; there is no `export`. An unquoted value ends at a
// space, tab or `#`. Nothing is escaped, so a backslash is text and a
// double-quoted value cannot hold `"`. `${NAME}` in an unquoted or
// double-quoted value is the value of NAME in the same file, never the
// process environment; a single-quoted value is not expanded. A reference
// to a name the file does not define, or a cycle, is an error. The file
// must be valid UTF-8. With [WithDotEnvFileAppendToOSEnv], each load also
// copies the file's variables into the process environment, apart from the
// unsafe ones listed above, and only once the whole file has loaded. A name
// left out of the configuration for an empty level is still copied.
//
// [JSONProvider] reads a JSON file, whose path [WithJSONFilePath] sets; it
// has no default. The file must hold an object. Its values have the types
// [encoding/json] gives them, an object a map[string]any, a list a []any
// and null nil, except numbers, which keep their digits: an integer is an
// int64, or a uint64 when it is too large for one, and any other number,
// such as `1.5`, is a float64. A number too large for a float64, such as
// `1e400`, is an error.
//
// # Files
//
// The JSON and `.env` providers read from the working directory. A path is
// relative to it or absolute, and the file must lie inside it, symlinks
// included, be a regular file, and be at most 1 MiB. A symlink on the path
// must be relative: an absolute one is refused, even when its target lies
// inside the working directory. [WithJSONFileFS] and
// [WithDotEnvFileFS] read from any [fs.FS] instead, such as an [embed.FS],
// without those checks; a path then follows that file system's rules,
// which for an embed.FS means slash-separated and unrooted, with no `.` or
// `..` element. A nil fs.FS reads from the working directory.
//
// # Writing a provider
//
// A provider implements [Provider]. One that should take the load's
// context also implements [ContextProvider], and the Config calls its
// LoadContext instead of Load. The Config keeps a copy of the map a
// provider returns, so the provider may keep and change it. The provider's
// Name names it in errors.
//
// # Loading
//
// [Config.Load], or [Config.LoadWithContext], runs each extension's PreLoad,
// loads every provider in the order they were added, merges their values
// over the configuration in that order, and runs each extension's PostLoad.
//
// Each provider's keys are trimmed and lowercased, at every depth of its
// maps and of the lists ([]any) in them, before it is merged. A slice of
// another type, such as []map[string]any, keeps the keys of its maps. Two
// keys of one provider that become the same, such as `Host` and `host`, are
// an error, while `Host` in one provider and `host` in a later one are not,
// and the later one wins. Where the configuration
// and a provider hold a map under one key, the maps are merged key by key.
// Anywhere else the provider's value replaces what the configuration holds:
// a value replaces a map, a map replaces a value, a list replaces a list as
// a whole, and nil replaces anything.
//
// Every provider loads before anything is merged, outside the lock, so a
// reader never sees part of a load, and a failing provider merges none of
// the providers' values, though what the extensions' PreLoad stored stays.
// The first error stops the load. A failing
// PreLoad stops it before any provider loads; a failing PostLoad stops it
// after the values are merged, and they stay.
//
// A load merges over what the configuration holds and never removes a key.
// Loading again loads every provider again: a key that a provider no longer
// returns keeps its value, and a value stored with Set keeps its value
// unless a provider sets that key.
//
// [Config.LoadProvider] loads one more provider, merges its values over the
// configuration, and adds it to the providers that later loads load. A
// provider that fails to load is not added. Extensions do not run.
//
// # Keys
//
// [Config.Get], [Config.Find], [Config.Set] and [Config.SetDefault] take a
// key with a dot between levels, so `database.host` is the key `host` in
// the map under `database`. Each level is trimmed and lowercased, so
// `Database.HOST` is the same key. A key that is empty, or has a level that
// is empty once trimmed, such as `a..b` or `a. .b`, is no key: Get and
// Find find nothing at it, and Set and SetDefault return an error wrapping
// [ErrInvalidKey].
//
// A key in the tree that holds a dot, such as one a JSON file sets as
// `{"database.host": "db"}`, cannot be reached, as the dot splits it.
//
// # Reading
//
// Get returns the value at a key, or nil when it holds none; [Config.Find]
// also reports whether the key holds a value, which tells a key holding nil
// from a missing one. [Config.Values] returns the whole tree. All three
// return a copy: maps, slices and pointers are copied at every depth, so
// changing the result does not change the configuration. A struct is
// copied the way Go copies it, so a map inside one is shared.
//
// # Writing
//
// Set stores a value at a key, replacing what it holds, and adds the maps
// missing on its path. A level on the path that holds a value that is not
// a map blocks it: Set returns an error wrapping [ErrKeyPathBlocked] and
// changes nothing. [Config.SetDefault] stores a value only at a key that
// holds none; a key holding nil holds a value, and a key under a level
// that holds a value is left alone too, without an error. Both store a
// copy, made as Get makes one, with the keys of a map in it trimmed and
// lowercased at every depth like a provider's, so every key in it can be
// reached; a map of any key type becomes a map[string]any. Two keys of one
// map that become the same are an error wrapping [ErrKeyConflict], and
// nothing is stored.
//
// [Config.SetDefaults] takes a map[string]any, a pointer to one, a struct
// or a pointer to one, and adds the keys the configuration does not hold. A
// struct's fields are named as Bind names them, below. Its keys are
// trimmed and lowercased like a provider's, and two that become the same
// are an error wrapping [ErrKeyConflict]. Where both hold a map under one
// key, the maps are merged the same way; any other key the configuration
// holds keeps its value.
//
// Defaults set before a load are a base that the providers load over.
//
// # Binding
//
// [Config.Bind] fills the struct a pointer points to from the
// configuration, then validates it. Each key that matches a field sets that
// field. A field without a key keeps its value, so a struct filled with
// defaults keeps them, and a key without a field is ignored.
//
// Fields are named as [encoding/json] names them: by the name in a json
// tag, or else the Go name. A field tagged `json:"-"` or unexported is left
// out, and the fields of an embedded struct bind at the level of the struct
// that embeds it. A key matches a field with exactly its name, or else
// without regard to case; two keys that match one field are an error.
//
// A value of a type that can be assigned to the field is copied into it.
// Anything else is converted to the field's type:
//
//   - A struct field takes a map, bound into it by the same rules.
//   - A map field takes a map with string keys; a slice field takes a list,
//     or a string split on `,` with each element trimmed. Either replaces
//     the field's value as a whole.
//   - A bool, integer or float field takes a value of that kind, or a string
//     that parses as one, such as `8080`. A number that does not fit the
//     field's type is an error.
//   - A string field takes a string, and a bool or number formatted with
//     [fmt.Sprint], except that a float is written without an exponent, so
//     1234567.0 is `1234567`.
//   - A [time.Duration] field takes a string that [time.ParseDuration]
//     accepts, so it needs a unit: `30s` works, `30` is an error, and `0` is
//     zero.
//   - nil sets the field to its zero value, and a pointer field is
//     allocated when it is nil.
//
// After filling the struct, Bind validates it with a [Validator], by
// default *validator.Validate from github.com/go-playground/validator/v10,
// which reads `validate` struct tags. [WithValidator] sets another, such as
// one with custom tags registered. A struct that fails to fill is not
// validated, and fields set before the error keep their new values.
//
// # The application environment
//
// [EnvExtension] resolves the environment the app runs in, such as
// development or production, during each load, and stores it at the key
// `GasEnv`. An app reads it by binding a field of type [Environment], or
// gas.Environment, which is the same type, from that key. A field named
// `Env` needs the tag, as its own name is the key `env`; with
// [WithConfigKey] set to `env`, it binds without one:
//
//	type AppConfig struct {
//		Env config.Environment `json:"gasenv"`
//	}
//
// The extension also reports the environment itself:
//
//	envExt := config.NewExtension()
//	cfg := config.New(config.WithExtension(envExt))
//	if err := cfg.Load(); err != nil {
//		return err
//	}
//	if envExt.IsProduction() {
//		// production-only setup
//	}
//
// The environment is the value a provider sets at the key, or else the
// value of the variable `GAS_ENV`, or else, when the variable is unset or
// empty, the fallback, development. The allowed values are development,
// testing, staging and production, compared with their case, and the
// fallback is always allowed. A value that is not allowed fails the load
// with an error wrapping [ErrEnvironmentNotAllowed], so a typo such as
// `prod` stops the app rather than running it as development: from the
// variable, PreLoad fails, and from a provider, or a key holding anything
// but a string, PostLoad fails. The variable is checked on every load,
// even when a provider sets the key or an earlier load stored it, so a
// typo in it fails the load though its value would not be used. The error
// names the variable or key and the allowed values, not the value.
// [WithEnvVarName], [WithAllowedEnvs], [WithDefault] and [WithConfigKey]
// change the variable, the allowed values, the fallback and the key. With
// another key, the environment is stored at `GasEnv` too.
//
// Before the providers load, the extension stores the variable's value, or
// the fallback, at the key unless the key holds a value, so once a load has
// stored it, a later change to the variable takes effect only through a
// provider. [EnvExtension.Current] returns the environment the last load
// resolved, or the empty Environment before the first load.
//
// # Concurrency
//
// A Config is safe for concurrent use: it may be loaded, read, written and
// bound from several goroutines at once. Readers take a read lock and
// writers a write lock, and provider I/O runs outside both. Loads,
// LoadProvider included, load and merge their providers one at a time, so
// a provider added during a load is merged after the providers it follows;
// the extensions' hooks run outside that order and may call LoadProvider.
// An EnvExtension is safe for concurrent use too. The built-in providers
// keep no state between loads.
//
// # Errors
//
// Errors wrap one of the package's sentinel errors, to be tested with
// [errors.Is], and the error they were caused by:
//
//   - [ErrProviderLoadFailed]: a provider failed to load, or two of its keys
//     conflict. The provider's own error is wrapped too, such as one of the
//     provider sentinels below.
//   - [ErrExtensionPreLoadHookFailed], [ErrExtensionPostLoadHookFailed]: an
//     extension's hook failed.
//   - [ErrEnvironmentNotAllowed]: the environment variable, or the key
//     EnvExtension reads, holds an environment that is not allowed. It is
//     wrapped with one of the two above.
//   - [ErrNilValues]: SetDefaults got an untyped nil, or a nil pointer to a
//     map. A nil map adds nothing, without an error.
//   - [ErrInvalidKey]: Set or SetDefault got a key that is empty or has an
//     empty level.
//   - [ErrKeyPathBlocked]: Set got a key under a level that holds a value
//     that is not a map.
//   - [ErrKeyConflict]: two keys of one map become the same key once
//     trimmed and lowercased, in a provider's values, wrapped with
//     ErrProviderLoadFailed, or in a value given to Set, SetDefault or
//     SetDefaults. Two environment variable names that conflict wrap
//     ErrProviderLoadFailed only.
//   - [ErrJSONFilePathNotSet], [ErrJSONFileReadFailed],
//     [ErrJSONDecodeFailed]: the JSON provider has no path, cannot read the
//     file, or the file does not hold a JSON object.
//   - [ErrDotEnvFilePathNotSet], [ErrDotEnvFileReadFailed],
//     [ErrDotEnvParseFailed], [ErrSetEnv]: the `.env` provider has an empty
//     path, cannot read the file, finds a file outside the format or names
//     that conflict, or cannot copy a variable into the process environment.
//
// A missing file wraps [fs.ErrNotExist]. Errors from Bind, Set, SetDefault
// and SetDefaults start with what failed, and Bind wraps the validator's
// error, so [errors.As] finds validator.ValidationErrors. An error names
// the provider, file, extension, key or variable it is about. It never
// includes a value from the configuration, so a secret does not leak into
// logs; a syntax error gives a position instead of the text around it.
// Some errors, the decode and parse ones as the provider returns them,
// which Load prefixes with `failed to load from provider` and the
// provider's name like the first three:
//
//	failed to load from provider static: conflicting keys: "Host" and "host"
//	failed to load from provider Environment Variables: conflicting variable names: "A" and "A__B"
//	failed to load from provider JSON: failed to read JSON config file app.json: config file too large
//	failed to decode JSON from app.json: value is not valid JSON: syntax error after byte 14
//	failed to parse .env file .env: 1:5: unexpected character, want newline: key: "A"
//	failed to execute extension pre-load hook one: boom
//	set defaults: conflicting keys: "B" and "b"
//	bind config: server.port: cannot convert string to int: invalid syntax
//	validate config: Key: 'AppConfig.Database.Host' Error:Field validation for 'Host' failed on the 'required' tag
package config
