// Package configsrc resolves a backend's configuration: from a copy of the
// one given to the backend's WithConfig option, or else by binding the
// backend's defaults from a gas.ConfigProvider.
//
// # Usage
//
// A backend keeps a [Source] of its Config, which WithConfig sets, and
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
// storage.ErrNilConfig, which a backend's constructor reports first, as
// [Source.IsNil] tells it. Without one, the defaults are bound from the
// [Binder], which checks the tags itself; a nil Binder is an error
// wrapping storage.ErrNoConfigProvider. Then the backend's rules run on the
// result.
//
// # Errors
//
// A failed tag check, bind or rule wraps storage.ErrInvalidConfig, and,
// for the first two, the validator's or the binder's error, so that
// errors.As reaches validator.ValidationErrors. The errors, before the
// backend's prefix:
//
//	config is nil
//	no config provider
//	invalid config: validate config: <the validator's error>
//	invalid config: <the binder's error>
//	invalid config: <the rule's error>
package configsrc
