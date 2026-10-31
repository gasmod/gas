package email_test

import (
	"net/mail"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/email"
)

// NormalizeAddress keeps a display name, quoting an ASCII one and
// encoding one that is not ASCII, drops the angle brackets of an address
// without one, and converts a domain that is not ASCII to Punycode.
func TestNormalizeAddress(t *testing.T) {
	t.Parallel()

	for in, want := range map[string]string{
		"alice@example.com":                   "alice@example.com",
		"<alice@example.com>":                 "alice@example.com",
		"  alice@example.com ":                "alice@example.com",
		"Alice <alice@example.com>":           `"Alice" <alice@example.com>`,
		`"Liddell, Alice" <a@example.com>`:    `"Liddell, Alice" <a@example.com>`,
		"Zoë <zoe@example.com>":               "=?utf-8?q?Zo=C3=AB?= <zoe@example.com>",
		"Zoë, Inc. <zoe@example.com>":         "", // a list: two addresses.
		`"Zoë, Inc." <zoe@example.com>`:       "=?utf-8?b?Wm/DqywgSW5jLg==?= <zoe@example.com>",
		"=?utf-8?q?Zo=C3=AB?= <z@a.com>":      "=?utf-8?q?Zo=C3=AB?= <z@a.com>",
		"a@bücher.example":                    "a@xn--bcher-kva.example",
		"a@BÜCHER.example":                    "a@xn--bcher-kva.example",
		"a@Example.COM":                       "a@example.com",
		`"a b"@example.com`:                   `"a b"@example.com`,
		"Al (comment) <al@example.com>":       `"Al" <al@example.com>`,
		"a@[192.0.2.1]":                       "a@[192.0.2.1]",
		"user+tag@sub.example.co.uk":          "user+tag@sub.example.co.uk",
		"\"\" <alice@example.com>":            "alice@example.com",
		"a@xn--bcher-kva.example":             "a@xn--bcher-kva.example",
		"=?utf-8?q?=00?= <alice@example.com>": "",
	} {
		got, err := email.NormalizeAddress(in)
		if want == "" {
			require.ErrorIs(t, err, email.ErrInvalidAddress, in)

			continue
		}

		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)

		again, err := email.NormalizeAddress(got)
		require.NoError(t, err, got)
		assert.Equal(t, got, again, "normalizing %q again", got)
	}
}

// NormalizeAddress refuses what is not one address with an ASCII local
// part and a valid domain, naming the reason and never the address.
func TestNormalizeAddress_Refused(t *testing.T) {
	t.Parallel()

	for in, reason := range map[string]string{
		"":                                    "empty",
		"zqx":                                 "not one email address",
		"zqx@a.example, b@a.example":          "not one email address",
		"zqx@a.example\r\nBcc: b@a.example":   "not one email address",
		"zqx@a.example\n":                     "not one email address",
		"Zqx <zqx@a.example":                  "not one email address",
		"zqx@exa mple.com":                    "not one email address",
		"bjørn-zqx@example.com":               "the local part is not ASCII",
		"zqx@bü_x.example":                    "the domain is not a valid domain name",
		"zqx@-bü.example":                     "the domain is not a valid domain name",
		"=?utf-8?q?zqx=0D=0A?= <z@a.example>": "the display name holds a control character",
		"=?utf-8?q?zqx=7F?= <z@a.example>":    "the display name holds a control character",
	} {
		_, err := email.NormalizeAddress(in)
		require.ErrorIs(t, err, email.ErrInvalidAddress, in)
		require.EqualError(t, err, "invalid address: "+reason, in)
	}
}

// IsReservedHeader reports the headers the module or SES sets, in any
// case, and any name starting with Content-.
func TestIsReservedHeader(t *testing.T) {
	t.Parallel()

	for _, name := range []string{
		"From", "to", "CC", "Bcc", "reply-to", "Subject", "Date", "Message-ID",
		"MIME-Version", "Return-Path", "Sender", "Content-Type", "content-transfer-encoding",
		"Content-", "Resent-To", "X-SES-Tags", "Errors-To", "Bounces-To", "Return-Receipt-To",
		"Disposition-Notification-To",
	} {
		assert.True(t, email.IsReservedHeader(name), name)
	}

	for _, name := range []string{"X-From", "List-Unsubscribe", "Contents", "Reply", "Tox"} {
		assert.False(t, email.IsReservedHeader(name), name)
	}
}

