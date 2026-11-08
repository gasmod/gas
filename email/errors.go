package email

import "errors"

// The sentinel errors every sender of the module shares, as described in
// the package documentation. A sender's errors start with its own service
// name and the operation, and wrap one of these.
var (
	// ErrNilMessage indicates that Send or SendFromTemplate was given a nil
	// message.
	ErrNilMessage = errors.New("message is nil")
	// ErrInvalidAddress indicates that the sender, the reply-to address or
	// a recipient is not one email address the rules accept, or that there
	// is no sender.
	ErrInvalidAddress = errors.New("invalid address")
	// ErrNoRecipients indicates that a message has no To, Cc or Bcc
	// recipient.
	ErrNoRecipients = errors.New("no recipients")
	// ErrTooManyRecipients indicates that a message has more than
	// MaxRecipients recipients across To, Cc and Bcc.
	ErrTooManyRecipients = errors.New("too many recipients")
	// ErrInvalidSubject indicates that a subject is empty, is not valid
	// UTF-8, or holds a control character, such as a CR, an LF or a NUL.
	ErrInvalidSubject = errors.New("invalid subject")
	// ErrEmptyBody indicates that a message has neither a text body nor an
	// HTML body.
	ErrEmptyBody = errors.New("empty body")
	// ErrInvalidBody indicates that a body is not valid UTF-8.
	ErrInvalidBody = errors.New("invalid body")
	// ErrInvalidHeader indicates that the headers are too many, or that a
	// name or a value breaks the header rules.
	ErrInvalidHeader = errors.New("invalid header")
	// ErrMessageTooLarge indicates that the estimated size of a message is
	// above MaxMessageSize, or that a template rendered more than
	// MaxMessageSize bytes.
	ErrMessageTooLarge = errors.New("message too large")
	// ErrTemplate indicates that SendFromTemplate could not fetch, parse or
	// execute a template.
	ErrTemplate = errors.New("template failed")
	// ErrNotInitialized indicates that a method was called before the
	// sender's first successful Init.
	ErrNotInitialized = errors.New("service not initialized")
	// ErrAlreadyInitialized indicates that Init was called on a sender that
	// is initialized.
	ErrAlreadyInitialized = errors.New("service already initialized")
	// ErrClosed indicates that a method was called after Close, before Init
	// succeeded again, or that Close ended the call.
	ErrClosed = errors.New("service closed")
	// ErrNilLogger indicates that a sender's constructor was given a nil
	// logger.
	ErrNilLogger = errors.New("logger is nil")
	// ErrNilTemplates indicates that a sender's constructor was given a nil
	// gas.TemplateProvider.
	ErrNilTemplates = errors.New("template provider is nil")
	// ErrNilConfig indicates that WithConfig was given a nil configuration.
	ErrNilConfig = errors.New("config is nil")
	// ErrNoConfigProvider indicates that there is neither a configuration
	// given to WithConfig nor a ConfigProvider to bind one from.
	ErrNoConfigProvider = errors.New("no config provider")
	// ErrInvalidConfig indicates that the configuration failed to bind,
	// failed its validate tags, or broke one of the sender's rules.
	ErrInvalidConfig = errors.New("invalid config")
)
