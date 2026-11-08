// Package token issues single-use, time-limited tokens, for links such as
// a password reset, an email verification, a magic login link or an
// invite.
//
// A token is a random string the app sends to a user. When the user
// presents it, the app verifies it once, for the purpose it was issued
// for, and learns whose it is. The database keeps the token's hash, never
// the token.
//
// [Service] is a gas.Service. It is not an auth.Authenticator, since a
// token is consumed when verified.
//
// # Usage
//
// Register the service, issue a token when a user asks for a reset, and
// verify it when the link comes back:
//
//	gas.WithSingletonService[*token.Service](token.New())
//
//	raw, err := tokens.Issue(ctx, userID, "password-reset", 0)
//	if err != nil {
//		return err
//	}
//	sendResetLink(email, raw)
//
//	// later, in the handler the link leads to:
//	userID, err := tokens.Verify(ctx, r.FormValue("token"), "password-reset")
//	if errors.Is(err, auth.ErrUnauthenticated) {
//		// unknown, used, expired, or not a reset token: ask for a new link
//	}
//
// The first Verify returns the user's ID; any later Verify of the same
// token returns an error wrapping [ErrTokenInvalid].
//
// # Configuration
//
// [Config] is bound from the `token` section of the configuration, over
// [DefaultConfig], unless [WithConfig] gives one. Its [Settings]:
//
//   - DefaultTTL: how long a token lives when Issue is given a ttl of 0.
//     It must be positive. Default: [DefaultTTL], 15 minutes.
//   - TokenLength: the number of random bytes in a token, at least 16.
//     Default: [DefaultTokenLength], 32.
//   - CleanupInterval: the time between deletions of expired tokens; 0
//     turns the cleanup off, and a negative interval is an error.
//     Default: [DefaultCleanupInterval], an hour.
//   - CleanupTimeout: the bound on each deletion, which must be positive
//     when the cleanup is on. Default: [DefaultCleanupTimeout], 30 seconds.
//
// WithConfig makes Init use a copy of the given Config, so later changes
// to the caller's struct have no effect. Init checks the copy as it checks
// a bound one: the `validate` tags, then the cleanup rules.
//
// Init fails without a configuration:
//
//   - WithConfig(nil) makes Init fail with [ErrNilConfig].
//   - Without WithConfig and without a ConfigProvider, Init fails with
//     [ErrNoConfigProvider].
//
// [Service.Config] returns a copy of the configuration the last
// successful Init resolved, defaults included, or the zero Config before
// one. A failed Init and Close leave it as it was.
//
// # Lifecycle
//
// [New] returns the constructor the dependency injection container
// calls. It fails only for a nil logger, with an error wrapping
// [ErrNilLogger].
//
// [Service.Init] runs these steps, in order:
//
//  1. It resolves and checks the configuration.
//  2. It builds a store that picks the SQL dialect from the database
//     provider's driver.
//  3. It registers the store's migrations with the migration manager,
//     which runs them after every service's Init.
//  4. It publishes the configuration and the store together.
//  5. It starts the background cleanup.
//
// [Service.Close] stops the cleanup and waits for a deletion in progress
// to end.
//
// A Service may be initialized again, with or without a Close between,
// any number of times:
//
//   - Each Init resolves the configuration afresh and builds a new store.
//   - Init stops a cleanup that an earlier Init started before it starts
//     its own, so there is never more than one.
//   - An Init that fails leaves the service uninitialized, with its
//     cleanup stopped.
//
// Issue, Verify, Revoke and RevokeAllByPurpose check first that the
// service is initialized. Before Init succeeds, after an Init that failed,
// and after Close, they return an error wrapping [ErrNotInitialized] and
// touch nothing.
//
// # Issuing
//
// [Service.Issue] makes a token of TokenLength random bytes from
// crypto/rand, encoded as unpadded URL-safe base64 (43 characters at the
// default length), so it is safe in a URL without escaping. It stores the
// token's SHA-256 hash with the subject, the purpose and an expiry ttl
// from now, and returns the token. A ttl of 0 means DefaultTTL.
//
// Issue checks its arguments in this order, and stores nothing when one
// fails:
//
//  1. A negative ttl is an error wrapping [ErrInvalidTTL].
//  2. An empty subject is an error wrapping auth.ErrEmptySubject.
//  3. A subject, then a purpose, longer than auth.MaxValueLength (255)
//     bytes is an error wrapping auth.ErrValueTooLong that names the
//     field, never its value. The check runs before the insert, so every
//     database behaves the same.
//
// Subject and purpose are otherwise stored as given; the purpose may be
// empty. A subject may hold any number of tokens, of any purposes.
//
// # Verifying
//
// [Service.Verify] hashes the token it is given and consumes the stored
// token with that hash: it deletes the token and reads what it held in
// one atomic step, so of any number of calls presenting the same token,
// at the same time or not, at most one succeeds. Then:
//
//  1. No stored token, including one consumed or revoked already: an
//     error wrapping ErrTokenInvalid.
//  2. A token issued for another purpose, compared exactly: an error
//     wrapping ErrTokenInvalid. It is consumed all the same.
//  3. An expired token: an error wrapping [ErrTokenExpired]. It is
//     consumed all the same.
//  4. Otherwise, the subject it was issued for.
//
// So a token is burned the first time it is presented, whatever the
// outcome: a guess at its purpose cannot be retried, and a bug that
// passes the wrong purpose fails at once instead of leaving the token
// usable. The purpose is checked before the expiry, so presenting a
// token for the wrong flow says nothing about whether it has expired.
//
// # Revoking
//
// [Service.Revoke] deletes one token, whatever its purpose and expiry. A
// token that is not stored, including one consumed or revoked already,
// is not an error, so a Revoke may be repeated.
//
// [Service.RevokeAllByPurpose] deletes every token of a subject issued
// for a purpose, expired ones included, and is not an error when there
// are none; use it to cancel the outstanding reset links of a user who
// has just reset the password.
//
// # Expiry and cleanup
//
// A token is expired from its expiry on: at exactly its expiry, it is
// expired already. Expired tokens stay in the database until they are
// presented, revoked, or deleted by the cleanup.
//
// The cleanup runs once every CleanupInterval, the first time one
// interval after Init. It deletes the tokens that are expired when it
// runs, each run bounded by CleanupTimeout.
//
// # Logging
//
// The service logs these events, each with `gas/auth/token` under
// `service`:
//
//   - Init logs `service initialized` at info level.
//   - A cleanup run that succeeds logs `expired tokens deleted` at debug
//     level, with the number deleted under `count`, zero included.
//   - A cleanup run that fails logs `background task failed` at warn
//     level, with `token cleanup` under `task` and the error under
//     `error`. The cleanup goes on.
//
// Nothing is logged for a run that Close interrupts.
//
// # Time precision
//
// MySQL and SQLite keep times in whole seconds, and the auth stores
// truncate every time to the second before writing it, so a stored time
// is up to a second earlier than the one written. On those databases a
// token may so expire up to a second before its ttl has passed, never
// after. Postgres keeps microseconds.
//
// # Storage
//
// Tokens live in the table `__gas_auth_tokens`, on Postgres, MySQL or
// SQLite, chosen by the name the database provider's Driver method
// reports:
//
//   - Postgres: `postgres` or `pgx`.
//   - MySQL: `mysql`. It needs `parseTime=true` in the DSN, which
//     gas/database turns on itself. `multiStatements` is not needed, since
//     gas/migrate sends a MySQL migration's statements one at a time.
//   - SQLite: `sqlite` or `sqlite3`.
//
// The store's migrations create the table, and then drop a redundant index
// on the token hash that its first version made. Lookups use the hash's
// unique index.
//
// Postgres and SQLite consume a token with one `DELETE ... RETURNING`.
// MySQL selects and deletes in one transaction, and treats a delete that
// removed no row as a token another caller consumed.
//
// # Concurrency
//
// Every method is safe for concurrent use, Init and Close included.
//
// Each Init publishes its configuration and store in one atomic step. A
// call that runs while Init does uses either the earlier ones or the new
// ones, never a mix, and a successful Init does not make the calls around
// it fail.
//
// Init and Close run one at a time, so the published state and the
// running cleanup always belong to the same call: once Close returns, no
// cleanup runs until the next Init.
//
// The atomic consume holds across processes that share the database, not
// only within one.
//
// # Errors
//
// Every error starts with `gas/auth/token:` and wraps its cause, to be
// tested with [errors.Is]. Verify denies a token with an error wrapping
// one of these:
//
//   - [ErrTokenInvalid]: no such token, or one issued for another
//     purpose. It wraps auth.ErrUnauthenticated.
//   - [ErrTokenExpired]: the token has expired. It wraps
//     auth.ErrCredentialsExpired, which wraps auth.ErrUnauthenticated.
//
// So errors.Is(err, auth.ErrUnauthenticated) holds for every denial, and
// for nothing else. The other errors wrap:
//
//   - [ErrInvalidTTL]: Issue was given a negative ttl.
//   - auth.ErrEmptySubject: Issue was given an empty subject.
//   - auth.ErrValueTooLong: Issue was given a subject or a purpose longer
//     than auth.MaxValueLength bytes.
//   - [ErrNotInitialized]: a method ran before Init succeeded, after an
//     Init that failed, or after Close.
//   - [ErrNilLogger]: the constructor was given a nil logger.
//   - [ErrNilConfig], [ErrNoConfigProvider], [ErrInvalidConfig]: Init
//     could not resolve the configuration. ErrInvalidConfig wraps the
//     binder's or the validator's error, so errors.As finds
//     validator.ValidationErrors; a field that fails its tag is named
//     with the tag, after `invalid config: validate config:`.
//   - A database error, prefixed by the store and the operation, such as
//     `gas/auth/token-store: consume token:`.
//
// ErrNotInitialized, ErrNilLogger, ErrNilConfig, ErrNoConfigProvider and
// ErrInvalidConfig are the auth module's shared service sentinels, so
// they match the same errors from the module's other services.
//
// No error includes a token, its hash, or a configuration value. Sample
// messages:
//
//	gas/auth/token: unauthenticated: invalid token
//	gas/auth/token: unauthenticated: credentials expired: token expired
//	gas/auth/token: invalid ttl: must not be negative
//	gas/auth/token: empty subject
//	gas/auth/token: value too long: subject is longer than 255 bytes
//	gas/auth/token: service not initialized
//	gas/auth/token: logger is nil
//	gas/auth/token: config is nil
//	gas/auth/token: invalid config: cleanup interval must not be negative
//	gas/auth/token: invalid config: cleanup timeout must be positive when cleanup is enabled
//	gas/auth/token: database not initialized
package token
