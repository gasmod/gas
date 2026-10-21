// Package secretsmanager is a gas/config provider that loads configuration
// from AWS Secrets Manager. Secrets are registered explicitly and fetched
// each time the configuration is loaded.
//
// # Role in the framework
//
// Provider implements providers.Provider and providers.ContextProvider, so it
// is passed to config.New or Config.LoadProvider like any other source and
// gets the caller's context from Config.LoadWithContext.
//
// # Wiring and configuration
//
// The package registers nothing and reads no configuration of its own; the
// app sets everything with options. WithSecret registers a secret (a name or
// a full ARN) whose value must be a JSON object; the object is merged at the
// root. WithSecretAtKey places a secret at a dot-separated key: a JSON object
// value is nested there, and any other value is stored there as a string.
// Secrets are fetched in registration order and merged, later ones
// overriding earlier ones. WithRegion, WithEndpoint (for example LocalStack)
// and WithStaticCredentials, used only when both values are set, configure
// the AWS client; otherwise the default AWS configuration and credential
// chain apply. WithClient injects a client instead, and then those options
// are ignored.
//
// # Lifecycle
//
// The provider builds its AWS client on the first load and reuses it. Load
// without a context bounds the whole fetch with a timeout, 10 seconds
// (DefaultTimeout) unless WithTimeout says otherwise; LoadContext uses the
// caller's context as is. A WithTimeout of zero or a negative duration means
// no timeout, so Load waits as long as the AWS client does. The provider
// starts no goroutines and holds
// nothing that needs closing.
//
// # Errors
//
// Load with no registered secret returns ErrNoSecretsConfigured. Otherwise
// errors wrap ErrClientInitFailed (the AWS configuration could not be
// loaded), ErrSecretFetchFailed (GetSecretValue failed) or
// ErrSecretDecodeFailed (a root secret is not a JSON object); fetch and
// decode errors name the secret. The first failing secret stops the load.
//
// SPDX-License-Identifier: MIT
package secretsmanager
