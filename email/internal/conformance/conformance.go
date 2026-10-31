//nolint:mnd,goconst // test rules: counts and texts read best where they are checked.
package conformance

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/email"
)

// Sender is an email sender with a lifecycle and readiness.
type Sender interface {
	gas.Service
	gas.EmailProvider
	gas.ReadyReporter
}

// Subject is a sender under test.
type Subject struct {
	// New returns a new sender, not initialized, that fetches templates
	// from templates and whose default sender is the address in from,
	// none when it is empty. The suite closes it.
	New func(t *testing.T, templates gas.TemplateProvider, from string) Sender
	// Sent returns the messages s sent, in order, normalized.
	Sent func(t *testing.T, s Sender) []gas.Email
	// Name is the sender's ServiceName, which starts its errors.
	Name string
}

// The times the rules wait on.
const (
	// pause is how long a rule lets a call it started begin.
	pause = 100 * time.Millisecond
	// prompt is how long a call that should return at once may take.
	prompt = 5 * time.Second
)

// The operations errors name.
const (
	opSend         = "send"
	opSendTemplate = "send from template"
)

// The values the rules send, distinctive so that an error that quotes one
// is found.
const (
	// shop is the default sender.
	shop = "Shop <shop@example.com>"
	// alice is a recipient.
	alice = "alice@example.com"
	// subject is a subject.
	subject = "Order zqx-77 shipped"
	// text is a text body.
	text = "Your order zqx-77 ships today."
)

// errMissing is what Templates returns for a template it does not have.
var errMissing = errors.New("template not found")

// Run runs the suite on s.
func Run(t *testing.T, s Subject) {
	t.Helper()

	rules := map[string]func(*testing.T, Subject){
		"RoundTrip":          roundTrip,
		"DefaultSender":      defaultSender,
		"NoSender":           noSender,
		"Addresses":          addresses,
		"Recipients":         recipients,
		"Subject":            subjectRule,
		"Bodies":             bodies,
		"Headers":            headers,
		"Size":               size,
		"CallerUnchanged":    callerUnchanged,
		"Template":           template,
		"TemplateEmptyNames": templateEmptyNames,
		"TemplateFails":      templateFails,
		"TemplateSubject":    templateSubject,
		"TemplateTooLarge":   templateTooLarge,
		"NilMessage":         nilMessage,
		"Lifecycle":          lifecycle,
		"CanceledContext":    canceledContext,
		"CancelDuringRender": cancelDuringRender,
		"CloseWaits":         closeWaits,
		"CloseCancels":       closeCancels,
		"RestartRace":        restartRace,
		"Concurrent":         concurrent,
		"ErrorsHoldNoData":   errorsHoldNoData,
	}

	for name, rule := range rules {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rule(t, s)
		})
	}
}

// Templates is a gas.TemplateProvider of named texts for the rules. Get
// blocks while the template is held, until it is released or its
// context ends. It is safe for concurrent use.
type Templates struct {
	// texts are the templates by name.
	texts map[string]string
	// held are the names Get blocks on, each with the channel closed to
	// release it.
	held map[string]chan struct{}
	// entered receives a value each time Get starts blocking.
	entered chan struct{}
	// mu guards texts and held.
	mu sync.Mutex
}

var _ gas.TemplateProvider = (*Templates)(nil)

// NewTemplates returns Templates holding texts.
func NewTemplates(texts map[string]string) *Templates {
	return &Templates{
		texts:   texts,
		held:    map[string]chan struct{}{},
		entered: make(chan struct{}, 64),
	}
}

// Get returns the template name, blocking first while it is held.
func (p *Templates) Get(ctx context.Context, name string) ([]byte, error) {
	p.mu.Lock()

	release, ok := p.held[name]
	p.mu.Unlock()

	if ok {
		p.entered <- struct{}{}

		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err() //nolint:wrapcheck // a provider's error, which the senders wrap.
		}
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	v, ok := p.texts[name]
	if !ok {
		return nil, errMissing
	}

	return []byte(v), nil
}

// List returns nothing.
func (*Templates) List(context.Context) ([]string, error) { return nil, nil }

// Register adds the template name.
func (p *Templates) Register(_ context.Context, name string, content []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.texts[name] = string(content)

	return nil
}

// RegisterFS adds nothing.
func (*Templates) RegisterFS(context.Context, fs.FS) error { return nil }

// contextIgnoring is a gas.TemplateProvider whose Get ignores its
// context, as one that reads from memory may, so that a held template
// is released only by its release func.
type contextIgnoring struct {
	*Templates
}

