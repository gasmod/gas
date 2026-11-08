// Package configsrc resolves a store's configuration: from a copy of the
// one given to the store's WithConfig option, or else by binding the
// store's defaults from a gas.ConfigProvider.
//
// # Usage
//
// A store keeps a [Source] of its Config, which WithConfig sets, and
// resolves a fresh configuration on each Init:
//
//	var src configsrc.Source[Config]
//	src.Set(cfg) // from WithConfig, or never
//	resolved, err := src.Resolve(cfgProvider, DefaultConfig, rules)
//
// # Resolving
//
// [Source.Resolve] never returns, and never changes, the struct given to
// Set: it returns a copy. With a configuration given to Set, the copy is
// checked against its `validate` tags; a nil one is an error wrapping
// cache.ErrNilConfig. Without one, the defaults are bound from the
// [Binder], which checks the tags itself; a nil Binder is an error
// wrapping cache.ErrNoConfigProvider. Then the store's rules run on the
// result.
//
// # Errors
//
// A failed tag check, bind or rule wraps cache.ErrInvalidConfig, and,
// for the first two, the validator's or the binder's error, so that
// errors.As reaches validator.ValidationErrors. The errors, before the
// store's prefix:
//
//	config is nil
//	no config provider
//	invalid config: validate config: <the validator's error>
//	invalid config: <the binder's error>
//	invalid config: <the rule's error>
package configsrc
