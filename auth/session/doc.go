// Package session authenticates HTTP requests by a session cookie, with
// the sessions kept server-side in a SQL database. Its [Service] is an
// auth.Authenticator, an auth.PrincipalRevoker and a gas.Service.
//
// # Usage
//
// Register [New] as a singleton service, next to the database, migrate
// and config services, so that sessions, the *Service, is built and
// initialized. Then issue a session at login, and authenticate the
// routes behind it, rejecting cross-site writes:
//
//	login := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
//		// ... check the user's password ...
//		sess, err := sessions.Create(r.Context(), "user-42", nil, r)
//		if err != nil {
//			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
//			return
//		}
//		if err := sessions.SetCookie(w, sess); err != nil {
//			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
//			return
//		}
//	})
//
//	sameOrigin := session.RequireSameOrigin()
//	mux.Handle("POST /login", sameOrigin(login))
//	mux.Handle("/", sameOrigin(auth.Middleware(sessions)(appHandler)))
//
// With this setup:
//
//   - A login sets the cookie `session_id`.
//   - A later request that carries it reaches appHandler with a principal
//     whose subject is `user-42` and whose scheme is auth.SchemeSession.
//   - A request without it gets a 401.
//   - A POST from another site's page gets a 403.
//
// # Sessions
//
// [Service.Create] issues a session for a subject, with metadata that the
// session's principal will carry, and stores it.
//
// The subject must not be empty and holds at most auth.MaxValueLength
// (255) bytes, checked before anything is stored, so every dialect rejects
// the same subjects.
//
// The metadata follows these rules:
//
//   - A nil metadata is an empty one; the session keeps a copy.
//   - Metadata is stored as JSON, so its values read back as JSON decodes
//     them: numbers as float64, objects as map[string]any.
//   - Metadata that cannot be encoded as JSON fails Create.
//
// The session records the request's remote address and User-Agent, as the
// request gives them, so behind a proxy the address is the proxy's. Both
// are cleaned before they are stored:
//
//   - Invalid UTF-8 becomes U+FFFD.
//   - Control characters are dropped.
//   - The address is cut to 255 bytes, and the User-Agent to 512.
//
// # Session IDs
//
// A session ID is 32 random bytes from crypto/rand, 43 characters of
// unpadded URL-safe base64. The ID is in the returned [Session] and, once
// [Service.SetCookie] sends it, in the cookie, and nowhere else. The
// database keys a session by the SHA-256 of its ID, so reading the
// database, or a backup of it, gives no cookie that authenticates.
//
// Upgrading from a version that stored the raw ID runs a migration that
// deletes every stored session, which signs every user out once.
//
// # Expiry
//
// A session has two expiries:
//
//   - Its idle expiry, [Session].ExpiresAt, is SessionTTL after it was
//     created.
//   - Its absolute expiry is MaxLifetime after it was created, and nothing
//     moves it.
//
// A session is expired from the first of the two on, however it is used.
// SessionTTL must not exceed MaxLifetime.
//
// The expiries are compared with the time in Go, as the database returns
// them. MySQL and SQLite keep times in whole seconds, and the auth stores
// truncate every time to the second before writing it, so a stored time
// is up to a second earlier than the one written. On those databases a
// session may so expire up to a second before its ExpiresAt, never
// after. Postgres keeps microseconds.
//
// # Extending on access
//
// With ExtendOnAccess on, Authenticate moves the idle expiry of a session
// it accepts to SessionTTL from now. It never moves it past the absolute
// expiry, and only does so when less than half of SessionTTL remains, so
// a session in steady use costs one write per half TTL rather than one
// per request. With ExtendOnAccess off, a session lasts SessionTTL from
// its creation.
//
// The write is best-effort: when it fails, Authenticate still succeeds.
// The error is logged at warn level with the message
// `session extend failed` and the error under `error`, unless the
// request's context has ended, as when the client went away.
//
// # Cleanup
//
// Expired sessions are deleted by a cleanup that runs every
// CleanupInterval, each run bounded by CleanupTimeout, from Init to
// Close. Its first run is one CleanupInterval after Init.
//
// Each run logs, with `gas/auth/session` under `service`:
//
//   - On success, the message `expired sessions deleted` at debug level,
//     with the number deleted under `count`, zero included.
//   - On failure, the message `background task failed` at warn level,
//     with `session cleanup` under `task` and the error under `error`. The
//     cleanup goes on.
//
// A zero CleanupInterval turns the cleanup off, and expired sessions then
// stay in the table, still rejected. A negative one is a configuration
// error.
//
// # Cookies
//
// [Service.SetCookie] writes the cookie for a session that Create or
// Rotate returned, with:
//
//   - The configured name.
//   - The session ID as its value.
//   - The configured Path, Domain, Secure, HttpOnly and SameSite
//     attributes.
//   - An Expires of the session's absolute expiry.
//
// The browser so keeps the cookie for as long as the session can live,
// while the server enforces the idle expiry, and an extended session needs
// no new cookie.
//
// [Service.ClearCookie] writes a cookie of the same name and attributes
// with an empty value and `Max-Age=0`, which makes the browser delete it.
// It leaves the session in the database.
//
// Init checks that the name, path and domain make a valid cookie, which
// net/http would otherwise drop or alter with only a log line. It also
// logs a warning, once per Init, for each insecure attribute:
//
//   - `session cookie is not Secure`
//   - `session cookie is not HttpOnly`
//   - `session cookie is not SameSite Lax or Strict`
//
// # Authentication
//
// [Service.Authenticate] reads the configured cookie and returns:
//
//   - auth.ErrNoCredentials when the request is nil, has no such cookie,
//     its value is empty, or net/http cannot parse it.
//   - auth.ErrUnauthenticated when the value is not a session ID, or no
//     stored session has it. A value that does not have the form of a
//     session ID is rejected without reading the database.
//   - auth.ErrCredentialsExpired when the session is past its idle or its
//     absolute expiry.
//   - Otherwise an auth.BasePrincipal with the session's subject and
//     metadata, the scheme auth.SchemeSession, and as its credential ID
//     the SHA-256 hex of the session ID, which identifies the session
//     without being the secret.
//
// Each denial is the auth sentinel prefixed with [ServiceName], such as
// `gas/auth/session: no credentials`, so [errors.Is] matches it and the
// text names the service. A database failure is an infrastructure error,
// which auth.Middleware answers with a 500.
//
// # Rotation
//
// [Service.Rotate] moves the session that the request's cookie names to a
// new ID, and returns it for SetCookie; the old ID no longer
// authenticates. It fails as Authenticate does when the request is nil
// ([ErrNilRequest]), or the session is missing or expired.
//
// The rotated session has:
//
//   - A new ID.
//   - The old one's subject, metadata and creation time, so rotating never
//     extends the absolute expiry.
//   - The address and User-Agent of the request.
//   - An idle expiry SessionTTL from now, capped by the absolute one.
//
// The session is rotated in place, with one UPDATE of its stored row
// that changes its ID hash. Under concurrency:
//
//   - Of two concurrent rotations of one session, one succeeds and the
//     other returns auth.ErrUnauthenticated.
//   - A RevokeAll of the subject that runs concurrently with a rotation
//     leaves no session of the subject behind, the rotated one included,
//     on every dialect.
//
// Rotate after a change of the session's privileges, such as a role
// change or a step-up authentication, and after a login that keeps an
// existing session, so that an ID captured before, such as one planted in
// the victim's browser (session fixation), is worthless afterwards. A
// login that issues a session with Create needs no rotation, though the
// app should Revoke the session the request had, if any.
//
// # Listing
//
// [Service.List] returns a subject's live sessions as [Info]
// values, newest first, for a page where a user sees where they are
// signed in and signs out one device. Sessions created at the same
// stored time are ordered by ID, descending. Sessions past their idle or
// absolute expiry are left out, as Authenticate would reject them, even
// before the cleanup deletes them.
//
// An Info holds the session's subject, metadata, address,
// User-Agent and times, and as its ID the SHA-256 hex of the session ID,
// never the session ID. Its ID is so safe to show to the user and to
// send back from a form, and it equals the credential ID of the
// principal Authenticate returns for the session, which tells the
// request's own session apart in the list. Rotate changes it.
//
// # Revocation
//
// Revoking deletes sessions:
//
//   - [Service.Revoke] deletes the session a principal from Authenticate
//     stands for. A nil principal, a nil pointer included, is
//     auth.ErrNilPrincipal, and one of another scheme
//     auth.ErrSchemeMismatch. The principal's credential ID is the hash
//     of the session ID, so a principal built from Session.ID deletes
//     nothing.
//   - [Service.RevokeSession] deletes a Session from Create or Rotate,
//     without a request. A nil session, or one whose ID is not a session
//     ID, is [ErrInvalidSession].
//   - [Service.RevokeByID] deletes the session an Info.ID names,
//     only if it belongs to the subject given, so that a user who sends
//     back another user's ID deletes nothing. An ID that is not 64
//     lowercase hex digits, such as a Session.ID, is [ErrInvalidSession].
//     An ID from before the session was rotated matches nothing.
//   - [Service.RevokeOthers] deletes every session of a principal's
//     subject but the principal's own, for "sign out other devices", in
//     one DELETE. It rejects a principal as Revoke does. If the
//     principal's session is rotated before the DELETE runs, its ID hash
//     no longer matches and it is deleted too.
//   - [Service.RevokeAll] deletes every session of a subject.
//   - [Service.RevokeAllByScheme] does the same for auth.SchemeSession, and
//     nothing for any other scheme.
//
// Revoking what is already gone, or was never there, is not an error.
// Revoking does not clear the cookie; ClearCookie does.
//
// # Cross-site requests
//
// A browser sends the session cookie with every request to the site,
// including one that another site's page makes. So a cookie session needs
// protection against cross-site request forgery (CSRF), which the app
// owns.
//
// SameSite=Lax, the default, keeps the cookie off cross-site POSTs from
// other sites. It does not keep it off POSTs from other subdomains of the
// same site, nor off GETs, so state-changing GETs stay unsafe.
//
// [RequireSameOrigin] is middleware that adds a check based on headers
// browsers set. It lets GET, HEAD, OPTIONS and TRACE through. Any other
// method is rejected with a plain-text 403 `Forbidden` when:
//
//   - a `Sec-Fetch-Site` header is present with a value other than
//     `same-origin` or `none`, compared exactly; or
//   - an `Origin` header is present and is not `scheme://host[:port]`
//     whose host and port equal the request's Host, ignoring case. The
//     scheme is not compared, and `Origin: null` never matches.
//
// Every value of a repeated header must pass. A request with neither
// header, as from a client that is not a browser or an old browser, is let
// through. RequireSameOrigin does not need the Service.
//
// Behind a proxy that rewrites the Host header, the Origin check rejects
// every cross-origin-looking request, so forward the original Host.
//
// # Configuration
//
// [Config] holds the [Settings] in the `session` section of the
// configuration. Init binds them from the gas.ConfigProvider over
// [DefaultConfig], or, with [WithConfig], uses a copy of the given
// configuration. Either way it checks the validate tags and these rules:
//
//   - CookieName is required and a valid cookie name.
//   - CookiePath and CookieDomain are valid for a cookie.
//   - CookieSameSite is 0 or an http.SameSite constant.
//   - SessionTTL and MaxLifetime are positive, and SessionTTL is at most
//     MaxLifetime.
//   - CleanupInterval is not negative.
//   - CleanupTimeout is positive when CleanupInterval is.
//
// A configuration given to WithConfig is checked as given: no zero
// setting in it is replaced by its default, so a Config literal that
// leaves MaxLifetime out fails Init. Build one from [DefaultConfig] to
// keep the defaults.
//
// The defaults:
//
//   - CookieName: [DefaultCookieName].
//   - CookiePath: [DefaultCookiePath].
//   - CookieDomain: none.
//   - CookieSameSite: Lax.
//   - CookieSecure and CookieHTTPOnly: true.
//   - SessionTTL: [DefaultSessionTTL] (24 hours).
//   - MaxLifetime: [DefaultMaxLifetime] (30 days).
//   - ExtendOnAccess: true.
//   - CleanupInterval: [DefaultCleanupInterval] (1 hour).
//   - CleanupTimeout: [DefaultCleanupTimeout] (30 seconds).
//
// [Service.Config] returns a copy of the configuration the last
// successful Init resolved, defaults included, or the zero Config before
// one. A failed Init and Close leave it as it was.
//
// # Lifecycle
//
// [New] returns the constructor the dependency injection container calls,
// with a gas.DatabaseProvider, gas.Logger, gas.MigrationManager and
// gas.ConfigProvider. It fails with [ErrNilLogger] for a nil logger.
//
// [Service.Init] runs these steps, in order:
//
//  1. It resolves the configuration.
//  2. It picks the SQL dialect from the database driver.
//  3. It registers the migrations with the migration manager, which
//     creates and updates the `__gas_auth_sessions` table.
//  4. It starts the cleanup.
//  5. It logs `service initialized` at info level.
//
// [Service.Close] stops the cleanup and waits for a run in progress.
//
// Init may run again after Close, or again without it, and picks up a
// changed configuration. A failed Init stops the cleanup and leaves the
// service uninitialized. Before Init, after a failed Init and after
// Close, every method but Name, Init, Close and Config returns
// [ErrNotInitialized].
//
// Every event the service logs carries [ServiceName] under `service`.
//
// # Storage
//
// The database dialects are picked by the name the database provider's
// Driver method reports:
//
//   - Postgres: `postgres` and `pgx`.
//   - MySQL: `mysql`. It needs `parseTime=true` in the DSN, as
//     gas/database sets it, and MySQL 8.0.13 or later, since the migration
//     that hashes session IDs gives the `metadata` and `user_agent`
//     columns expression defaults.
//   - SQLite: `sqlite` and `sqlite3`.
//
// # Concurrency
//
// A Service is safe for concurrent use, Init and Close included.
// [Provider] is the interface the Service implements, for a mock seam.
//
// Each Init builds its own store and publishes it with the configuration
// in one atomic step, so a method runs entirely with the configuration and
// store of one Init. Init and Close may run while other methods run:
//
//   - Each method running finishes with what it started with.
//   - The methods that start after Close return ErrNotInitialized.
//
// Init and Close run one at a time, so the published state and the
// running cleanup always belong to the same call: once Close returns, no
// cleanup runs until the next Init.
//
// # Errors
//
// Authentication denials are the auth sentinels, wrapped, as listed under
// Authentication. The other sentinels, to be tested with [errors.Is]:
//
//   - [ErrNotInitialized]: a method ran before Init, after a failed Init,
//     or after Close.
//   - [ErrNilLogger]: New's constructor was given a nil logger.
//   - [ErrNilConfig]: WithConfig was given nil.
//   - [ErrNoConfigProvider]: there is neither a WithConfig configuration
//     nor a ConfigProvider.
//   - [ErrInvalidConfig]: the configuration failed to bind, its tags or a
//     rule.
//   - [ErrNilRequest]: Create or Rotate was given a nil request.
//   - [ErrEmptySubject]: Create was given an empty subject.
//   - auth.ErrValueTooLong: Create was given a subject longer than
//     auth.MaxValueLength bytes.
//   - [ErrInvalidSession]: SetCookie or RevokeSession was given a nil
//     session or one that Create or Rotate did not return, or RevokeByID
//     an ID that is not an Info.ID.
//   - auth.ErrNilPrincipal: Revoke or RevokeOthers was given a nil
//     principal.
//   - auth.ErrSchemeMismatch: Revoke or RevokeOthers was given a
//     principal of another scheme.
//
// [ErrNilConfig], [ErrNoConfigProvider], [ErrInvalidConfig],
// [ErrNotInitialized] and [ErrNilLogger] are not defined by this package
// but shared by the module's services, and [ErrEmptySubject] is
// auth.ErrEmptySubject.
//
// Every error starts with [ServiceName]. Some errors then name the
// operation, such as `create:` or `revoke all:`:
//
//   - These do: the errors of Create, Rotate, SetCookie, List, Revoke,
//     RevokeSession, RevokeByID, RevokeOthers, RevokeAll and
//     RevokeAllByScheme, other than a denial or ErrNotInitialized, and a
//     database error from Authenticate.
//   - These do not: a denial, ErrNotInitialized, and the errors of the
//     constructor and Init.
//
// A database error is wrapped, so [errors.As] reaches the driver's error.
// No error includes a session ID, a cookie value or a configuration
// value:
//
//	gas/auth/session: service not initialized
//	gas/auth/session: logger is nil
//	gas/auth/session: no credentials
//	gas/auth/session: unauthenticated
//	gas/auth/session: unauthenticated: credentials expired
//	gas/auth/session: create: empty subject
//	gas/auth/session: create: value too long: subject is longer than 255 bytes
//	gas/auth/session: rotate: request is nil
//	gas/auth/session: set cookie: invalid session
//	gas/auth/session: revoke: nil principal
//	gas/auth/session: revoke: principal scheme mismatch
//	gas/auth/session: revoke session: invalid session
//	gas/auth/session: revoke by id: invalid session
//	gas/auth/session: invalid config: session TTL must not exceed the max lifetime
//	gas/auth/session: invalid config: cleanup interval must not be negative
//	gas/auth/session: authenticate: gas/auth/session-store: get session: sql: database is closed
package session