// Get returns the template name, ignoring ctx.
func (p contextIgnoring) Get(ctx context.Context, name string) ([]byte, error) {
	return p.Templates.Get(context.WithoutCancel(ctx), name)
}

// hold makes Get of name block until the returned func is called.
func (p *Templates) hold(name string) func() {
	ch := make(chan struct{})

	p.mu.Lock()

	p.held[name] = ch
	p.mu.Unlock()

	var once sync.Once

	return func() { once.Do(func() { close(ch) }) }
}

// open returns a new initialized sender of s with templates and from,
// closed when the test ends.
func open(t *testing.T, s Subject, templates gas.TemplateProvider, from string) Sender {
	t.Helper()

	st := build(t, s, templates, from)
	require.NoError(t, st.Init(t.Context()))

	return st
}

// build returns a new sender of s, not initialized, closed when the test
// ends.
func build(t *testing.T, s Subject, templates gas.TemplateProvider, from string) Sender {
	t.Helper()

	if templates == nil {
		templates = NewTemplates(map[string]string{})
	}

	st := s.New(t, templates, from)
	t.Cleanup(func() { require.NoError(t, st.Close(context.Background())) })

	return st
}

// message returns a valid message to alice from the default sender.
func message() *gas.Email {
	return &gas.Email{To: []string{alice}, Subject: subject, TextBody: text}
}

// errText returns the text of op's error err of s.
func errText(s Subject, op, err string) string {
	return s.Name + ": " + op + ": " + err
}

// The messages a sender sends keep every field, normalized: addresses in
// the form email.NormalizeAddress writes, display names encoded, IDN
// domains in Punycode, the headers as given.
func roundTrip(t *testing.T, s Subject) {
	t.Helper()

	st := open(t, s, nil, shop)

	msg := &gas.Email{
		From:     "Bücher Shop <shop@bücher.example>",
		ReplyTo:  "<help@example.com>",
		To:       []string{"Alice Liddell <alice@example.com>", "bob@example.com"},
		Cc:       []string{"carol@example.com"},
		Bcc:      []string{"dave@example.com"},
		Subject:  "Grüße, " + subject,
		TextBody: text,
		HTMLBody: "<p>" + text + "</p>",
		Headers: map[string]string{
			"X-Order":          "zqx-77",
			"List-Unsubscribe": "<https://example.com/u>",
		},
	}

	require.NoError(t, st.Send(t.Context(), msg))

	assert.Equal(t, []gas.Email{
		{
			From:     "=?utf-8?q?B=C3=BCcher_Shop?= <shop@xn--bcher-kva.example>",
			ReplyTo:  "help@example.com",
			To:       []string{`"Alice Liddell" <alice@example.com>`, "bob@example.com"},
			Cc:       []string{"carol@example.com"},
			Bcc:      []string{"dave@example.com"},
			Subject:  "Grüße, " + subject,
			TextBody: text,
			HTMLBody: "<p>" + text + "</p>",
			Headers: map[string]string{
				"X-Order":          "zqx-77",
				"List-Unsubscribe": "<https://example.com/u>",
			},
		},
	}, s.Sent(t, st))
}

// A message without a From is sent from the default sender, normalized;
// one with a From is sent from it.
func defaultSender(t *testing.T, s Subject) {
	t.Helper()

	st := open(t, s, nil, "  Shop <shop@example.com> ")

	require.NoError(t, st.Send(t.Context(), message()))

	other := message()

	other.From = "news@example.com"

	require.NoError(t, st.Send(t.Context(), other))

	sent := s.Sent(t, st)
	require.Len(t, sent, 2)
	assert.Equal(t, `"Shop" <shop@example.com>`, sent[0].From)
	assert.Equal(t, "news@example.com", sent[1].From)
}

// A message without a From, on a sender without a default sender, is
// ErrInvalidAddress, and nothing is sent.
func noSender(t *testing.T, s Subject) {
	t.Helper()

	st := open(t, s, nil, "")

	err := st.Send(t.Context(), message())
	require.ErrorIs(t, err, email.ErrInvalidAddress)
	require.EqualError(
		t,
		err,
		errText(s, opSend, "invalid address: From: no sender, and no default sender"),
	)
	assert.Empty(t, s.Sent(t, st))
}

