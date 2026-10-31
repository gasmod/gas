// Package email defines what every email sender of the module promises:
// the messages it accepts, how it normalizes their addresses, the
// headers it sends, how it renders templates, how it honors a context,
// its lifecycle, and the errors it returns. The rules are Amazon SES's,
// so that code tested against one sender runs on another. The senders
// implement gas.EmailProvider, gas.Service and gas.ReadyReporter, and
// each lives in its own package:
//
//   - gas/email/ses: Amazon SES, through its API v2, or an emulator that
//     speaks it.
//   - gas/email/memory: an outbox in the process's memory, for tests and
//     local development.
//   - gas/email/emailtest: a mock for tests.
//
// # Usage
//
// Each sender's New returns a constructor in the shape the gas container
// expects, so a sender is registered as gas.EmailProvider:
//
//	gas.WithSingletonService[gas.EmailProvider](ses.New())
//
// A sender then sends a message written in code, or one rendered from
// templates of the app's gas.TemplateProvider:
//
//	err := mailer.Send(ctx, &gas.Email{
//		From:     "Shop <shop@example.com>",
//		To:       []string{"alice@example.com"},
//		Subject:  "Your order",
//		TextBody: "It ships today.",
//	})
//
//	err = mailer.SendFromTemplate(ctx, &gas.TemplatedEmail{
//		SubjectTemplate: "welcome-subject.txt",
//		HTMLTemplate:    "welcome.html",
//		Data:            map[string]string{"Name": "Alice"},
//		Email:           gas.Email{To: []string{"alice@example.com"}},
//	})
//
// After this SES has accepted both messages for delivery, or the memory
// outbox holds them, normalized; the second is sent from the sender's
// FromEmail setting.
//
// # Contract
//
// Every sender, and any gas.EmailProvider that wants to be one, keeps
// these rules:
//
//   - A nil error means the message was accepted for delivery, not that
//     it was delivered: SES can accept a message and drop it, or bounce
//     it later.
//   - Send checks the message, as [CheckEmail] does, before it sends
//     anything, and returns the first rule it breaks; a message that
//     fails is never sent. A sender's FromEmail setting is the sender of
//     a message whose From is empty.
//   - What is sent is the normalized copy CheckEmail returns. Send and
//     SendFromTemplate never change the caller's message, and keep none
//     of its slices or maps.
//   - SendFromTemplate renders the message into a copy, as under
//     Templates, then sends the copy as Send does.
//   - A method checks its arguments first: a nil message is
//     [ErrNilMessage]. Then the sender's state, then the context, then,
//     for SendFromTemplate, the templates, then the rules of CheckEmail,
//     and returns the first failure. The state comes before the rules
//     because the default sender is part of the configuration.
//   - Every method returns the context's error, wrapped, when its context
//     has ended before it starts, and honors the context during the call.
//     Close is the exception: it always closes.
//   - Before its first successful Init, every method but Init and Close
//     returns [ErrNotInitialized]; Init on an initialized sender returns
//     [ErrAlreadyInitialized] and changes nothing; after Close, until Init
//     succeeds again, they return [ErrClosed]. Close on a sender that is
//     not initialized returns nil, and Close then Init restarts the
//     sender.
//   - Close refuses new sends at once, and returns once every send in
//     flight has returned. When its context ends first, it cancels the
//     sends in flight, which return ErrClosed, and returns an error
//     wrapping the context's error; the sender stays closed. A send that
//     Close cancels may still have been accepted by SES.
//   - A constructor refuses a nil gas.TemplateProvider
//     ([ErrNilTemplates]), a nil logger ([ErrNilLogger]) and a nil
//     configuration given to its WithConfig ([ErrNilConfig]), in that
//     order.
//
// The module's conformance tests run every sender through these rules,
// with identical expectations.
//
// # Addresses
//
// From, ReplyTo, and each entry of To, Cc and Bcc hold one email
// address, as net/mail reads one: a bare address such as
// `alice@example.com`, or one with a display name such as
// `Alice Liddell <alice@example.com>`. [NormalizeAddress] reads it and
// writes it back as the senders send it:
//
//   - A display name is kept. One that is not ASCII is written as an
//     RFC 2047 encoded word, as SES requires, and one that needs quotes is
//     quoted. A comment, such as `(Alice)` in
//     `alice@example.com (Alice)`, becomes the display name when there is
//     no other, as net/mail reads it. A display name that is not valid
//     UTF-8, or that holds a control character, as under Content, such as
//     one decoded from an encoded word, is refused.
//   - An address without a display name loses its angle brackets.
//   - A domain that is not ASCII, such as `bücher.example`, is converted
//     to Punycode, `xn--bcher-kva.example`, with the IDNA lookup profile;
//     one the profile refuses is refused, and so is one whose Punycode
//     does not convert back to the domain given, but for case and Unicode
//     normalization, so that the profile's mapping cannot drop an
//     ignorable code point, such as a soft hyphen, fold a fullwidth
//     letter, or turn an ideographic full stop into a dot. Case is
//     compared with strings.ToLower, so a domain holding a capital dotted
//     I (U+0130), which the profile maps to `i` and a combining dot, is
//     refused; write it in lower case. An ASCII domain other than a
//     domain literal is sent in lower case, and each of its Punycode (`xn--`) labels must decode and
//     encode back to itself, so one mailbox has one normalized form. A
//     domain literal holds an IP address, such as
//     `[192.0.2.1]`, so never an `@` or `=?`.
//   - A local part that is not ASCII is refused: SES does not support
//     SMTPUTF8, and Punycode does not apply to it. One holding an `@`,
//     even quoted, or the `=?` that starts an encoded word is refused.
//   - An empty entry, a list of addresses in one entry, an address with a
//     CR or an LF, and anything else net/mail does not read as one
//     address are refused, as is an address that net/mail no longer reads
//     once normalized.
//
// An allow or deny list of addresses must hold, and be matched against,
// the output of NormalizeAddress, not the text a caller gave: two texts
// that differ, by a comment, case in the display name or the form of the
// domain, can be the same address.
//
// Normalizing a normalized address returns it unchanged. A refused
// address is [ErrInvalidAddress], and the error names the field, such as
// `To[2]`, and the reason, never the address. A message needs a sender,
// From or the sender's default, and at least one recipient across To, Cc
// and Bcc ([ErrNoRecipients]), and at most [MaxRecipients], 50
// ([ErrTooManyRecipients]), which SES counts the same way. The rules run
// in this order: the sender, the reply-to address, the count of the
// recipients, then each recipient, To, Cc then Bcc. An address may
// appear twice; SES then delivers as it decides.
//
// # Content
//
// The subject is required, valid UTF-8, and without a control character
// ([ErrInvalidSubject]): a C0 control but the horizontal tab, such as a
// CR, an LF or a NUL, which would let it end the header it is sent in,
// DEL, a C1 control, or the line and paragraph separators U+2028 and
// U+2029. Display names are held to the same rule, and so is a subject
// rendered from template data. At least one of TextBody and HTMLBody is required
// ([ErrEmptyBody]); a body that is empty is left out of the message, not
// sent as an empty part, so a message with one body is sent as a
// single-part message. Each body must be valid UTF-8 ([ErrInvalidBody]).
// The bodies are sent with the UTF-8 character set, which SES encodes
// as MIME requires. The SES sender sends a subject that is not ASCII as
// RFC 2047 encoded words, base64 of its UTF-8, which it encodes itself;
// the memory sender keeps the subject as given. These rules run after
// the addresses.
//
// # Headers
//
// gas.Email.Headers adds headers to the message, which SES's Simple
// content sends as they are. The rules are SES's: at most [MaxHeaders],
// 15; a name is 1 to [MaxHeaderNameLength], 126, printable ASCII
// characters but `:`; a value is 1 to [MaxHeaderValueLength], 995,
// printable ASCII characters, space included, so a value cannot hold a
// CR or an LF; a name and its value are at most [MaxHeaderLength], 996,
// together. A name the module or SES sets is refused, in any case, as
// [IsReservedHeader] reports, so that a header can never override an
// address, the subject or the MIME structure, nor redirect replies,
// receipts or bounces: `From`, `To`, `Cc`, `Bcc`, `Reply-To`, `Subject`,
// `Date`, `Message-ID`, `MIME-Version`, `Return-Path`, `Sender`,
// `Disposition-Notification-To`, `Return-Receipt-To`, `Errors-To`,
// `Bounces-To`, and any name starting with `Content-`, `Resent-` or
// `X-SES-`. Two
// names that differ only in case are refused, as a header map cannot
// order them. Non-ASCII text in a value must be encoded by the caller,
// such as with mime.QEncoding. A broken rule is [ErrInvalidHeader], and
// the error quotes neither the name nor the value, but for the usual
// name of a reserved header. The count is checked first, then each
// header in the sorted order of its name, which is also the order they
// are sent in. These rules run after the content.
//
// # Limits
//
// SES refuses a message above 40 MB after base64 encoding. Before it
// sends anything, and after every other rule, a sender estimates the
// size of the message, as [MessageSize] does: the subject, as RFC 2047
// encoded words when it is not ASCII, each body base64-encoded in lines
// of 76 characters, the headers, and the addresses, each with the length
// of its header line. An estimate above [MaxMessageSize], 40,000,000
// bytes, SES's 40 MB read as decimal megabytes, is
// [ErrMessageTooLarge], so that a message
// SES would refuse is not uploaded, and retried, first. The estimate
// leaves out the parts of the MIME structure SES adds, so SES's own
// count is the one that holds: a message just under the limit can still
// be refused by SES.
//
// # Templates
//
// SendFromTemplate renders gas.TemplatedEmail into a copy of its
// embedded gas.Email: SubjectTemplate into Subject and TextTemplate into
// TextBody with text/template, HTMLTemplate into HTMLBody with
// html/template, which escapes the data for HTML. Each template is
// fetched by its name from the gas.TemplateProvider, parsed alone, so it
// cannot call another, and executed with Data. A template name left empty
// keeps that field of the embedded Email. Templates are fetched and parsed
// on every call. One trailing CRLF or LF of a rendered subject is cut, as
// a template file usually ends with one; the subject rule then applies.
// The fields render in the order subject, text, HTML, and the first
// failure ends the call, with nothing sent and the caller's message
// unchanged. Text/template does not escape, so a subject rendered from
// data holding a line break is refused by the subject rule. A template
// that renders more than [MaxMessageSize] bytes is stopped there, and is
// [ErrMessageTooLarge].
//
// A template that cannot be fetched, parsed or executed is
// [ErrTemplate], and the error names the field and the template name,
// never the data, the template's text or the template engine's error,
// which can quote both. A failed fetch also wraps the
// gas.TemplateProvider's error, for errors.Is, without showing its text.
// A template name built from data, such as an address, shows in the
// error; name templates by what they are.
//
// # Security
//
// Addresses, subjects, bodies, header values and template data are
// application data, often personal. No error a sender makes and no log
// it writes holds any of them, nor an endpoint or a credential. The
// errors name the operation, the field and the rule instead. A sender
// never builds MIME itself: SES assembles the message from the
// normalized fields, and the rules above keep a CR or an LF out of every
// field that becomes a header.
//
// # Concurrency
//
// Every sender is safe for concurrent use, as each sender's package
// describes. The mock is too, except for setting its fields while it is
// called. The functions of this package are safe for concurrent use.
//
// # Errors
//
// Every error a sender returns starts with its service name, such as
// `gas/email/memory`, then the operation, `send` or `send from
// template`, then what failed, and wraps one of these sentinels, to be
// tested with [errors.Is]:
//
//   - [ErrNilMessage]: the message is nil.
//   - [ErrInvalidAddress], [ErrNoRecipients], [ErrTooManyRecipients]: as
//     under Addresses.
//   - [ErrInvalidSubject], [ErrEmptyBody], [ErrInvalidBody]: as under
//     Content.
//   - [ErrInvalidHeader]: as under Headers.
//   - [ErrMessageTooLarge]: as under Limits and Templates.
//   - [ErrTemplate]: as under Templates.
//   - [ErrNotInitialized], [ErrAlreadyInitialized], [ErrClosed]: as under
//     Contract.
//   - [ErrNilTemplates], [ErrNilLogger]: a sender's constructor got a nil
//     dependency.
//   - [ErrNilConfig], [ErrNoConfigProvider], [ErrInvalidConfig]: a sender
//     could not resolve a valid configuration.
//
// The errors of this package's checks, as returned, before a sender adds
// its service name and the operation:
//
//	message is nil
//	invalid address: From: no sender, and no default sender
//	invalid address: To[0]: empty
//	invalid address: To[0]: not one email address
//	invalid address: To[0]: the display name is not valid UTF-8
//	invalid address: To[0]: the display name holds a control character
//	invalid address: To[0]: the local part is not ASCII
//	invalid address: To[0]: the local part holds an at sign or an encoded word
//	invalid address: To[0]: the domain is not a valid domain name
//	no recipients
//	too many recipients: 51, at most 50
//	invalid subject: empty
//	invalid subject: not valid UTF-8
//	invalid subject: holds a control character
//	empty body
//	invalid body: TextBody is not valid UTF-8
//	invalid body: HTMLBody is not valid UTF-8
//	invalid header: 16 headers, at most 15
//	invalid header: a name is empty or longer than 126 bytes
//	invalid header: a name holds a character other than printable ASCII but ':'
//	invalid header: "Reply-To" is set by the module or SES
//	invalid header: a name starting with Content- is set by the module or SES
//	invalid header: a name starting with Resent- is set by the module or SES
//	invalid header: a name starting with X-SES- is set by the module or SES
//	invalid header: a value is empty or longer than 995 bytes
//	invalid header: a value holds a character other than printable ASCII
//	invalid header: a name and its value are longer than 996 bytes together
//	invalid header: two names differ only in case
//	message too large: about 44481865 bytes, more than 40000000
//	message too large: text "large.txt": renders more than 40000000 bytes
//	template failed: subject "welcome-subject.txt": fetch failed
//	template failed: text "welcome.txt": parse failed
//	template failed: HTML "welcome.html": execute failed
//
// [NormalizeAddress] returns the address errors without the field, such
// as `invalid address: not one email address`.
package email
