// Package gasenv is a gas/config extension that resolves the application
// environment (development, testing, staging or production) and stores it in
// the configuration, where services and bound structs can read it.
//
// # Role in the framework
//
// Extension implements config.Extension. Environment is the resolved value,
// with helpers such as IsProduction and IsDevelopmentLike (development or
// testing). WithGasEnv is a struct to embed in a configuration struct: its
// GasEnv field binds from the configuration's GasEnv key, which is how module
// configs (database, email, queue, ui, ...) carry the environment. Without
// the extension, nothing sets that key and GasEnv stays empty unless a
// provider happens to set it.
//
// # Wiring
//
// The package registers nothing in the DI container. The app passes the
// extension to config.New and keeps it if it wants to ask for the
// environment directly:
//
//	envExt := gasenv.NewExtension()
//	cfg := config.New(config.WithExtension(envExt))
//	if err := cfg.Load(); err != nil {
//		return err
//	}
//	if envExt.IsProduction() {
//		// production-only setup
//	}
//
// # Configuration and resolution
//
// The extension reads the OS environment variable GAS_ENV (WithEnvVarName
// changes it) and the configuration key GasEnv (WithConfigKey changes it).
// The allowed values are development, testing, staging and production
// (WithAllowedEnvs narrows them), compared case-sensitively, and the
// fallback is development (WithDefault changes it; it should be one of the
// allowed values, which is not checked).
//
// Resolution runs during Config.Load. Before the providers load, the
// extension takes the OS variable, or the fallback when it is unset or not
// allowed, and sets it as a default for the key. After they load, a provider
// that set the key to an allowed value wins; any other value at the key is
// replaced with the fallback. So the order is: a provider's value, then the
// OS variable, then the fallback. With a custom key, the result is also
// written to GasEnv so WithGasEnv still binds it. An invalid value is never
// reported as an error; it is replaced silently.
//
// # Lifecycle and concurrency
//
// The extension has no lifecycle beyond the two load hooks and starts no
// goroutines. Current and the Is helpers return the environment resolved by
// the last Load, and the empty Environment before the first. They are meant
// to be read after loading: a Load running at the same time writes the value
// without synchronization.
//
// SPDX-License-Identifier: MIT
package gasenv