// Each address that is not one ASCII-local email address is
// ErrInvalidAddress, naming the field and the reason, and nothing is
// sent. The sender, the reply-to address, then To, Cc and Bcc are
// checked in that order.
func addresses(t *testing.T, s Subject) {
	t.Helper()

	st := open(t, s, nil, shop)

	cases := []struct {
		edit func(*gas.Email)
		want string
	}{
		{func(m *gas.Email) { m.From = "shop@exa mple.com" }, "From: not one email address"},
		{
			func(m *gas.Email) { m.ReplyTo = "a@x.example, b@x.example" },
			"ReplyTo: not one email address",
		},
		{func(m *gas.Email) { m.To = []string{""} }, "To[0]: empty"},
		{
			func(m *gas.Email) { m.To = []string{alice, "evil@example.com\r\nBcc: x@example.com"} },
			"To[1]: not one email address",
		},
		{
			func(m *gas.Email) { m.Cc = []string{"bjørn@example.com"} },
			"Cc[0]: the local part is not ASCII",
		},
		{
			func(m *gas.Email) { m.Bcc = []string{"a@bü_x.example"} },
			"Bcc[0]: the domain is not a valid domain name",
		},
		{
			func(m *gas.Email) { m.To = []string{"=?utf-8?q?a=0D=0Ab?= <a@example.com>"} },
			"To[0]: the display name holds a control character",
		},
		{func(m *gas.Email) { m.From, m.To = "bad", []string{""} }, "From: not one email address"},
	}

	for _, c := range cases {
		m := message()
		c.edit(m)

		err := st.Send(t.Context(), m)
		require.ErrorIs(t, err, email.ErrInvalidAddress, c.want)
		require.EqualError(t, err, errText(s, opSend, "invalid address: "+c.want))
	}

	assert.Empty(t, s.Sent(t, st))
}

// A message needs 1 to 50 recipients across To, Cc and Bcc; the count is
// checked before the addresses.
func recipients(t *testing.T, s Subject) {
	t.Helper()

	st := open(t, s, nil, shop)

	none := message()

	none.To = nil

	err := st.Send(t.Context(), none)
	require.ErrorIs(t, err, email.ErrNoRecipients)
	require.EqualError(t, err, errText(s, opSend, "no recipients"))

	many := message()

	many.To = addrs("to", 20)
	many.Cc = addrs("cc", 20)
	many.Bcc = append(addrs("bcc", 10), "not an address")

	err = st.Send(t.Context(), many)
	require.ErrorIs(t, err, email.ErrTooManyRecipients)
	require.EqualError(t, err, errText(s, opSend, "too many recipients: 51, at most 50"))
	assert.Empty(t, s.Sent(t, st))

	many.Bcc = many.Bcc[:10]

	require.NoError(t, st.Send(t.Context(), many))

	sent := s.Sent(t, st)
	require.Len(t, sent, 1)
	assert.Len(t, sent[0].Bcc, 10)

	bccOnly := message()

	bccOnly.To, bccOnly.Bcc = nil, []string{alice}

	require.NoError(t, st.Send(t.Context(), bccOnly))
}

// addrs returns n addresses with the local part prefix.
func addrs(prefix string, n int) []string {
	out := make([]string, n)
	for i := range n {
		out[i] = fmt.Sprintf("%s%d@example.com", prefix, i)
	}

	return out
}

// A subject is required, valid UTF-8, without a control character: a
// C0 control but HTAB, DEL, a C1 control, U+2028 or U+2029.
func subjectRule(t *testing.T, s Subject) {
	t.Helper()

	st := open(t, s, nil, shop)

	for subj, want := range map[string]string{
		"":                            "invalid subject: empty",
		"a\xffb":                      "invalid subject: not valid UTF-8",
		"Hi\r\nBcc: evil@example.com": "invalid subject: holds a control character",
		"Hi\nthere":                   "invalid subject: holds a control character",
		"Hi\x00":                      "invalid subject: holds a control character",
		"Hi\x1b":                      "invalid subject: holds a control character",
		"Hi\x7f":                      "invalid subject: holds a control character",
		"Hi\u0085there":               "invalid subject: holds a control character",
		"Hi\u2028Bcc: x@example.com":  "invalid subject: holds a control character",
		"Hi\u2029":                    "invalid subject: holds a control character",
	} {
		m := message()

		m.Subject = subj

		err := st.Send(t.Context(), m)
		require.ErrorIs(t, err, email.ErrInvalidSubject)
		require.EqualError(t, err, errText(s, opSend, want))
	}

	assert.Empty(t, s.Sent(t, st))
}

