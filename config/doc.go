// Package config loads an app's configuration from one or more providers,
// merges it into one tree of values, and binds it into structs. It is the
// implementation of core's gas.ConfigProvider.
//
// # Role in the framework
//
// Core defines gas.ConfigProvider, the interface services read configuration
// through (Bind, Get, Find, Values, Set, SetDefault and SetDefaults);
// *Config implements it. This module imports core, not the other way round:
// BindOption is an alias of gas.BindOption, and the gasenv types are aliases
// of gas.Environment and gas.WithGasEnv. LoadProvider and LoadProviderContext
// stay on *Config, for setup before it is registered. Each module that takes
// configuration binds its own struct from the gas.ConfigProvider at startup,
// under its own top-level key (database, email, queue, ...).
//
// Configuration sources implement providers.Provider; the providers
// sub-package has environment variable, JSON file and .env file sources, and
// providers/secretsmanager reads AWS Secrets Manager. An Extension runs code
// before and after loading; extensions/gasenv uses that to resolve the
// application environment. configtest provides a mock for tests.
//
// # Wiring
//
// The package registers nothing in the DI container. The app builds a
// Config with New, loads it, and registers it as the gas.ConfigProvider
// instance:
//
//	cfg := config.New(config.WithProvider(providers.NewEnvProvider()))
//	if err := cfg.Load(); err != nil {
//		return err
//	}
//	app := gas.NewApp(gas.WithServiceInstance[gas.ConfigProvider](cfg))
//
// New takes WithProvider (repeatable), WithExtension (repeatable) and
// WithValidator. With no provider, New adds an environment variable provider
// with the default "__" separator.
//
// # Loading and precedence
//
// Load, or LoadWithContext, runs every extension's PreLoad, loads every
// provider, merges the results in registration order so that a later
// provider overrides the keys an earlier one sets, then runs every
// extension's PostLoad. All providers are loaded before anything is merged,
// and provider I/O runs outside the lock, so a reader never sees a partly
// merged result. The first provider or hook error stops the load and is
// returned; values already merged by an earlier Load stay. A provider that
// implements providers.ContextProvider gets the context; others are called
// without one. LoadProvider adds and loads one more provider over the
// current values, registering it only if its load succeeds.
//
// SetDefault and SetDefaults add values only where none exist; Set always
// overrides. Defaults set before Load are overridden by any provider that
// sets the same key.
//
// # Keys and binding
//
// Keys are case-insensitive: provider keys are lowercased when merged, and
// lookups lowercase the key. A dot separates levels, so "database.dsn" is
// the dsn key under database. Get, Find and Values return copies in which
// maps, slices and pointers are copied deeply, so changing a returned map
// does not change the stored configuration; a struct value set with Set or
// SetDefault is copied by value, so maps inside it stay shared.
//
// Bind copies the values into a struct, matching a field by its json tag,
// then by its lowercased name; nested structs, slices, arrays, maps and
// pointers are filled recursively, with conversion between common scalar
// types and time.Duration. A key with no matching field is ignored, and a
// field with no key keeps its value, so binding over a struct of defaults
// keeps the defaults. Bind then validates the struct with
// go-playground/validator (the "validate" struct tags) unless
// WithValidate(false) is passed.
//
// # Lifecycle
//
// Config has no lifecycle of its own: it is not a gas.Service and starts no
// goroutines. Loading is up to the app, normally once at startup before the
// container is built.
//
// # Errors and concurrency
//
// Load wraps ErrProviderLoadFailed, ErrExtensionPreLoadHookFailed or
// ErrExtensionPostLoadHookFailed around the failing provider's or hook's
// error, naming it; SetDefaults returns ErrNilValues for a nil argument.
// Bind errors start with "bind config:" or "validate config:". A Config is
// safe for concurrent use: reads take a read lock and writes a write lock.
//
// Guides with usage examples are at https://gasmod.github.io/gas/modules/config/.
//
// SPDX-License-Identifier: MIT
package config
