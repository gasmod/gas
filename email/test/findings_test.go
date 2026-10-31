package email_test

import (
	"mime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/email"
)

// IDNA's lookup mapping dropped ignorable code points, folded fullwidth
// letters and turned U+3002 into a dot, so `a@\u00ad` became `a@`, which
// net/mail then refused, and `user@\uff45vil.com` was sent to evil.com. A
// domain now must convert back to itself, but for case and
// normalization form, and the rebuilt address must read again.
func TestFinding_IDNARoundTrip(t *testing.T) {
	t.Parallel()

	for _, in := range []string{
		"a@\u00ad",
		"a@x\u3002",
		"user@\uff45vil.com",
		"user@\uff45\uff56\uff49\uff4c.com",
		"user@evil\u3002com",
		"user@ev\u00adil.com",
		"user@ev\u200bil.com",
		"user@ev\u200dil.com",
		"a@\u034f",
		"a@\u180b",
		"Bob <a@\u00ad>",
	} {
		_, err := email.NormalizeAddress(in)
		require.EqualError(t, err, "invalid address: the domain is not a valid domain name", in)
	}

	for in, want := range map[string]string{
		"a@BÜCHER.example":           "a@xn--bcher-kva.example",
		"a@bu\u0308cher.example":     "a@xn--bcher-kva.example",
		"a@sub.éxample.com":          "a@sub.xn--xample-9ua.com",
		"user@\u0430pple.com":        "user@xn--pple-43d.com",
		"Zoë <zoe@bücher.example>":   "=?utf-8?q?Zo=C3=AB?= <zoe@xn--bcher-kva.example>",
		"a@xn--bcher-kva.example":    "a@xn--bcher-kva.example",
		"a@Example.COM":              "a@example.com",
		"Al <al@sub.bücher.example>": `"Al" <al@sub.xn--bcher-kva.example>`,
	} {
		got, err := email.NormalizeAddress(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}

	_, err := email.NormalizeAddress("a@éxample.com.")
	require.EqualError(t, err, "invalid address: not one email address")
}

// An ASCII domain was sent as given, so `a@EVIL.com` and `a@evil.com`, or
// an upper-case Punycode domain and its Unicode form, normalized to
// different texts, and an invalid Punycode label passed. ASCII domains
// are now lower-cased, and a Punycode label must decode and encode back
// to itself.
func TestFinding_ASCIIDomainCanonical(t *testing.T) {
	t.Parallel()

	for in, want := range map[string]string{
		"a@EVIL.com":              "a@evil.com",
		"a@XN--MNCHEN-3YA.DE":     "a@xn--mnchen-3ya.de",
		"a@MÜNCHEN.DE":            "a@xn--mnchen-3ya.de",
		"A@Sub.XN--Bcher-Kva.Com": "A@sub.xn--bcher-kva.com",
		"a@[192.0.2.1]":           "a@[192.0.2.1]",
		"a@[IPv6:::1]":            "a@[IPv6:::1]",
	} {
		got, err := email.NormalizeAddress(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)

		again, err := email.NormalizeAddress(got)
		require.NoError(t, err, got)
		assert.Equal(t, got, again, got)
	}

	for _, in := range []string{"a@xn--zz.com", "a@xn--.com", "a@sub.xn--.com", "a@İstanbul.tr"} {
		_, err := email.NormalizeAddress(in)
		require.ErrorIs(t, err, email.ErrInvalidAddress, in)
	}
}

// A display name an encoded word decoded to invalid UTF-8 was kept. It
// is now refused, and so is one holding a C1 control or U+2028; a tab is
// allowed, as in a subject.
func TestFinding_DisplayNameRules(t *testing.T) {
	t.Parallel()

	for in, reason := range map[string]string{
		"=?utf-8?q?=FF?= <a@example.com>":             "the display name is not valid UTF-8",
		"=?utf-8?q?a=E2=80=A8b?= <a@example.com>":     "the display name holds a control character",
		"=?utf-8?q?a=C2=85b?= <a@example.com>":        "the display name holds a control character",
		"a\u2028b <a@example.com>":                    "the display name holds a control character",
		"a\u0085b <a@example.com>":                    "the display name holds a control character",
		"=?utf-8?q?Al=0Dice?= <a@example.com>":        "the display name holds a control character",
		"=?utf-8?q?a=0D=0ABcc:_x=40y?= <a@b.example>": "not one email address",
	} {
		_, err := email.NormalizeAddress(in)
		require.EqualError(t, err, "invalid address: "+reason, in)
	}

	got, err := email.NormalizeAddress("=?utf-8?q?a=09b?= <a@example.com>")
	require.NoError(t, err)
	assert.Equal(t, "\"a\tb\" <a@example.com>", got)

	got, err = email.NormalizeAddress("=?iso-8859-1?q?M=FCller?= <a@example.com>")
	require.NoError(t, err)
	assert.Equal(t, "=?utf-8?q?M=C3=BCller?= <a@example.com>", got)
}

// A local part could hold an `@`, quoted, or an encoded word, which a
// receiver could read as another address. Both are refused; a comment
// still becomes the display name, as net/mail reads it.
func TestFinding_LocalPartTricks(t *testing.T) {
	t.Parallel()

	for _, in := range []string{
		`"a@b.example"@evil.example`,
		`"x@evil.example>"@victim.example`,
		"=?utf-8?q?x=40evil.com=3e=00?=@victim.example",
		"=?x?q?collab=40evil.com=3e=20?=foo@victim.example",
	} {
		_, err := email.NormalizeAddress(in)
		require.EqualError(
			t, err, "invalid address: the local part holds an at sign or an encoded word", in,
		)
	}

	for _, in := range []string{"a@[a@b]", "a@[=?x?=]"} {
		_, err := email.NormalizeAddress(in)
		require.EqualError(t, err, "invalid address: not one email address", in)
	}

	for in, want := range map[string]string{
		"a@example.com (comment)":   `"comment" <a@example.com>`,
		"a@b.example (c@d.example)": `"c@d.example" <a@b.example>`,
		"x%evil.com@victim.example": "x%evil.com@victim.example",
		"a@[IPv6:::1]":              "a@[IPv6:::1]",
	} {
		got, err := email.NormalizeAddress(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}
}

// A subject could hold a C0 control, DEL, a C1 control or U+2028, which
// some readers take as a line break. They are now refused; a tab is
// kept.
func TestFinding_SubjectControls(t *testing.T) {
	t.Parallel()

	for _, subject := range []string{
		"a\rb", "a\nb", "a\x00b", "a\vb", "a\x1bb", "a\x7fb", "a\u0085b", "a\u009fb",
		"a\u2028b", "a\u2029b",
	} {
		_, err := email.CheckEmail(&gas.Email{
			From: "a@example.com", To: []string{"b@example.com"}, Subject: subject, TextBody: "t",
		}, "")
		require.ErrorIs(t, err, email.ErrInvalidSubject, subject)
		require.EqualError(t, err, "invalid subject: holds a control character", subject)
	}

	out, err := email.CheckEmail(&gas.Email{
		From: "a@example.com", To: []string{"b@example.com"}, Subject: "a\tb ü", TextBody: "t",
	}, "")
	require.NoError(t, err)
	assert.Equal(t, "a\tb ü", out.Subject)
}

// The resent headers, SES's X-SES- headers and the headers that redirect
// receipts or bounces were accepted, though the package documentation
// said no header could override what the module or SES sets. They are
// now reserved, in any case.
func TestFinding_ReservedHeaders(t *testing.T) {
	t.Parallel()

	for name, want := range map[string]string{
		"Resent-From":                 "a name starting with Resent- is set by the module or SES",
		"RESENT-bcc":                  "a name starting with Resent- is set by the module or SES",
		"X-SES-CONFIGURATION-SET":     "a name starting with X-SES- is set by the module or SES",
		"x-ses-message-tags":          "a name starting with X-SES- is set by the module or SES",
		"disposition-notification-to": `"Disposition-Notification-To" is set by the module or SES`,
		"Return-Receipt-To":           `"Return-Receipt-To" is set by the module or SES`,
		"ERRORS-TO":                   `"Errors-To" is set by the module or SES`,
		"Bounces-To":                  `"Bounces-To" is set by the module or SES`,
	} {
		assert.True(t, email.IsReservedHeader(name), name)
		require.EqualError(t, email.CheckHeader(name, "v"), "invalid header: "+want, name)
	}

	for _, name := range []string{"Resent", "X-SES", "X-Sesame", "Errors", "Bounces-To-X"} {
		assert.False(t, email.IsReservedHeader(name), name)
		require.NoError(t, email.CheckHeader(name, "v"), name)
	}
}

// MaxMessageSize was 40 MiB, above SES's 40 MB. It is now 40,000,000
// bytes.
func TestFinding_MaxMessageSizeDecimal(t *testing.T) {
	t.Parallel()

	assert.Equal(t, 40_000_000, email.MaxMessageSize)

	m := &gas.Email{From: "a@example.com", To: []string{"b@example.com"}, Subject: "s"}

	// 29,000,000 bytes base64-encode to about 39.2 MB, under the limit;
	// 29,700,000 to about 40.1 MB, over it.
	m.TextBody = strings.Repeat("a", 29_000_000)

	_, err := email.CheckEmail(m, "")
	require.NoError(t, err)

	m.TextBody = strings.Repeat("a", 29_700_000)

	_, err = email.CheckEmail(m, "")
	require.ErrorIs(t, err, email.ErrMessageTooLarge)
}

// MessageSize counted a subject that is not ASCII by its UTF-8 bytes,
// while SES's sender sends it as encoded words. It now counts the
// encoded words.
func TestFinding_SizeCountsEncodedSubject(t *testing.T) {
	t.Parallel()

	base := email.MessageSize(&gas.Email{})
	subject := strings.Repeat("ü", 100)

	encoded := mime.BEncoding.Encode("UTF-8", subject)
	require.NotEqual(t, subject, encoded)

	assert.Equal(t, base+len(encoded), email.MessageSize(&gas.Email{Subject: subject}))
	assert.Equal(t, base+5, email.MessageSize(&gas.Email{Subject: "a\tb c"}))
}