// A message needs a body; an empty one is left out, and a body must be
// valid UTF-8.
func bodies(t *testing.T, s Subject) {
	t.Helper()

	st := open(t, s, nil, shop)

	m := message()

	m.TextBody = ""

	err := st.Send(t.Context(), m)
	require.ErrorIs(t, err, email.ErrEmptyBody)
	require.EqualError(t, err, errText(s, opSend, "empty body"))

	m.HTMLBody = "a\xff"

	err = st.Send(t.Context(), m)
	require.ErrorIs(t, err, email.ErrInvalidBody)
	require.EqualError(t, err, errText(s, opSend, "invalid body: HTMLBody is not valid UTF-8"))

	m.HTMLBody, m.TextBody = "", "\xfe"

	err = st.Send(t.Context(), m)
	require.ErrorIs(t, err, email.ErrInvalidBody)
	require.EqualError(t, err, errText(s, opSend, "invalid body: TextBody is not valid UTF-8"))

	htmlOnly := message()

	htmlOnly.TextBody, htmlOnly.HTMLBody = "", "<p>hi</p>"

	require.NoError(t, st.Send(t.Context(), message()))
	require.NoError(t, st.Send(t.Context(), htmlOnly))

	sent := s.Sent(t, st)
	require.Len(t, sent, 2)
	assert.Equal(t, text, sent[0].TextBody)
	assert.Empty(t, sent[0].HTMLBody)
	assert.Empty(t, sent[1].TextBody)
	assert.Equal(t, "<p>hi</p>", sent[1].HTMLBody)
}

// The header rules are SES's, with the names the module or SES sets
// refused in any case.
//
//nolint:funlen // one case per header rule.
func headers(t *testing.T, s Subject) {
	t.Helper()

	st := open(t, s, nil, shop)

	many := map[string]string{}
	for i := range 16 {
		many[fmt.Sprintf("X-H%d", i)] = "v"
	}

	cases := []struct {
		headers map[string]string
		want    string
	}{
		{many, "16 headers, at most 15"},
		{map[string]string{"": "v"}, "a name is empty or longer than 126 bytes"},
		{
			map[string]string{strings.Repeat("X", 127): "v"},
			"a name is empty or longer than 126 bytes",
		},
		{
			map[string]string{"X Order": "v"},
			"a name holds a character other than printable ASCII but ':'",
		},
		{
			map[string]string{"X:Order": "v"},
			"a name holds a character other than printable ASCII but ':'",
		},
		{map[string]string{"bcc": "evil@example.com"}, `"Bcc" is set by the module or SES`},
		{
			map[string]string{"REPLY-TO": "evil@example.com"},
			`"Reply-To" is set by the module or SES`,
		},
		{
			map[string]string{"content-type": "text/html"},
			"a name starting with Content- is set by the module or SES",
		},
		{
			map[string]string{"Resent-Bcc": "evil@example.com"},
			"a name starting with Resent- is set by the module or SES",
		},
		{
			map[string]string{"X-SES-CONFIGURATION-SET": "zqx"},
			"a name starting with X-SES- is set by the module or SES",
		},
		{
			map[string]string{"errors-to": "evil@example.com"},
			`"Errors-To" is set by the module or SES`,
		},
		{map[string]string{"X-Order": ""}, "a value is empty or longer than 995 bytes"},
		{
			map[string]string{"X-Order": strings.Repeat("v", 996)},
			"a value is empty or longer than 995 bytes",
		},
		{
			map[string]string{"X-Order": "zqx\r\nBcc: evil@example.com"},
			"a value holds a character other than printable ASCII",
		},
		{
			map[string]string{"X-Order": "grüße"},
			"a value holds a character other than printable ASCII",
		},
		{
			map[string]string{strings.Repeat("X", 10): strings.Repeat("v", 990)},
			"a name and its value are longer than 996 bytes together",
		},
		{map[string]string{"X-Order": "1", "x-order": "2"}, "two names differ only in case"},
	}

	for _, c := range cases {
		m := message()

		m.Headers = c.headers

		err := st.Send(t.Context(), m)
		require.ErrorIs(t, err, email.ErrInvalidHeader, c.want)
		require.EqualError(t, err, errText(s, opSend, "invalid header: "+c.want))
	}

	assert.Empty(t, s.Sent(t, st))

	ok := message()

	ok.Headers = map[string]string{
		strings.Repeat("X", 126): strings.Repeat("v", 870),
		"X-Tag":                  " spaced value ~!",
	}

	for i := range 13 {
		ok.Headers[fmt.Sprintf("X-H%d", i)] = "v"
	}

	require.NoError(t, st.Send(t.Context(), ok))

	sent := s.Sent(t, st)
	require.Len(t, sent, 1)
	assert.Equal(t, ok.Headers, sent[0].Headers)
}

