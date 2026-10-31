package email

import (
	"errors"
	"fmt"
	"maps"
	"mime"
	"net/mail"
	"slices"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/idna"
	"golang.org/x/text/unicode/norm"

	"github.com/gasmod/gas"
)

// The limits of a message, as described in the package documentation.
const (
	// MaxRecipients is the most recipients a message has across To, Cc
	// and Bcc, SES's limit.
	MaxRecipients = 50
	// MaxHeaders is the most headers a message has, SES's limit.
	MaxHeaders = 15
	// MaxHeaderNameLength is the longest a header name is, in bytes.
	MaxHeaderNameLength = 126
	// MaxHeaderValueLength is the longest a header value is, in bytes.
	MaxHeaderValueLength = 995
	// MaxHeaderLength is the longest a header name and its value are
	// together, in bytes.
	MaxHeaderLength = 996
	// MaxMessageSize is the largest estimated size of a message, in
	// bytes: 40,000,000, SES's 40 MB after base64 encoding, read as
	// decimal megabytes so as to stay under SES's limit.
	MaxMessageSize = 40_000_000
)

// The reasons an address is refused. They name no part of the address.
var (
	// errAddressEmpty reports an empty address.
	errAddressEmpty = errors.New("empty")
	// errAddressSyntax reports text that is not one email address.
	errAddressSyntax = errors.New("not one email address")
	// errAddressName reports a display name with a control character.
	errAddressName = errors.New("the display name holds a control character")
	// errAddressNameUTF8 reports a display name, such as one decoded from
	// an encoded word, that is not valid UTF-8.
	errAddressNameUTF8 = errors.New("the display name is not valid UTF-8")
	// errAddressLocal reports a local part with a character outside ASCII.
	errAddressLocal = errors.New("the local part is not ASCII")
	// errAddressLocalChars reports a local part holding an `@` or the
	// `=?` that starts an encoded word.
	errAddressLocalChars = errors.New("the local part holds an at sign or an encoded word")
	// errAddressDomain reports a domain that is not a valid
	// internationalized domain name.
	errAddressDomain = errors.New("the domain is not a valid domain name")
)

// The header rules' errors. They quote neither a name nor a value, but
// for the name of a header the module or SES sets.
var (
	// errHeaderName reports a name of the wrong length.
	errHeaderName = fmt.Errorf(
		"%w: a name is empty or longer than %d bytes", ErrInvalidHeader, MaxHeaderNameLength,
	)
	// errHeaderNameChars reports a name with a character SES does not
	// allow.
	errHeaderNameChars = fmt.Errorf(
		"%w: a name holds a character other than printable ASCII but ':'", ErrInvalidHeader,
	)
	// errHeaderValue reports a value of the wrong length.
	errHeaderValue = fmt.Errorf(
		"%w: a value is empty or longer than %d bytes", ErrInvalidHeader, MaxHeaderValueLength,
	)
	// errHeaderValueChars reports a value with a character SES does not
	// allow.
	errHeaderValueChars = fmt.Errorf(
		"%w: a value holds a character other than printable ASCII", ErrInvalidHeader,
	)
	// errHeaderLength reports a name and value too long together.
	errHeaderLength = fmt.Errorf(
		"%w: a name and its value are longer than %d bytes together", ErrInvalidHeader,
		MaxHeaderLength,
	)
	// errHeaderDuplicate reports two names that differ only in case.
	errHeaderDuplicate = fmt.Errorf("%w: two names differ only in case", ErrInvalidHeader)
)

// The subject and body rules' errors.
var (
	// errSubjectEmpty reports an empty subject.
	errSubjectEmpty = fmt.Errorf("%w: empty", ErrInvalidSubject)
	// errSubjectUTF8 reports a subject that is not valid UTF-8.
	errSubjectUTF8 = fmt.Errorf("%w: not valid UTF-8", ErrInvalidSubject)
	// errSubjectControl reports a subject with a control character, as
	// isControl tells one.
	errSubjectControl = fmt.Errorf("%w: holds a control character", ErrInvalidSubject)
	// errNoSender reports a message without a sender.
	errNoSender = fmt.Errorf("%w: From: no sender, and no default sender", ErrInvalidAddress)
)

// reservedHeaders are the names of the headers the module or SES sets,
// or that redirect replies, receipts or bounces, in their usual case. A
// name starting with one of reservedPrefixes is reserved too.
var reservedHeaders = []string{
	"Bcc", "Bounces-To", "Cc", "Date", "Disposition-Notification-To", "Errors-To", "From",
	"MIME-Version", "Message-ID", "Reply-To", "Return-Path", "Return-Receipt-To", "Sender",
	"Subject", "To",
}

