// Package configsrc resolves the ui service's configuration: from a copy
// of the one given to its WithConfig option, or else by binding its
// defaults from a gas.ConfigProvider.
//
// # Usage
//
// The service keeps a [Source] of its Config, which WithConfig sets, and
// resolves a fresh configuration on each Init:
//
//	var src configsrc.Source[Config]
//	src.Set(cfg) // from WithConfig, or never
//	resolved, err := src.Resolve(cfgProvider, DefaultConfig, rules)
//
// # Resolving
//
// [Source.Resolve] never returns, and never changes, the struct given to
// Set: it returns a shallow copy, so a caller that keeps slices of the
// result copies them too. With a configuration given to Set, the copy is
// checked against its `validate` tags; a nil one is [ErrNilConfig].
// Without one, the defaults are bound from the [Binder], which checks the
// tags itself; a nil Binder is [ErrNoConfigProvider]. Then the service's
// rules run on the result.
//
// # Errors
//
// A failed tag check, bind or rule wraps [ErrInvalidConfig], and, for the
// first two, the validator's or the binder's error, so that errors.As
// reaches validator.ValidationErrors. The errors, before the service's
// prefix:
//
//	config is nil
//	no config provider
//	invalid config: validate config: <the validator's error>
//	invalid config: <the binder's error>
//	invalid config: <the rule's error>
package configsrc