// A message whose estimated size is above 40,000,000 bytes is
// ErrMessageTooLarge,
// and is not sent.
func size(t *testing.T, s Subject) {
	t.Helper()

	st := open(t, s, nil, shop)

	m := message()

	m.TextBody = strings.Repeat("a", 31<<20)

	err := st.Send(t.Context(), m)
	require.ErrorIs(t, err, email.ErrMessageTooLarge)
	assert.True(
		t,
		strings.HasPrefix(err.Error(), errText(s, opSend, "message too large: about ")),
		err,
	)
	assert.True(t, strings.HasSuffix(err.Error(), "bytes, more than 40000000"), err)
	assert.Empty(t, s.Sent(t, st))
}

// Send and SendFromTemplate never change the caller's message, and a
// caller that changes it after the call does not change what was sent.
func callerUnchanged(t *testing.T, s Subject) {
	t.Helper()

	st := open(t, s, NewTemplates(map[string]string{"s": "Hi {{.}}"}), shop)

	m := &gas.Email{
		To:       []string{"Alice <alice@bücher.example>"},
		Subject:  subject,
		TextBody: text,
		Headers:  map[string]string{"X-Order": "1"},
	}
	before := *m

	before.To = append([]string(nil), m.To...)

	require.NoError(t, st.Send(t.Context(), m))
	assert.Equal(t, before.To, m.To)
	assert.Equal(t, map[string]string{"X-Order": "1"}, m.Headers)
	assert.Empty(t, m.From)

	m.To[0] = "changed@example.com"
	m.Headers["X-Order"] = "2"

	tm := &gas.TemplatedEmail{SubjectTemplate: "s", Data: "Bob", Email: *message()}

	require.NoError(t, st.SendFromTemplate(t.Context(), tm))
	assert.Equal(t, subject, tm.Subject)
	assert.Empty(t, tm.From)

	sent := s.Sent(t, st)
	require.Len(t, sent, 2)
	assert.Equal(t, []string{`"Alice" <alice@xn--bcher-kva.example>`}, sent[0].To)
	assert.Equal(t, "1", sent[0].Headers["X-Order"])
	assert.Equal(t, "Hi Bob", sent[1].Subject)
}

// SendFromTemplate renders the subject and the text with text/template
// and the HTML with html/template, which escapes the data.
func template(t *testing.T, s Subject) {
	t.Helper()

	st := open(t, s, NewTemplates(map[string]string{
		"welcome-subject": "Welcome, {{.Name}}",
		"welcome.txt":     "Hello {{.Name}} & co",
		"welcome.html":    "<p>Hello {{.Name}}</p>",
	}), shop)

	err := st.SendFromTemplate(t.Context(), &gas.TemplatedEmail{
		SubjectTemplate: "welcome-subject",
		TextTemplate:    "welcome.txt",
		HTMLTemplate:    "welcome.html",
		Data:            map[string]string{"Name": "<b>Al</b>"},
		To:              []string{alice},
	})
	require.NoError(t, err)

	sent := s.Sent(t, st)
	require.Len(t, sent, 1)
	assert.Equal(t, "Welcome, <b>Al</b>", sent[0].Subject)
	assert.Equal(t, "Hello <b>Al</b> & co", sent[0].TextBody)
	assert.Equal(t, "<p>Hello &lt;b&gt;Al&lt;/b&gt;</p>", sent[0].HTMLBody)
	assert.Equal(t, shop, strings.ReplaceAll(sent[0].From, `"`, ""))
}

// A template name left empty keeps that field of the embedded Email; with
// all three empty, the message is sent as it is.
func templateEmptyNames(t *testing.T, s Subject) {
	t.Helper()

	st := open(t, s, NewTemplates(map[string]string{"body.html": "<i>{{.}}</i>"}), shop)

	m := &gas.TemplatedEmail{HTMLTemplate: "body.html", Data: "x", Email: *message()}

	require.NoError(t, st.SendFromTemplate(t.Context(), m))
	require.NoError(t, st.SendFromTemplate(t.Context(), &gas.TemplatedEmail{Email: *message()}))

	sent := s.Sent(t, st)
	require.Len(t, sent, 2)
	assert.Equal(t, subject, sent[0].Subject)
	assert.Equal(t, text, sent[0].TextBody)
	assert.Equal(t, "<i>x</i>", sent[0].HTMLBody)
	assert.Equal(t, subject, sent[1].Subject)
	assert.Empty(t, sent[1].HTMLBody)
}