// CheckHeader keeps SES's limits at their bounds.
func TestCheckHeader_Bounds(t *testing.T) {
	t.Parallel()

	require.NoError(t, email.CheckHeader(strings.Repeat("X", 126), strings.Repeat("v", 870)))
	require.NoError(t, email.CheckHeader("X", strings.Repeat("v", 995)))
	require.NoError(t, email.CheckHeader("!#$%&'*+-.^_`|~9;<=>?@", " ~"))
	require.ErrorIs(t, email.CheckHeader("XX", strings.Repeat("v", 995)), email.ErrInvalidHeader)
	require.ErrorIs(t, email.CheckHeader("X\x7f", "v"), email.ErrInvalidHeader)
	require.ErrorIs(t, email.CheckHeader("X", "v\x7f"), email.ErrInvalidHeader)
	require.ErrorIs(t, email.CheckHeader("X", "\tv"), email.ErrInvalidHeader)
}

// CheckEmail returns a normalized copy that shares nothing with the
// message, with empty lists and headers as nil, and never changes the
// message; a nil message is ErrNilMessage.
func TestCheckEmail_Copy(t *testing.T) {
	t.Parallel()

	_, err := email.CheckEmail(nil, "")
	require.ErrorIs(t, err, email.ErrNilMessage)
	require.EqualError(t, err, "message is nil")

	m := &gas.Email{
		To:       []string{"A <a@bücher.example>"},
		Cc:       []string{},
		Subject:  "Hi",
		HTMLBody: "<p>hi</p>",
		Headers:  map[string]string{},
	}

	out, err := email.CheckEmail(m, "shop@example.com")
	require.NoError(t, err)
	assert.Equal(t, &gas.Email{
		From:     "shop@example.com",
		To:       []string{`"A" <a@xn--bcher-kva.example>`},
		Subject:  "Hi",
		HTMLBody: "<p>hi</p>",
	}, out)
	assert.Equal(t, []string{"A <a@bücher.example>"}, m.To)
	assert.Empty(t, m.From)

	m.Headers["X-A"] = "1"

	out, err = email.CheckEmail(m, "shop@example.com")
	require.NoError(t, err)

	m.Headers["X-A"] = "2"
	assert.Equal(t, "1", out.Headers["X-A"])
}

// The rules run in the order the package documentation lists: the
// sender, the reply-to address, the recipient count, the recipients, the
// subject, the bodies, the headers, the size.
func TestCheckEmail_Order(t *testing.T) {
	t.Parallel()

	m := &gas.Email{
		From:    "bad",
		ReplyTo: "bad",
		To:      []string{"bad"},
		Headers: map[string]string{"From": "x"},
	}

	steps := []struct {
		fix  func()
		want error
	}{
		{func() { m.From = "a@example.com" }, email.ErrInvalidAddress},
		{func() { m.ReplyTo = "" }, email.ErrInvalidAddress},
		{func() { m.To = []string{"b@example.com"} }, email.ErrInvalidSubject},
		{func() { m.Subject = "Hi" }, email.ErrEmptyBody},
		{func() { m.TextBody = strings.Repeat("a", 31<<20) }, email.ErrInvalidHeader},
		{func() { m.Headers = nil }, email.ErrMessageTooLarge},
		{func() { m.TextBody = "a" }, nil},
	}

	_, err := email.CheckEmail(m, "")
	require.EqualError(t, err, "invalid address: From: not one email address")

	for _, s := range steps {
		s.fix()

		_, err := email.CheckEmail(m, "")
		if s.want == nil {
			require.NoError(t, err)

			continue
		}

		require.ErrorIs(t, err, s.want)
	}
}

// MessageSize counts the subject, each body base64-encoded in lines of
// 76 characters with their CRLF, the headers and the addresses.
func TestMessageSize(t *testing.T) {
	t.Parallel()

	base := email.MessageSize(&gas.Email{})

	assert.Equal(t, 3*32, base, "the Subject, From and ReplyTo header lines")
	assert.Equal(t, base+4+2, email.MessageSize(&gas.Email{TextBody: "abc"}))
	assert.Equal(t, base+4+2, email.MessageSize(&gas.Email{HTMLBody: "a"}))
	assert.Equal(t, base+76+2+4+2, email.MessageSize(&gas.Email{TextBody: strings.Repeat("a", 58)}))
	assert.Equal(
		t,
		base+len("X: v\r\n"),
		email.MessageSize(&gas.Email{Headers: map[string]string{"X": "v"}}),
	)
	assert.Equal(t, base+3+3+32, email.MessageSize(&gas.Email{Subject: "abc", To: []string{"a@b"}}))
}

