// Package ses is an email sender on Amazon SES, as a gas.EmailProvider.
// It sends with the SendEmail operation of the SES API v2 and the AWS SDK
// for Go v2, so it also reaches an emulator that speaks the SES API v2,
// such as aws-ses-v2-local. [Service] implements gas.Service,
// gas.EmailProvider and gas.ReadyReporter, and keeps the contract of the
// email package.
//
// # Usage
//
// In an app, the container builds the sender from [New] and calls its
// Init. By hand, with a configuration given in code:
//
//	cfg := ses.DefaultConfig()
//	cfg.Email.Region = "eu-west-1"
//	cfg.Email.FromEmail = "Shop <shop@example.com>"
//
//	mailer, err := ses.New(ses.WithConfig(cfg))(templates, nil, logger)
//	if err != nil {
//		return err
//	}
//	if err := mailer.Init(ctx); err != nil {
//		return err
//	}
//	defer mailer.Close(ctx)
//
//	err = mailer.Send(ctx, &gas.Email{
//		To:       []string{"alice@example.com"},
//		Subject:  "Your order",
//		TextBody: "It ships today.",
//	})
//
// After this SES has accepted the message for delivery, from
// `Shop <shop@example.com>`, an identity SES must have verified.
//
// # Configuration
//
// The settings live in the `email` section, in [Settings]:
//
//   - `email.region`: the AWS region. Default: none; the AWS SDK's
//     default chain decides, as under Credentials.
//   - `email.endpoint`: the URL requests go to, an absolute `http` or
//     `https` URL with a host, and with no user information, query,
//     fragment, or path but `/`, such as an emulator's. Default: none, the
//     region's SES endpoint. An `http` endpoint sends every request in
//     clear text, the access key ID, the session token and the messages
//     included; the secret key signs the requests and is not sent. Use
//     it only for an emulator on a network of its own.
//   - `email.fromemail`: the sender of a message whose From is empty, one
//     email address as the email package reads one, which Init
//     normalizes. Default: none, so every message needs a From.
//   - `email.accesskeyid`, `email.secretaccesskey`, `email.sessiontoken`:
//     static credentials, as under Credentials. The access key ID is not
//     a secret, but it maps to the AWS account ID, and the sender never
//     logs it. The secret key and the token are each a [Secret], which
//     prints, marshals to JSON or YAML, and logs with log/slog as
//     `[redacted]`, and on which encoding/gob fails rather than encode
//     the value. Default: none.
//   - `email.optimeout`: the bound of each send, retries included, above
//     0, as under Timeouts. Default: [DefaultOpTimeout] (10s).
//   - `email.maxretries`: how many times a failed send is retried, 0 to
//     [MaxRetries] (10), with the AWS SDK's standard retryer and its
//     backoff, whatever `AWS_MAX_ATTEMPTS` and `AWS_RETRY_MODE` say.
//     Default: [DefaultMaxRetries], 2, so 3 attempts.
//
// [Service.Init] resolves the configuration each time it runs. Without
// [WithConfig], it binds [DefaultConfig] from the gas.ConfigProvider,
// which checks the `validate` tags; a nil ConfigProvider is the error
// email.ErrNoConfigProvider. With WithConfig, it takes a copy of the
// given Config, never changing the caller's struct, and checks the same
// tags. Either way, these rules then run, in this order, and the first
// that fails is the error:
//
//  1. `endpoint`, when set, is an absolute `http` or `https` URL with a
//     host, and with no user information, query, fragment, or path but
//     `/`.
//  2. `fromemail`, when set, is one email address.
//
// A failure wraps email.ErrInvalidConfig and names the setting, never its
// value.
//
// [Service.Config] returns a copy of the configuration the last successful
// Init resolved, defaults included, or the zero Config before one; a
// failed Init and Close leave it as it was. Its `fromemail` is the
// normalized address, and its `secretaccesskey` and `sessiontoken` stay
// a [Secret], which prints as `[redacted]`.
//
// # Credentials
//
// Init builds the client with the AWS SDK's default configuration, which
// comes from the environment, such as `AWS_REGION` and
// `AWS_ACCESS_KEY_ID`, the shared configuration and credentials files and
// their profile, and the container and instance roles. Its HTTP client is
// the sender's own, on the AWS SDK's default transport, and follows no
// redirect, as the SDK's does not. A `region` setting takes precedence
// over the chain's region; without one, a region the chain does not find
// fails Init with [ErrNoRegion]. Static credentials are used when both
// `accesskeyid` and `secretaccesskey` are set, with `sessiontoken` when it
// is set too; with either key empty, the chain's credentials apply, as
// config/secretsmanager and gas/queue/sqs do it, and the other static
// values are not used. When some of them are set, but not both keys,
// Init logs `static credentials incomplete, using the default chain` at
// warn level, with `missing` naming the keys that are not set, never a
// value. AWS configuration that fails to load, such as a profile that
// does not exist, fails Init with email.ErrInvalidConfig.
//
// Init retrieves the credentials within its context and `optimeout`, so
// that it fails, with [ErrNoCredentials], when there are none, rather
// than the first send. The client then refreshes them as they expire.
// Init sends no request to SES. The credentials need the
// `ses:SendEmail` permission.
//
// [WithClient] gives the sender a client to use instead, such as an
// *sesv2.Client built elsewhere or a fake of [API]; `region`, `endpoint`,
// the credentials and `maxretries` then have no effect, and Init neither
// checks a region nor retrieves credentials.
//
// # Requests
//
// Send is one SendEmail request with Simple content, which SES assembles
// into a MIME message: `FromEmailAddress` is the normalized sender,
// `ReplyToAddresses` the reply-to address, when there is one, and
// `Destination` the To, Cc and Bcc lists, each left out when it is empty.
// The subject and each body that is not empty are sent with the `UTF-8`
// character set; an empty body is left out. A subject that is not ASCII
// is sent as RFC 2047 encoded words, base64 of its UTF-8, which the
// sender encodes itself, so that SES sends it as it is. The headers are
// sent in the sorted order of their names. No configuration set, tag,
// list management option or attachment is sent. The message ID SES
// returns is not kept: the interface returns only an error.
//
// # Timeouts
//
// Every send runs within its context and `optimeout`, whichever ends
// first, the AWS SDK's retries and their backoff included, so a server
// that stops answering cannot hold a send longer than `optimeout`, even
// with a context that never ends. A SendFromTemplate fetches its
// templates within its context alone, then sends as Send does.
//
// The SDK retries a send after a network error, a throttle, such as
// TooManyRequestsException or LimitExceededException, and a server
// error, up to `maxretries` times. SES has no idempotency key, so a send
// whose reply was lost and that is retried can deliver the message
// twice. Set `maxretries` to 0 where a duplicate is worse than a lost
// message, and retry in the app with what it knows.
//
// A send that fails at the connection returns an error wrapping
// [ErrConnect] and a reason, never the AWS SDK's error, which holds the
// endpoint:
//
//   - `timed out`: a deadline passed, in a context or on the network.
//     The error then wraps context.DeadlineExceeded too.
//   - `connection refused`: the endpoint refused the TCP connection.
//   - `unreachable`: any other network error, such as a failed name
//     lookup.
//
// A send that got an error reply of SES, as below, and whose retries a
// deadline then cut short is told by the reply's code, not as `timed
// out`, and wraps context.DeadlineExceeded too: a throttled send that
// `optimeout` ends is `throttled`.
//
// A send whose own context ended returns the context's error after the
// operation, wrapped, without ErrConnect. Which of the two a timeout is
// depends on which deadline was in force: a timeout of a send whose
// context's deadline is no later than `optimeout`'s is the context's
// error.
//
// An error reply of SES returns an error wrapping [ErrCommand], with a
// reason told by its AWS error code, and the code, in parentheses, when
// it is one SES or the AWS APIs document; the reply's message, which can
// quote an address, is never kept, nor a code of any other text, nor the
// AWS SDK's error. The reasons, in the order they are told:
//
//   - `not verified`: MessageRejected whose message says an address is
//     not verified, such as the sender, or a recipient of an account in
//     the SES sandbox, also wrapping [ErrNotVerified] and [ErrRejected];
//     and MailFromDomainNotVerifiedException, also wrapping
//     ErrNotVerified.
//   - `rejected`: any other MessageRejected, also wrapping ErrRejected.
//   - `throttled`: TooManyRequestsException, LimitExceededException and
//     the other throttles, past the retries, also wrapping
//     [ErrThrottled].
//   - `sending paused`: SendingPausedException and
//     AccountSuspendedException, also wrapping [ErrSendingPaused].
//   - `access denied`: AccessDenied, InvalidClientTokenId,
//     SignatureDoesNotMatch, ExpiredToken and the other codes of refused
//     credentials or permissions, also wrapping [ErrAccessDenied].
//   - `invalid reply`: a reply SES does not send: a body larger than 1
//     MiB, which the client Init builds stops reading at, or a client of
//     [WithClient] that returns neither an output nor an error.
//   - `failed`: any other reply, such as BadRequestException, or a send
//     that failed for another reason, such as a reply that is not valid
//     JSON.
//
// # Lifecycle
//
// A successful Init publishes the client, logs `service initialized` at
// info level with `region`, whether an `endpoint`, a `client` and a
// default sender, `from`, were given, `credentials`, where the client
// Init built takes them from, `static` or `default chain`, left out with
// [WithClient], `op_timeout` and `max_retries`, and only then do the
// methods use it. Init on an initialized sender returns
// email.ErrAlreadyInitialized and changes nothing. A failed Init leaves
// the sender as it was.
//
// [Service.Close] makes the sender closed, so that every later call
// returns email.ErrClosed, waits for every send in flight to return,
// closes the idle connections of the client Init built, and logs
// `service closed` at info level. While its context lasts, it does not
// cut a send short. When its context ends first, Close cancels the sends
// in flight, which return email.ErrClosed, though SES may have accepted
// their messages, logs `service closed, sends in flight canceled` at warn
// level, and returns an error wrapping the context's error; the sender
// stays closed. Close on a sender that is not initialized returns nil and
// logs nothing. Init after Close builds a new client, with a
// configuration resolved afresh.
//
// [New]'s constructor fails with email.ErrNilTemplates for a nil
// gas.TemplateProvider, email.ErrNilLogger for a nil logger, and
// email.ErrNilConfig for WithConfig(nil); a nil ConfigProvider is
// accepted, and Init then requires WithConfig. A nil client given to
// WithClient fails Init with [ErrNilClient].
//
// # Readiness
//
// [Service.CheckReady] does no I/O: it fails only when the sender is not
// initialized or its context has ended. It does not ask SES whether the
// account may send.
//
// # Client
//
// [Service.Client] returns the *sesv2.Client, for the operations the
// sender has no method for, such as GetAccount, or nil when the sender is
// not initialized or was given another [API]. Requests sent on it are
// bounded only by their own context, and return the AWS SDK's errors as
// they are, which can hold the endpoint and addresses.
//
// # Security
//
// No error the sender makes and no log it writes holds an address, a
// subject, a body, a header value, template data, the endpoint, or a
// credential, the access key ID included. The logs carry the region,
// whether an endpoint, a client and a default sender were given, where
// the credentials come from, the names of the static keys missing, the
// operation timeout and the retries. fmt, encoding/json, YAML encoders
// that use encoding.TextMarshaler, and log/slog write the secret key and
// the session token of a [Settings] or a [Config] as `[redacted]`, and
// encoding/gob fails on them rather than encode them. Static
// credentials in configuration are a secret of the app's; prefer a role,
// which the default chain finds. An `http` endpoint sends the access key
// ID, the session token and the messages in clear text, as under
// Configuration. The client Init builds reads no reply past 1 MiB, so an
// endpoint cannot make it read without bound, and logs nothing: the AWS
// SDK's default logger, which writes raw transport errors holding the
// endpoint, is replaced with one that discards them.
//
// # Concurrency
//
// A Service is safe for concurrent use. Init and Close run one at a time.
// Init swaps in what it built at once, so a send, which takes no lock
// but to count itself in flight, uses one Init's client throughout.
//
// # Errors
//
// The sentinel errors of this package, to be tested with [errors.Is]:
//
//   - [ErrNoRegion]: Init found no region.
//   - [ErrNoCredentials]: Init could not retrieve credentials.
//   - [ErrNilClient]: WithClient was given a nil client.
//   - [ErrConnect]: a send got no reply, as under Timeouts.
//   - [ErrCommand]: SES replied with an error, or with an invalid reply,
//     or a send failed for another reason, as under Timeouts.
//   - [ErrRejected], [ErrNotVerified], [ErrThrottled],
//     [ErrSendingPaused], [ErrAccessDenied]: kinds of ErrCommand.
//
// The other errors wrap a sentinel of the email package, as the email
// package describes. Every error starts with [ServiceName] and the
// operation. A configuration that fails its tags gets the validator's
// error after `gas/email/ses: init: invalid config: validate config: `,
// such as:
//
//	Key: 'Config.Email.OpTimeout' Error:Field validation for 'OpTimeout' failed on the 'gt' tag
//
// The other errors, as returned, with `send` standing for both send
// operations where they fail the same way:
//
//	gas/email/ses: template provider is nil
//	gas/email/ses: logger is nil
//	gas/email/ses: config is nil
//	gas/email/ses: init: no config provider
//	gas/email/ses: init: invalid config: Email.Endpoint must be an http or https URL with a host and nothing more but /
//	gas/email/ses: init: invalid config: Email.FromEmail must be one email address
//	gas/email/ses: init: invalid config: AWS configuration failed to load
//	gas/email/ses: init: no region
//	gas/email/ses: init: no credentials: failed
//	gas/email/ses: init: no credentials: timed out
//	gas/email/ses: init: no credentials: timed out: context deadline exceeded
//	gas/email/ses: init: client is nil
//	gas/email/ses: init: service already initialized
//	gas/email/ses: init: context canceled
//	gas/email/ses: ready: service not initialized
//	gas/email/ses: send: message is nil
//	gas/email/ses: send: service not initialized
//	gas/email/ses: send: service closed
//	gas/email/ses: send: context canceled
//	gas/email/ses: send: context deadline exceeded
//	gas/email/ses: send: invalid address: To[0]: not one email address
//	gas/email/ses: send: timed out
//	gas/email/ses: send: connection refused
//	gas/email/ses: send: unreachable
//	gas/email/ses: send: not verified (MessageRejected)
//	gas/email/ses: send: not verified (MailFromDomainNotVerifiedException)
//	gas/email/ses: send: rejected (MessageRejected)
//	gas/email/ses: send: throttled (TooManyRequestsException)
//	gas/email/ses: send: sending paused (SendingPausedException)
//	gas/email/ses: send: access denied (AccessDeniedException)
//	gas/email/ses: send: failed (BadRequestException)
//	gas/email/ses: send: failed
//	gas/email/ses: send: invalid reply
//	gas/email/ses: send from template: template failed: subject "welcome-subject.txt": fetch failed
//	gas/email/ses: close: context deadline exceeded
package ses