// A template that cannot be fetched, parsed or executed is ErrTemplate,
// naming the field and the template, never the data or the template's
// text; a fetch failure wraps the provider's error. Nothing is sent and
// the caller's message is unchanged, even when a field rendered before
// the failure.
func templateFails(t *testing.T, s Subject) {
	t.Helper()

	st := open(t, s, NewTemplates(map[string]string{
		"subject":   "Hi {{.Name}}",
		"bad-parse": "{{.Name",
		"bad-exec":  "{{index .Cards 5}}",
	}), shop)

	cases := []struct {
		want string
		msg  gas.TemplatedEmail
	}{
		{`subject "missing": fetch failed`, gas.TemplatedEmail{SubjectTemplate: "missing"}},
		{
			`text "bad-parse": parse failed`,
			gas.TemplatedEmail{SubjectTemplate: "subject", TextTemplate: "bad-parse"},
		},
		{
			`HTML "bad-exec": execute failed`,
			gas.TemplatedEmail{SubjectTemplate: "subject", HTMLTemplate: "bad-exec"},
		},
	}

	for _, c := range cases {
		m := c.msg

		m.Email = *message()
		m.Data = map[string]any{"Name": "zqx-secret", "Cards": []string{"4111-zqx"}}

		err := st.SendFromTemplate(t.Context(), &m)
		require.ErrorIs(t, err, email.ErrTemplate)
		require.EqualError(t, err, errText(s, opSendTemplate, "template failed: "+c.want))
		assert.Equal(t, subject, m.Subject)
		assert.Equal(t, text, m.TextBody)
		assert.Empty(t, m.HTMLBody)

		if strings.Contains(c.want, "fetch") {
			require.ErrorIs(t, err, errMissing)
		}
	}

	assert.Empty(t, s.Sent(t, st))
}

// A subject rendered from data holding a line break is refused, as any
// subject with one is; one trailing CRLF or LF of a rendered subject is
// cut first.
func templateSubject(t *testing.T, s Subject) {
	t.Helper()

	st := open(t, s, NewTemplates(map[string]string{
		"s":    "Hi {{.}}",
		"lf":   "Hi {{.}}\n",
		"crlf": "Hi {{.}}\r\n",
		"two":  "Hi {{.}}\n\n",
	}), shop)

	for _, name := range []string{"lf", "crlf"} {
		require.NoError(t, st.SendFromTemplate(t.Context(), &gas.TemplatedEmail{
			SubjectTemplate: name, Data: "Bob", Email: *message(),
		}), name)
	}

	sent := s.Sent(t, st)
	require.Len(t, sent, 2)
	assert.Equal(t, "Hi Bob", sent[0].Subject)
	assert.Equal(t, "Hi Bob", sent[1].Subject)

	err := st.SendFromTemplate(t.Context(), &gas.TemplatedEmail{
		SubjectTemplate: "two", Data: "Bob", Email: *message(),
	})
	require.ErrorIs(t, err, email.ErrInvalidSubject)

	err = st.SendFromTemplate(t.Context(), &gas.TemplatedEmail{
		SubjectTemplate: "s",
		Data:            "Bob\r\nBcc: evil@example.com",
		Email:           *message(),
	})
	require.ErrorIs(t, err, email.ErrInvalidSubject)
	require.EqualError(
		t,
		err,
		errText(s, opSendTemplate, "invalid subject: holds a control character"),
	)
	assert.Len(t, s.Sent(t, st), 2)
}

// A template that renders more than email.MaxMessageSize bytes is
// stopped there and is ErrMessageTooLarge, naming the field and the
// template; nothing is sent.
func templateTooLarge(t *testing.T, s Subject) {
	t.Helper()

	st := open(t, s, NewTemplates(map[string]string{
		"big": "{{range .N}}{{$.S}}{{end}}",
	}), shop)

	data := map[string]any{"N": make([]int, 40), "S": strings.Repeat("z", 1<<20)}

	for field, tm := range map[string]gas.TemplatedEmail{
		"text": {TextTemplate: "big", Data: data, Email: *message()},
		"HTML": {HTMLTemplate: "big", Data: data, Email: *message()},
	} {
		err := st.SendFromTemplate(t.Context(), &tm)
		require.ErrorIs(t, err, email.ErrMessageTooLarge, field)
		require.EqualError(t, err, errText(s, opSendTemplate,
			"message too large: "+field+` "big": renders more than 40000000 bytes`))
	}

	assert.Empty(t, s.Sent(t, st))
}

// A nil message is ErrNilMessage, before the sender's state is looked at.
func nilMessage(t *testing.T, s Subject) {
	t.Helper()

	st := build(t, s, nil, shop)

	err := st.Send(t.Context(), nil)
	require.ErrorIs(t, err, email.ErrNilMessage)
	require.EqualError(t, err, errText(s, opSend, "message is nil"))

	err = st.SendFromTemplate(t.Context(), nil)
	require.ErrorIs(t, err, email.ErrNilMessage)
	require.EqualError(t, err, errText(s, opSendTemplate, "message is nil"))
}