// errShape is the shape every error of the rules has: a sentinel's text,
// then a field and a reason from fixed text, or counts, or a reserved
// header's usual name, so that no error can quote an input.
var errShape = regexp.MustCompile(`^(invalid address|invalid header|invalid subject|invalid body|` +
	`no recipients|too many recipients|empty body|message too large)` +
	`(: (From|ReplyTo|(To|Cc|Bcc)\[\d+\]))?` +
	`(: [a-zA-Z0-9 ,.:'-]+|: "[A-Za-z-]+" is set by the module or SES)?$`)

// FuzzNormalizeAddress checks that NormalizeAddress never panics, is
// deterministic, returns ASCII without CR or LF, accepts what it returns
// and returns it unchanged, returns what net/mail reads as the same
// address, and fails only with an error of the documented shape.
func FuzzNormalizeAddress(f *testing.F) {
	for _, seed := range []string{
		"alice@example.com", "Alice <alice@example.com>", "Zoë <zoe@bücher.example>",
		`"a b"@example.com`, "a@[192.0.2.1]", "a@b, c@d", "a@b\r\nBcc: c@d", "=?utf-8?q?a=0D?= <a@b>",
		"bjørn@example.com", "", "<>", "@", `"\"" <a@b>`, "a@xn--bcher-kva.example",
		"a@\u00ad", "a@x\u3002", "user@\uff45vil.com",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, in string) {
		out, err := email.NormalizeAddress(in)
		out2, err2 := email.NormalizeAddress(in)

		require.Equal(t, out, out2)
		require.Equal(t, err == nil, err2 == nil)

		if err != nil {
			require.ErrorIs(t, err, email.ErrInvalidAddress)
			require.Regexp(t, errShape, err.Error())

			return
		}

		for i := range len(out) {
			require.Less(t, out[i], byte(0x80), "not ASCII: %q", out)
		}

		require.NotContains(t, out, "\r")
		require.NotContains(t, out, "\n")

		again, err := email.NormalizeAddress(out)
		require.NoError(t, err, "normalized %q to %q, which it refuses", in, out)
		require.Equal(t, out, again)

		a, err := mail.ParseAddress(out)
		require.NoError(t, err)

		b, err := mail.ParseAddress(again)
		require.NoError(t, err)
		require.Equal(t, a, b)
	})
}

// FuzzCheckHeader checks that CheckHeader never panics, accepts only
// what SES's patterns accept, and fails only with an error of the
// documented shape, which quotes no input.
func FuzzCheckHeader(f *testing.F) {
	for _, seed := range [][2]string{
		{"X-A", "v"},
		{"From", "x"},
		{"content-type", "x"},
		{"X:A", "v"},
		{"X", "a\r\nb"},
		{"X", ""},
		{"", "v"},
		{"X", "ü"},
		{strings.Repeat("X", 127), "v"},
	} {
		f.Add(seed[0], seed[1])
	}

	name := regexp.MustCompile(`^[!-9;-~]{1,126}$`)
	value := regexp.MustCompile(`^[ -~]{1,995}$`)

	f.Fuzz(func(t *testing.T, n, v string) {
		err := email.CheckHeader(n, v)
		if err != nil {
			require.ErrorIs(t, err, email.ErrInvalidHeader)
			require.Regexp(t, errShape, err.Error())

			return
		}

		require.Regexp(t, name, n)
		require.Regexp(t, value, v)
		require.LessOrEqual(t, len(n)+len(v), email.MaxHeaderLength)
		require.False(t, email.IsReservedHeader(n), n)
	})
}

// Every error CheckEmail returns has the documented shape, whatever the
// message.
func FuzzCheckEmail(f *testing.F) {
	f.Add("a@b", "c@d", "Hi", "text", "X-A", "v")
	f.Add("", "x, y", "a\r\n", "", "From", "\n")

	f.Fuzz(func(t *testing.T, from, to, subject, body, hname, hvalue string) {
		m := &gas.Email{
			From:     from,
			To:       []string{to},
			Subject:  subject,
			TextBody: body,
			Headers:  map[string]string{hname: hvalue},
		}

		out, err := email.CheckEmail(m, "")
		if err != nil {
			require.Regexp(t, errShape, err.Error())

			return
		}

		require.NotContains(t, out.Subject, "\n")
		require.Equal(t, []string{to}, m.To, "changed %q", to)
	})
}