// reservedPrefixes start the names of reserved headers, in their usual
// case: the MIME content headers, which SES sets, the resent headers,
// and SES's own X-SES- headers.
var reservedPrefixes = []string{"Content-", "Resent-", "X-SES-"}

// subjectCharset is the character set a subject that is not ASCII is
// encoded with, as SES's sender sends it.
const subjectCharset = "UTF-8"

// NormalizeAddress returns addr, one email address with or without a
// display name, in the form the senders send, as described in the
// package documentation: an IDN domain in Punycode, a display name
// encoded as RFC 2047 when it is not ASCII, and no angle brackets
// without a display name. It fails with an error wrapping
// ErrInvalidAddress and a reason, never the address.
func NormalizeAddress(addr string) (string, error) {
	out, err := normalize(addr)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidAddress, err)
	}

	return out, nil
}

// normalize returns the normalized addr, or the reason it is refused.
func normalize(addr string) (string, error) {
	if addr == "" {
		return "", errAddressEmpty
	}

	a, err := mail.ParseAddress(addr)
	if err != nil {
		return "", errAddressSyntax
	}

	err = checkName(a.Name)
	if err != nil {
		return "", err
	}

	// ParseAddress returns an address with exactly one `@` outside a
	// quoted local part, so the last one splits it.
	at := strings.LastIndexByte(a.Address, '@')
	local, domain := a.Address[:at], a.Address[at+1:]

	switch {
	case !isASCII(local):
		return "", errAddressLocal
	case strings.Contains(local, "@") || strings.Contains(local, "=?"):
		return "", errAddressLocalChars
	}

	domain, err = checkDomain(domain)
	if err != nil {
		return "", err
	}

	a.Address = local + "@" + domain

	out := a.String()

	// The rebuilt address must still be one address net/mail reads.
	_, err = mail.ParseAddress(out)
	if err != nil {
		return "", errAddressSyntax
	}

	if a.Name == "" {
		// String writes `<addr>` for an address without a name.
		out = out[1 : len(out)-1]
	}

	return out, nil
}

// checkName returns the reason the display name is refused, or nil.
func checkName(name string) error {
	switch {
	case !utf8.ValidString(name):
		return errAddressNameUTF8
	case strings.ContainsFunc(name, isControl):
		return errAddressName
	default:
		return nil
	}
}

// checkDomain returns domain as it is sent: a domain literal as it is, an
// ASCII domain in lower case, its Punycode labels checked, and one that
// is not ASCII in
// Punycode, when it round-trips. A domain literal is ASCII, and net/mail
// reads only an IP address in one, so it cannot hold an `@` or `=?`.
func checkDomain(domain string) (string, error) {
	if strings.HasPrefix(domain, "[") {
		// A domain literal keeps its case: net/mail reads the `IPv6:`
		// tag only as written.
		return domain, nil
	}

	if isASCII(domain) {
		return checkASCIIDomain(strings.ToLower(domain))
	}

	ascii, err := idna.Lookup.ToASCII(domain)
	if err != nil || !isASCII(ascii) {
		return "", errAddressDomain
	}

	// The Punycode must read back as the domain given, but for case and
	// normalization form, so that IDNA's mapping cannot drop an ignorable
	// code point, fold a fullwidth letter or turn U+3002 into a dot.
	back, err := idna.Lookup.ToUnicode(ascii)
	if err != nil || back != norm.NFC.String(strings.ToLower(domain)) {
		return "", errAddressDomain
	}

	return ascii, nil
}

// checkASCIIDomain returns domain, in lower case, when each of its
// Punycode (`xn--`) labels decodes and encodes back to itself, so that
// one mailbox has one normalized form.
func checkASCIIDomain(domain string) (string, error) {
	if !strings.HasPrefix(domain, "xn--") && !strings.Contains(domain, ".xn--") {
		return domain, nil
	}

	unicode, err := idna.Lookup.ToUnicode(domain)
	if err != nil {
		return "", errAddressDomain
	}

	back, err := idna.Lookup.ToASCII(unicode)
	if err != nil || back != domain {
		return "", errAddressDomain
	}

	return domain, nil
}

