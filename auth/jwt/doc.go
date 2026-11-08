// Package jwt signs and verifies JSON Web Tokens with HS256 or RS256, and
// authenticates requests that carry one as a bearer token. It keeps no
// state per token, so it needs no database. [Service] implements
// auth.Authenticator, gas.Service and [Provider], which embeds
// auth.Authenticator.
//
// # Usage
//
// In an app, the container builds the service from [New] and calls its
// Init. By hand, with a configuration given in code:
//
//	svc, err := jwt.New(jwt.WithConfig(&jwt.Config{JWT: jwt.Settings{
//		SigningMethod: jwt.MethodHS256,
//		SigningKey:    gas.Secret(key), // at least 32 bytes
//		Expiry:        15 * time.Minute,
//	}}))(nil, logger)
//	if err != nil {
//		return err
//	}
//	if err := svc.Init(ctx); err != nil {
//		return err
//	}
//
//	token, err := svc.Sign("user-42", map[string]any{"role": "admin"})
//	if err != nil {
//		return err
//	}
//
//	r.Header.Set("Authorization", "Bearer "+token)
//	p, err := svc.Authenticate(ctx, r)
//
// p.Subject() is `user-42`, p.Scheme() is auth.SchemeJWT, p.CredentialID()
// is the token's random `jti`, and p.Metadata() holds `role`. Behind
// auth.Middleware, the service authenticates every request the same way.
//
// # Configuration
//
// The settings live in the `jwt` section, in [Settings]:
//
//   - `jwt.signingmethod`: [MethodHS256] or [MethodRS256]. Default:
//     [DefaultSigningMethod].
//   - `jwt.signingkey`: the HMAC key for HS256, at least [MinHMACKeyBytes]
//     bytes, a gas.Secret, which fmt, encoding/json, YAML encoders and
//     log/slog write as `[redacted]`. It has no default.
//   - `jwt.publickeypath`, `jwt.privatekeypath`: the PEM files of the RSA
//     keys for RS256. They have no default.
//   - `jwt.issuer`, `jwt.audience`: when not empty, set as `iss` and `aud`
//     on signed tokens and required of verified ones. Default: empty.
//   - `jwt.expiry`: the lifetime of the tokens [Service.Sign] makes; it
//     must be positive. Default: [DefaultExpiry].
//   - `jwt.leeway`: the clock skew allowed when checking `exp`, `nbf` and
//     `iat`; it must not be negative. Default: 0.
//
// HS256 requires a signing key and both key paths empty. RS256 requires a
// public key path and an empty signing key; the private key path is
// optional, and without it the service only verifies. A setting the
// method does not use is an error rather than ignored.
//
// [Service.Init] resolves the configuration each time it runs. Without
// [WithConfig], it binds [DefaultConfig] from the gas.ConfigProvider,
// which checks the `validate` tags. With WithConfig, it takes a copy of
// the given Config, never changing the caller's struct, and checks the
// same tags; a nil Config is the error [ErrNilConfig]. Either way, the
// rules above then run, and the keys load. Init fails, with an error
// wrapping [ErrInvalidConfig], [ErrNoConfigProvider] or
// [ErrKeyLoadFailed], unless all of it succeeds.
//
// [Service.Config] returns a copy of the configuration the last
// successful Init resolved, defaults included, with SigningKey still a
// gas.Secret, or the zero Config before one. A failed Init and Close
// leave it as it was.
//
// # Keys
//
// An HS256 key is the signing key's bytes, used to both sign and verify.
// Its length is checked, not its randomness; generate it from a secure
// random source.
//
// For RS256, Init reads each key file whole, up to 64 KiB, and parses its
// first PEM block. The public key may be PKIX (`PUBLIC KEY`) or PKCS #1
// (`RSA PUBLIC KEY`), the private key PKCS #8 (`PRIVATE KEY`) or PKCS #1
// (`RSA PRIVATE KEY`), unencrypted. Each must have at least
// [MinRSAKeyBits] bits, and the private key must belong to the public
// key, so that the service verifies every token it signs. A key that
// fails any of this fails Init with an error wrapping ErrKeyLoadFailed
// that names the setting, and [ErrKeyTooSmall] too for a small key; a
// file that does not exist wraps [fs.ErrNotExist]. The error never
// includes the path or the file's contents.
//
// # Signing
//
// [Service.SignWithExpiry] returns a compact JWS signed with the
// configured method, whose claims are the custom claims given, plus:
//
//   - `sub`: the subject.
//   - `jti`: 16 random bytes from crypto/rand, as 22 characters of
//     unpadded URL-safe base64, new for each token.
//   - `iat`: now.
//   - `exp`: now plus the expiry.
//   - `iss` and `aud`: the configured issuer and audience, when not empty.
//
// Times are whole seconds, rounded down, so a token's lifetime is the
// expiry less up to one second. [Service.Sign] is SignWithExpiry with the
// configured expiry. The checks run in this order, and the first that
// fails returns its error and no token:
//
//  1. The service is initialized ([ErrNotInitialized]).
//  2. It has a signing key: RS256 without a private key cannot sign
//     ([ErrSigningKeyNotConfigured]).
//  3. The subject is not empty ([ErrEmptySubject]) and holds at most
//     auth.MaxValueLength bytes ([ErrValueTooLong]), so it fits the
//     subject columns of the module's other services.
//  4. The expiry is positive ([ErrInvalidTTL]).
//  5. No custom claim is named after a registered claim, `aud`, `exp`,
//     `iat`, `iss`, `jti`, `nbf` or `sub` ([ErrReservedClaim]), and each
//     custom claim's value can be encoded as JSON ([ErrInvalidClaim]).
//     The claims are checked in sorted order of their names, and the
//     error names the first that fails, never its value.
//
// The claims map is not changed or kept, and nil means none.
//
// # Verifying
//
// [Service.Verify] accepts a token only when all of this holds:
//
//   - It is a compact JWS whose segments are canonical unpadded base64url
//     and whose header and claims are JSON objects.
//   - Its `alg` is the configured method, exactly, so `none`, another
//     HMAC hash, or an RSA token at an HS256 service are rejected.
//   - Its signature verifies with the configured key.
//   - It has `exp`, and now is before `exp` plus the leeway.
//   - It has `sub`, a string that is not empty.
//   - Its `iat`, if present, is not after now plus the leeway.
//   - Its `nbf`, if present, is not after now plus the leeway.
//   - Its `iss` equals the configured issuer, when one is configured.
//   - Its `aud`, a string or an array of strings, includes the configured
//     audience, when one is configured.
//   - `exp`, `iat` and `nbf`, when present, are numbers, and `sub`, `jti`
//     and `iss` strings, and `aud` a string or an array of strings, even
//     when no issuer or audience is configured.
//
// A `jti` is not required, so tokens from other issuers that share the
// key verify; a token without one has an empty [TokenClaims].TokenID.
// The claims that are not registered come back in
// TokenClaims.CustomClaims, as encoding/json decodes them, so a number
// is a float64.
//
// Verify does not limit the length of `sub`. A token that fails a check
// is denied with an error wrapping [ErrTokenInvalid] and a fixed reason.
// The one exception is a token that passes every check but `exp`: it is
// denied with [ErrTokenExpired]. So an expired token without `sub` is
// invalid, not expired.
//
// # Authenticating
//
// [Service.Authenticate] reads the request's Authorization headers. One
// whose value, trimmed, starts with `Bearer` in any case, then one or
// more spaces, carries a token; headers with another scheme, such as
// `Basic`, are left for other authenticators. With no bearer header, or a
// nil request, the error wraps auth.ErrNoCredentials, which auth.Chain
// skips. With more than one, or a bearer header without a token, it wraps
// ErrTokenInvalid. The token is then verified as Verify does, and
// Verify's error returned as it is. On success the principal has:
//
//   - Subject: the `sub` claim.
//   - Scheme: auth.SchemeJWT.
//   - CredentialID: the `jti` claim, or empty.
//   - Metadata: the custom claims.
//
// A JWT cannot be revoked: the service stores nothing per token and does
// not implement auth.PrincipalRevoker. Keep the expiry short.
//
// # Lifecycle
//
// Until Init succeeds, every method other than Name, Init, Close and
// Config returns an error wrapping ErrNotInitialized, which
// auth.Middleware answers with a 500, not a 401. A successful Init logs
// `service initialized` at info level with `service` set to
// [ServiceName], the signing method and whether the service only
// verifies. It is the only event the service logs. Init may run again,
// after Close or not, and takes up the configuration and key files as
// they are then; a failed Init leaves the service uninitialized, not on
// the previous keys. Close makes the service uninitialized and always
// returns nil. [New]'s constructor fails with [ErrNilLogger] for a nil
// logger; a nil ConfigProvider is accepted, and Init then requires
// WithConfig.
//
// # Concurrency
//
// A Service is safe for concurrent use. Init swaps in what it loaded at
// once, so a call running beside Init uses either the old keys or the new
// ones, never a mix. Init and Close each change the state in one atomic
// step, and the service holds nothing else, such as a background task,
// so an Init and a Close that run at the same time leave it as if one ran
// after the other. Sign, Verify and Authenticate do no I/O.
//
// # Errors
//
// The sentinel errors, to be tested with [errors.Is]:
//
//   - [ErrNilConfig], [ErrNoConfigProvider], [ErrInvalidConfig]: Init
//     could not resolve a valid configuration. ErrInvalidConfig wraps the
//     binder's, the validator's or the rule's error.
//   - [ErrKeyLoadFailed]: Init could not load an RSA key, with
//     [ErrKeyTooSmall] for a key under MinRSAKeyBits.
//   - [ErrNotInitialized]: a method ran before Init succeeded, after a
//     failed Init, or after Close.
//   - [ErrNilLogger]: New's constructor got a nil logger.
//   - [ErrSigningKeyNotConfigured], [ErrEmptySubject], [ErrValueTooLong],
//     [ErrInvalidTTL], [ErrReservedClaim], [ErrInvalidClaim]: Sign
//     refused its arguments.
//   - auth.ErrNoCredentials: Authenticate found no bearer token.
//   - [ErrTokenInvalid]: the token is not valid. It wraps
//     auth.ErrUnauthenticated.
//   - [ErrTokenExpired]: the token has expired. It wraps
//     auth.ErrCredentialsExpired, and so auth.ErrUnauthenticated.
//
// ErrNilConfig, ErrNoConfigProvider, ErrInvalidConfig, ErrNotInitialized
// and ErrNilLogger are aliases of the auth module's shared service
// sentinels, and ErrEmptySubject and ErrValueTooLong of auth's, so they
// match the same errors from the module's other services.
//
// Every error starts with [ServiceName], denials included, and wraps its
// sentinel, so [errors.Is] matches it. No error includes a token, a key,
// a key path, or another configuration value; errors name the setting or
// claim instead. A token is denied with one of these, and nothing else:
//
//	gas/auth/jwt: unauthenticated: credentials expired: token expired
//	gas/auth/jwt: unauthenticated: invalid token: malformed
//	gas/auth/jwt: unauthenticated: invalid token: unknown or missing algorithm
//	gas/auth/jwt: unauthenticated: invalid token: bad signature or unexpected algorithm
//	gas/auth/jwt: unauthenticated: invalid token: missing required claim
//	gas/auth/jwt: unauthenticated: invalid token: claim of the wrong type
//	gas/auth/jwt: unauthenticated: invalid token: issued in the future
//	gas/auth/jwt: unauthenticated: invalid token: not valid yet
//	gas/auth/jwt: unauthenticated: invalid token: issuer not accepted
//	gas/auth/jwt: unauthenticated: invalid token: audience not accepted
//	gas/auth/jwt: unauthenticated: invalid token: more than one bearer token
//
// A request without a bearer token gets:
//
//	gas/auth/jwt: no credentials
//
// Other errors, as returned:
//
//	gas/auth/jwt: service not initialized
//	gas/auth/jwt: logger is nil
//	gas/auth/jwt: config is nil
//	gas/auth/jwt: invalid config: HS256 requires JWT.SigningKey of at least 32 bytes
//	gas/auth/jwt: failed to load key JWT.PublicKeyPath: open: no such file or directory
//	gas/auth/jwt: failed to load key JWT.PublicKeyPath: key too small: RSA key has 1024 bits, fewer than 2048
//	gas/auth/jwt: failed to load key JWT.PrivateKeyPath: key does not match JWT.PublicKeyPath
//	gas/auth/jwt: signing key not configured
//	gas/auth/jwt: empty subject
//	gas/auth/jwt: value too long: subject is longer than 255 bytes
//	gas/auth/jwt: invalid ttl: must be positive
//	gas/auth/jwt: reserved claim "sub"
//	gas/auth/jwt: invalid claim "ch": value cannot be encoded as JSON
package jwt