// Before Init the methods return ErrNotInitialized, Init twice returns
// ErrAlreadyInitialized, after Close they return ErrClosed, and Init
// after Close restarts the sender.
func lifecycle(t *testing.T, s Subject) {
	t.Helper()

	ctx := t.Context()
	st := build(t, s, nil, shop)
	tm := &gas.TemplatedEmail{Email: *message()}

	require.NoError(t, st.Close(ctx))

	for op, err := range map[string]error{
		opSend:         st.Send(ctx, message()),
		opSendTemplate: st.SendFromTemplate(ctx, tm),
		"ready":        st.CheckReady(ctx),
	} {
		require.ErrorIs(t, err, email.ErrNotInitialized)
		require.EqualError(t, err, errText(s, op, "service not initialized"))
	}

	require.NoError(t, st.Init(ctx))
	require.NoError(t, st.CheckReady(ctx))

	err := st.Init(ctx)
	require.ErrorIs(t, err, email.ErrAlreadyInitialized)
	require.EqualError(t, err, errText(s, "init", "service already initialized"))

	require.NoError(t, st.Send(ctx, message()))
	require.NoError(t, st.Close(ctx))
	require.NoError(t, st.Close(ctx))

	for op, err := range map[string]error{
		opSend:         st.Send(ctx, message()),
		opSendTemplate: st.SendFromTemplate(ctx, tm),
		"ready":        st.CheckReady(ctx),
	} {
		require.ErrorIs(t, err, email.ErrClosed)
		require.EqualError(t, err, errText(s, op, "service closed"))
	}

	require.NoError(t, st.Init(ctx))
	require.NoError(t, st.Send(ctx, message()))
	assert.Len(t, s.Sent(t, st), 2)
}

// A call whose context has ended returns its error, and sends nothing;
// Init with an ended context fails and leaves the sender uninitialized.
func canceledContext(t *testing.T, s Subject) {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	st := build(t, s, nil, shop)

	err := st.Init(ctx)
	require.ErrorIs(t, err, context.Canceled)
	require.EqualError(t, err, errText(s, "init", "context canceled"))

	require.NoError(t, st.Init(t.Context()))

	for op, err := range map[string]error{
		opSend:         st.Send(ctx, message()),
		opSendTemplate: st.SendFromTemplate(ctx, &gas.TemplatedEmail{Email: *message()}),
		"ready":        st.CheckReady(ctx),
	} {
		require.ErrorIs(t, err, context.Canceled)
		require.EqualError(t, err, errText(s, op, "context canceled"))
	}

	assert.Empty(t, s.Sent(t, st))
}

// A context that ends while SendFromTemplate fetches a template ends the
// call with the context's error.
func cancelDuringRender(t *testing.T, s Subject) {
	t.Helper()

	templates := NewTemplates(map[string]string{"s": "Hi"})
	release := templates.hold("s")

	defer release()

	st := open(t, s, templates, shop)

	ctx, cancel := context.WithTimeout(t.Context(), pause)
	defer cancel()

	err := st.SendFromTemplate(ctx, &gas.TemplatedEmail{SubjectTemplate: "s", Email: *message()})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.EqualError(t, err, errText(s, opSendTemplate, "context deadline exceeded"))
	assert.Empty(t, s.Sent(t, st))
}

// Close refuses new sends at once and waits for a send in flight, which
// then completes.
func closeWaits(t *testing.T, s Subject) {
	t.Helper()

	templates := NewTemplates(map[string]string{"s": "Hi"})
	release := templates.hold("s")

	defer release()

	st := open(t, s, templates, shop)

	sendErr := make(chan error, 1)

	go func() {
		sendErr <- st.SendFromTemplate(context.Background(),
			&gas.TemplatedEmail{SubjectTemplate: "s", Email: *message()})
	}()

	<-templates.entered

	closeErr := make(chan error, 1)

	go func() { closeErr <- st.Close(context.Background()) }()

	require.Eventually(t, func() bool {
		return errors.Is(st.CheckReady(t.Context()), email.ErrClosed)
	}, prompt, time.Millisecond)

	err := st.Send(t.Context(), message())
	require.ErrorIs(t, err, email.ErrClosed)

	select {
	case err := <-closeErr:
		t.Fatalf("Close returned %v with a send in flight", err)
	case <-time.After(pause):
	}

	release()

	require.NoError(t, <-sendErr)
	require.NoError(t, <-closeErr)
	assert.Len(t, s.Sent(t, st), 1)
}

// When Close's context ends with a send in flight, Close cancels it,
// which returns ErrClosed, and returns the context's error; the sender
// stays closed. A send whose TemplateProvider ignores the cancellation,
// and so renders after Close returned, sends nothing either.
func closeCancels(t *testing.T, s Subject) {
	t.Helper()

	for name, ignore := range map[string]bool{"honored": false, "ignored": true} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			closeCancelsWith(t, s, ignore)
		})
	}
}

