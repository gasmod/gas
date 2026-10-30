// Package secretsmanager is a config provider that loads values from AWS
// Secrets Manager. Each load fetches the secrets registered with the
// provider and merges them into one map, which the config module merges
// like that of any other provider.
//
// # Usage
//
// Register each secret with an option, and pass the provider to the config
// module:
//
//	p := secretsmanager.NewProvider(
//		secretsmanager.WithSecret("myapp/config"),
//		secretsmanager.WithSecretAtKey("myapp/db-password", "database.password"),
//		secretsmanager.WithRegion("eu-west-1"),
//	)
//	cfg := config.New(config.WithProvider(p))
//	err := cfg.Load()
//	// cfg.Get("database.password") is the value of myapp/db-password
//
// [Provider] implements config.Provider and config.ContextProvider. It
// reads no configuration keys of its own; the options set everything.
//
// # Secrets
//
// A secret is registered by its name or ARN, which goes to GetSecretValue
// as it is, with no version or stage, so Secrets Manager returns the
// current version, AWSCURRENT. Every load fetches every registered secret
// again, one at a time in registration order, so a load after a rotation
// gets the new value. A secret registered twice is fetched twice.
//
// A secret's value is its SecretString, or its SecretBinary when it has no
// string.
//
// # Values
//
// [WithSecret] takes a secret whose value is a JSON object and merges the
// object at the root. Any other value is an error.
//
// [WithSecretAtKey] places a secret at a key, with a dot between levels as
// for Config.Get, so `database.password` is the key `password` in the map
// under `database`. A JSON object is nested there. Any other value is
// stored there as its text, exactly as stored: a JSON string keeps its
// quotes, and a number, list or null is not decoded. An empty key is the
// root, as for WithSecret. A key with an empty level, such as
// `db..password`, `.db` or a space, is an error. A JSON object that holds a
// number too large for a float64, such as `1e400`, is an error too, at a
// key as at the root.
//
// JSON values have the types [encoding/json] decodes them to, an object a
// map[string]any, a list a []any and null nil, except numbers, which keep
// their digits: an integer is an int64, or a uint64 when it is too large
// for one, and any other number, such as `1.5`, is a float64.
//
// # Keys
//
// Keys are trimmed and lowercased, those of the key passed to
// WithSecretAtKey and those of a JSON object at every depth, as the config
// module does for every provider. A key of a JSON object that is empty
// once trimmed is dropped, with its value. Each secret is handled on its
// own: two keys of one secret that become the same, such as `Host` and
// `host`, are an error, while `Host` in one secret and `host` in a later
// one are not, and the later one wins.
//
// A dot in a key of a JSON object is part of the key and does not nest:
// `{"database.host": "db"}` holds the key `database.host`, which Config.Get
// cannot reach, as it splits its argument on dots.
//
// # Merging
//
// Secrets are merged in registration order, a later one winning. Where two
// secrets hold a map under one key, the maps are merged key by key, so a
// secret at the root holding `{"database": {"host": "db"}}` and one at
// `database.password` give `database` both keys. Anywhere else the later
// value replaces the earlier one: a value replaces a map, a map replaces a
// value, a list replaces a list as a whole, and null replaces anything.
//
// # Client
//
// The first load builds the AWS client from the default AWS configuration,
// as the AWS SDK loads it, with these options over it:
//
//   - [WithRegion] sets the region.
//   - [WithStaticCredentials] sets the credentials, when both values are
//     set. With one of them empty, the default credential chain applies.
//   - [WithEndpoint] sets the endpoint, such as LocalStack's.
//
// An empty region or endpoint leaves it to the default configuration.
// Later loads reuse the client, so a change to the AWS configuration after
// the first load does not reach the provider. A failed build is not kept:
// the next load tries again. A region missing from both the option and the
// default configuration does not fail the build; without an endpoint, each
// fetch then fails.
//
// [WithClient] gives the provider a client to use instead, such as a
// *secretsmanager.Client built elsewhere or a fake, and the three options
// above then have no effect. A nil client builds one as without the option.
// Only a nil interface counts: a nil *secretsmanager.Client is used as it
// is, and panics on the first fetch.
//
// # Timeouts
//
// Each load runs under a context that ends after [DefaultTimeout], 10
// seconds, or after the duration passed to [WithTimeout]. Zero or a
// negative duration means no limit of the provider's own.
//
// [Provider.LoadContext] derives that context from the caller's, so the
// caller's values reach the client, and the load ends at the caller's
// deadline or the provider's, whichever comes first. A caller that needs
// longer than the timeout sets a longer one with WithTimeout. [Provider.Load]
// is LoadContext with context.Background(). The config module calls
// LoadContext, so Config.Load and Config.LoadWithContext are bounded the
// same way.
//
// When the context ends during a fetch, the error wraps the context's
// error, such as [context.DeadlineExceeded].
//
// # Concurrency
//
// Load and LoadContext may be called from several goroutines at once, on
// one provider, as two Config.Load calls do.
//
// # Errors
//
// Errors wrap one of the package's sentinel errors, to be tested with
// [errors.Is]:
//
//   - [ErrNoSecretsConfigured]: no secret was registered.
//   - [ErrInvalidKey]: a key passed to WithSecretAtKey has an empty level.
//   - [ErrClientInitFailed]: the AWS configuration could not be loaded, such
//     as for a profile that does not exist.
//   - [ErrSecretFetchFailed]: GetSecretValue failed. The AWS error is
//     wrapped, so [errors.As] finds it, such as a
//     *types.ResourceNotFoundException for a secret that does not exist.
//   - [ErrSecretDecodeFailed]: a secret at the root is not a JSON object, or
//     two keys of a secret conflict.
//
// The first two are reported before the client is built, and the third
// before any secret is fetched. The first error stops the load, and no
// later secret is fetched.
//
// An error about one secret names it. The provider adds no part of a
// secret's value to an error: a decode error gives the offset of a JSON syntax
// error or the kind of JSON value, and a key conflict names the two keys:
//
//	no secrets configured
//	invalid key "db..password" for secret myapp/db-password: a level is empty
//	failed to initialize SecretsManager client: failed to get shared config profile, prod
//	failed to fetch secret myapp/config: operation error Secrets Manager: GetSecretValue, ...
//	failed to decode secret myapp/config: value is not valid JSON: syntax error after byte 14
//	failed to decode secret myapp/config: value is a JSON string, not an object
//	failed to decode secret myapp/config: database: conflicting keys: "Host" and "host"
//
// The config module wraps the error once more and names the provider by
// [ProviderName]:
//
//	failed to load from provider AWS SecretsManager: failed to fetch secret myapp/config: ...
package secretsmanager