// isControl reports whether r is a control character that the rules
// refuse in a display name and a subject: a C0 control but HTAB, DEL, a
// C1 control, or the line and paragraph separators U+2028 and U+2029.
func isControl(r rune) bool {
	return r < ' ' && r != '\t' || r == 0x7f || r >= 0x80 && r <= 0x9f || r == 0x2028 ||
		r == 0x2029
}

// isASCII reports whether s holds only ASCII.
func isASCII(s string) bool {
	for i := range len(s) {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}

	return true
}

// IsReservedHeader reports whether name is the name of a header the
// module or SES sets, in any case, as described in the package
// documentation.
func IsReservedHeader(name string) bool {
	_, ok := reservedName(name)
	if ok {
		return true
	}

	_, ok = reservedPrefix(name)

	return ok
}

// reservedPrefix returns the usual case of the reserved prefix name
// starts with, in any case.
func reservedPrefix(name string) (string, bool) {
	for _, p := range reservedPrefixes {
		if len(name) >= len(p) && strings.EqualFold(name[:len(p)], p) {
			return p, true
		}
	}

	return "", false
}

// reservedName returns the usual case of name, when it is one of
// reservedHeaders in any case.
func reservedName(name string) (string, bool) {
	for _, r := range reservedHeaders {
		if strings.EqualFold(r, name) {
			return r, true
		}
	}

	return "", false
}

// CheckHeader returns nil when the header name: value keeps the header
// rules, and else an error wrapping ErrInvalidHeader, as described in the
// package documentation. The error quotes neither the name nor the value,
// but for the usual name of a reserved header.
func CheckHeader(name, value string) error {
	err := checkHeaderName(name)
	if err != nil {
		return err
	}

	if value == "" || len(value) > MaxHeaderValueLength {
		return errHeaderValue
	}

	for i := range len(value) {
		if c := value[i]; c < ' ' || c > '~' {
			return errHeaderValueChars
		}
	}

	if len(name)+len(value) > MaxHeaderLength {
		return errHeaderLength
	}

	return nil
}

// checkHeaderName checks a header name: its length, its characters, then
// that it is not reserved.
func checkHeaderName(name string) error {
	if name == "" || len(name) > MaxHeaderNameLength {
		return errHeaderName
	}

	for i := range len(name) {
		if c := name[i]; c < '!' || c > '~' || c == ':' {
			return errHeaderNameChars
		}
	}

	if r, ok := reservedName(name); ok {
		return fmt.Errorf("%w: %q is set by the module or SES", ErrInvalidHeader, r)
	}

	if p, ok := reservedPrefix(name); ok {
		return fmt.Errorf(
			"%w: a name starting with %s is set by the module or SES", ErrInvalidHeader, p,
		)
	}

	return nil
}

// checkHeaders checks the headers: their count, then each, in the sorted
// order of their names, then that no two names differ only in case.
func checkHeaders(headers map[string]string) error {
	if len(headers) > MaxHeaders {
		return fmt.Errorf(
			"%w: %d headers, at most %d", ErrInvalidHeader, len(headers), MaxHeaders,
		)
	}

	names := slices.Sorted(maps.Keys(headers))
	seen := make(map[string]bool, len(names))

	for _, name := range names {
		err := CheckHeader(name, headers[name])
		if err != nil {
			return err
		}

		lower := strings.ToLower(name)
		if seen[lower] {
			return errHeaderDuplicate
		}

		seen[lower] = true
	}

	return nil
}

// CheckEmail checks msg against the rules, as described in the package
// documentation, with defaultFrom as the sender when msg.From is empty,
// and returns a normalized copy of it that shares nothing with msg. It
// returns the first rule broken, in the order the package documentation
// lists them, and never changes msg. A nil msg is ErrNilMessage.
func CheckEmail(msg *gas.Email, defaultFrom string) (*gas.Email, error) {
	if msg == nil {
		return nil, ErrNilMessage
	}

	out := &gas.Email{
		Subject:  msg.Subject,
		TextBody: msg.TextBody,
		HTMLBody: msg.HTMLBody,
	}

	var err error

	out.From, err = checkFrom(msg.From, defaultFrom)
	if err != nil {
		return nil, err
	}

	if msg.ReplyTo != "" {
		out.ReplyTo, err = field("ReplyTo", msg.ReplyTo)
		if err != nil {
			return nil, err
		}
	}

	err = checkRecipients(out, msg)
	if err != nil {
		return nil, err
	}

	err = checkContent(msg)
	if err != nil {
		return nil, err
	}

	err = checkHeaders(msg.Headers)
	if err != nil {
		return nil, err
	}

	if len(msg.Headers) > 0 {
		out.Headers = maps.Clone(msg.Headers)
	}

	size := MessageSize(out)
	if size > MaxMessageSize {
		return nil, fmt.Errorf(
			"%w: about %d bytes, more than %d", ErrMessageTooLarge, size, MaxMessageSize,
		)
	}

	return out, nil
}