// closeCancelsWith runs closeCancels with a TemplateProvider that ignores
// its context when ignore is set.
func closeCancelsWith(t *testing.T, s Subject, ignore bool) {
	t.Helper()

	templates := NewTemplates(map[string]string{"s": "Hi"})
	release := templates.hold("s")

	defer release()

	var provider gas.TemplateProvider = templates

	if ignore {
		provider = contextIgnoring{templates}
	}

	st := open(t, s, provider, shop)

	sendErr := make(chan error, 1)

	go func() {
		sendErr <- st.SendFromTemplate(context.Background(),
			&gas.TemplatedEmail{SubjectTemplate: "s", Email: *message()})
	}()

	<-templates.entered

	ctx, cancel := context.WithTimeout(t.Context(), pause)
	defer cancel()

	err := st.Close(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.EqualError(t, err, s.Name+": close: context deadline exceeded")

	if ignore {
		release()
	}

	select {
	case err := <-sendErr:
		require.ErrorIs(t, err, email.ErrClosed)
		require.EqualError(t, err, errText(s, opSendTemplate, "service closed"))
	case <-time.After(prompt):
		t.Fatal("the send in flight did not return")
	}

	require.ErrorIs(t, st.Send(t.Context(), message()), email.ErrClosed)
	assert.Empty(t, s.Sent(t, st))
}

// Sends that race Close and Init, for -race, get nil or ErrClosed, never
// ErrNotInitialized once the sender was initialized.
func restartRace(t *testing.T, s Subject) {
	t.Helper()

	st := open(t, s, nil, shop)

	var wg sync.WaitGroup

	stop := make(chan struct{})

	for range 4 {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}

				err := st.Send(context.Background(), message())
				if err != nil && !errors.Is(err, email.ErrClosed) {
					t.Errorf("send during restart: %v", err)

					return
				}
			}
		})
	}

	for range 50 {
		assert.NoError(t, st.Close(context.Background()))
		assert.NoError(t, st.Init(context.Background()))
	}

	close(stop)
	wg.Wait()
}

// Concurrent sends each send once.
func concurrent(t *testing.T, s Subject) {
	t.Helper()

	st := open(t, s, NewTemplates(map[string]string{"s": "Hi {{.}}"}), shop)

	var wg sync.WaitGroup

	for i := range 20 {
		wg.Go(func() {
			if i%2 == 0 {
				assert.NoError(t, st.Send(context.Background(), message()))

				return
			}

			assert.NoError(t, st.SendFromTemplate(context.Background(),
				&gas.TemplatedEmail{SubjectTemplate: "s", Data: i, Email: *message()}))
		})
	}

	wg.Wait()
	assert.Len(t, s.Sent(t, st), 20)
}

// No error quotes an address, a subject, a body, a header value or
// template data.
func errorsHoldNoData(t *testing.T, s Subject) {
	t.Helper()

	st := open(t, s, NewTemplates(map[string]string{"t": "{{.zqx}}{{index .L 9}}"}), shop)

	msgs := []*gas.Email{
		{From: "zqx@exa mple", To: []string{alice}, Subject: subject, TextBody: text},
		{
			To:       []string{"zqx-bob@example.com, zqx-eve@example.com"},
			Subject:  subject,
			TextBody: text,
		},
		{To: []string{"zqx-ø@example.com"}, Subject: subject, TextBody: text},
		{To: []string{alice}, Subject: "zqx\r\n", TextBody: text},
		{To: []string{alice}, Subject: subject, TextBody: "zqx\xff"},
		{
			To:       []string{alice},
			Subject:  subject,
			TextBody: text,
			Headers:  map[string]string{"X-A": "zqx\r\n"},
		},
		{
			To:       []string{alice},
			Subject:  subject,
			TextBody: text,
			Headers:  map[string]string{"X zqx": "v"},
		},
	}

	errs := make([]error, 0, len(msgs)+1)

	for _, m := range msgs {
		errs = append(errs, st.Send(t.Context(), m))
	}

	errs = append(errs, st.SendFromTemplate(t.Context(), &gas.TemplatedEmail{
		TextTemplate: "t",
		Data:         map[string]any{"zqx": "zqx-data", "L": []string{"zqx"}},
		Email:        *message(),
	}))

	for _, err := range errs {
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "zqx")
		assert.NotContains(t, err.Error(), "example.com")
		assert.NotContains(t, err.Error(), "\n")
	}
}