// checkFrom returns the normalized sender: from, or else defaultFrom.
func checkFrom(from, defaultFrom string) (string, error) {
	if from == "" {
		from = defaultFrom
	}

	if from == "" {
		return "", errNoSender
	}

	return field("From", from)
}

// field returns the normalized addr, or an error naming the field it is
// in.
func field(name, addr string) (string, error) {
	out, err := normalize(addr)
	if err != nil {
		return "", fmt.Errorf("%w: %s: %w", ErrInvalidAddress, name, err)
	}

	return out, nil
}

// checkRecipients checks the count of the recipients of msg, then each,
// To, Cc then Bcc, and sets the normalized ones on out.
func checkRecipients(out, msg *gas.Email) error {
	n := len(msg.To) + len(msg.Cc) + len(msg.Bcc)

	switch {
	case n == 0:
		return ErrNoRecipients
	case n > MaxRecipients:
		return fmt.Errorf("%w: %d, at most %d", ErrTooManyRecipients, n, MaxRecipients)
	}

	var err error

	out.To, err = list("To", msg.To)
	if err != nil {
		return err
	}

	out.Cc, err = list("Cc", msg.Cc)
	if err != nil {
		return err
	}

	out.Bcc, err = list("Bcc", msg.Bcc)

	return err
}

// list returns the normalized addresses of the field name, nil for none.
func list(name string, addrs []string) ([]string, error) {
	if len(addrs) == 0 {
		return nil, nil
	}

	out := make([]string, len(addrs))

	for i, a := range addrs {
		n, err := field(fmt.Sprintf("%s[%d]", name, i), a)
		if err != nil {
			return nil, err
		}

		out[i] = n
	}

	return out, nil
}

// checkContent checks the subject, then the bodies.
func checkContent(msg *gas.Email) error {
	switch {
	case msg.Subject == "":
		return errSubjectEmpty
	case !utf8.ValidString(msg.Subject):
		return errSubjectUTF8
	case strings.ContainsFunc(msg.Subject, isControl):
		return errSubjectControl
	case msg.TextBody == "" && msg.HTMLBody == "":
		return ErrEmptyBody
	case !utf8.ValidString(msg.TextBody):
		return fmt.Errorf("%w: TextBody is not valid UTF-8", ErrInvalidBody)
	case !utf8.ValidString(msg.HTMLBody):
		return fmt.Errorf("%w: HTMLBody is not valid UTF-8", ErrInvalidBody)
	default:
		return nil
	}
}

// MessageSize returns the estimated size of msg once SES builds it, in
// bytes, as described in the package documentation: the subject, as an
// RFC 2047 encoded word when it is not ASCII, each body base64-encoded
// in lines of 76 characters, the headers and the addresses, each with
// the length of its header line's name and break.
func MessageSize(msg *gas.Email) int {
	// headerLine is the most a header line adds to its value: a name
	// such as `Subject: ` and the CRLF.
	const headerLine = 32

	size := len(mime.BEncoding.Encode(subjectCharset, msg.Subject)) + headerLine +
		base64Size(len(msg.TextBody)) + base64Size(len(msg.HTMLBody))

	for name, value := range msg.Headers {
		size += len(name) + len(value) + len(": \r\n")
	}

	for _, addrs := range [][]string{{msg.From, msg.ReplyTo}, msg.To, msg.Cc, msg.Bcc} {
		for _, a := range addrs {
			size += len(a) + headerLine
		}
	}

	return size
}

// The shape of base64 in a MIME body.
const (
	// b64In is how many bytes base64 encodes at a time.
	b64In = 3
	// b64Out is how many characters it encodes them into.
	b64Out = 4
	// b64Line is how many characters a line holds, before its CRLF.
	b64Line = 76
)

// base64Size returns the size of n bytes base64-encoded in lines of 76
// characters, each ending with a CRLF.
func base64Size(n int) int {
	encoded := (n + b64In - 1) / b64In * b64Out

	return encoded + (encoded+b64Line-1)/b64Line*len("\r\n")
}
