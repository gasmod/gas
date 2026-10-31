package email_test

import (
	"bufio"
	"context"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/email"
	"github.com/gasmod/gas/email/internal/conformance"
	"github.com/gasmod/gas/email/memory"
)

// The example in the package documentation's Usage section, on the
// memory sender.
func TestDoc_Usage(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	templates := conformance.NewTemplates(map[string]string{
		"welcome-subject.txt": "Welcome, {{.Name}}",
		"welcome.html":        "<p>Hello {{.Name}}</p>",
	})

	cfg := memory.DefaultConfig()

	cfg.Email.FromEmail = "Shop <shop@example.com>"

	mailer, err := memory.New(memory.WithConfig(cfg))(templates, nil, &gas.NopLogger{})
	require.NoError(t, err)
	require.NoError(t, mailer.Init(ctx))

	defer func() { require.NoError(t, mailer.Close(ctx)) }()

	err = mailer.Send(ctx, &gas.Email{
		From:     "Shop <shop@example.com>",
		To:       []string{"alice@example.com"},
		Subject:  "Your order",
		TextBody: "It ships today.",
	})
	require.NoError(t, err)

	err = mailer.SendFromTemplate(ctx, &gas.TemplatedEmail{
		SubjectTemplate: "welcome-subject.txt",
		HTMLTemplate:    "welcome.html",
		Data:            map[string]string{"Name": "Alice"},
		To:              []string{"alice@example.com"},
	})
	require.NoError(t, err)

	sent := mailer.Messages()
	require.Len(t, sent, 2)
	assert.Equal(t, `"Shop" <shop@example.com>`, sent[1].From)
	assert.Equal(t, "Welcome, Alice", sent[1].Subject)
	assert.Equal(t, "<p>Hello Alice</p>", sent[1].HTMLBody)
}

// errorLines returns the code lines of the package documentation's
// Errors section.
func errorLines(t *testing.T) []string {
	t.Helper()

	f, err := os.Open("../doc.go")
	require.NoError(t, err)

	defer func() { require.NoError(t, f.Close()) }()

	var (
		out    []string
		inside bool
	)

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if line == "// # Errors" {
			inside = true
		}

		code, ok := strings.CutPrefix(line, "//\t")
		if inside && ok {
			out = append(out, code)
		}
	}

	require.NoError(t, sc.Err())

	return out
}

// The error messages the package documentation lists are the ones the
// checks return, and it lists every message this test makes. The
// template errors come from a sender, without its prefix.
//
//nolint:funlen // one case per documented message.
func TestDoc_Errors(t *testing.T) {
	t.Parallel()

	check := func(edit func(*gas.Email)) func() error {
		return func() error {
			m := &gas.Email{
				From:     "shop@example.com",
				To:       []string{"alice@example.com"},
				Subject:  "Hi",
				TextBody: "Hello",
			}
			edit(m)

			_, err := email.CheckEmail(m, "")

			return err
		}
	}
	to := func(addr string) func() error {
		return check(func(m *gas.Email) { m.To = []string{addr} })
	}
	header := func(name, value string) func() error {
		return check(func(m *gas.Email) { m.Headers = map[string]string{name: value} })
	}

	templates := conformance.NewTemplates(map[string]string{
		"welcome.txt":  "{{.Name",
		"welcome.html": "{{index .X 3}}",
		"large.txt":    "{{range .N}}{{$.S}}{{end}}",
	})
	newSender := memory.New(memory.WithConfig(memory.DefaultConfig()))

	s, err := newSender(templates, nil, &gas.NopLogger{})
	require.NoError(t, err)
	require.NoError(t, s.Init(t.Context()))
	t.Cleanup(func() { require.NoError(t, s.Close(context.Background())) })

	render := func(tm gas.TemplatedEmail) func() error {
		return func() error {
			tm.Email = gas.Email{
				From:     "a@example.com",
				To:       []string{"b@example.com"},
				Subject:  "s",
				TextBody: "t",
			}

			sendErr := s.SendFromTemplate(t.Context(), &tm)
			require.Error(t, sendErr)

			return &trimmed{sendErr, memory.ServiceName + ": send from template: "}
		}
	}

	many := map[string]string{}
	for i := range 16 {
		many[strings.Repeat("X", i+1)] = "v"
	}

	cases := map[string]func() error{
		"message is nil": func() error {
			_, nilErr := email.CheckEmail(nil, "")

			return nilErr
		},
		"invalid address: From: no sender, and no default sender": check(
			func(m *gas.Email) { m.From = "" },
		),
		"invalid address: To[0]: empty":                 to(""),
		"invalid address: To[0]: not one email address": to("alice"),
		"invalid address: To[0]: the display name holds a control character": to(
			"=?utf-8?q?a=0A?= <a@example.com>",
		),
		"invalid address: To[0]: the display name is not valid UTF-8": to(
			"=?utf-8?q?=FF?= <a@example.com>",
		),
		"invalid address: To[0]: the local part is not ASCII": to("bjørn@example.com"),
		"invalid address: To[0]: the local part holds an at sign or an encoded word": to(
			`"a@b.example"@example.com`,
		),
		"invalid address: To[0]: the domain is not a valid domain name": to("a@bü_x.example"),
		"no recipients": check(func(m *gas.Email) { m.To = nil }),
		"too many recipients: 51, at most 50": check(func(m *gas.Email) {
			m.To = slices.Repeat([]string{"a@example.com"}, 51)
		}),
		"invalid subject: empty": check(func(m *gas.Email) { m.Subject = "" }),
		"invalid subject: not valid UTF-8": check(
			func(m *gas.Email) { m.Subject = "\xff" },
		),
		"invalid subject: holds a control character": check(
			func(m *gas.Email) { m.Subject = "a\nb" },
		),
		"empty body": check(func(m *gas.Email) { m.TextBody = "" }),
		"invalid body: TextBody is not valid UTF-8": check(
			func(m *gas.Email) { m.TextBody = "\xff" },
		),
		"invalid body: HTMLBody is not valid UTF-8": check(
			func(m *gas.Email) { m.HTMLBody = "\xff" },
		),
		"invalid header: 16 headers, at most 15": check(
			func(m *gas.Email) { m.Headers = many },
		),
		"invalid header: a name is empty or longer than 126 bytes": header(
			"",
			"v",
		),
		"invalid header: a name holds a character other than printable ASCII but ':'": header(
			"X:",
			"v",
		),
		`invalid header: "Reply-To" is set by the module or SES`: header(
			"reply-TO",
			"v",
		),
		"invalid header: a name starting with Content- is set by the module or SES": header(
			"Content-Id",
			"v",
		),
		"invalid header: a name starting with Resent- is set by the module or SES": header(
			"resent-from",
			"v",
		),
		"invalid header: a name starting with X-SES- is set by the module or SES": header(
			"x-ses-configuration-set",
			"v",
		),
		"invalid header: a value is empty or longer than 995 bytes": header(
			"X",
			"",
		),
		"invalid header: a value holds a character other than printable ASCII": header(
			"X",
			"a\r\n",
		),
		"invalid header: a name and its value are longer than 996 bytes together": header(
			"XX", strings.Repeat("v", 995),
		),
		"invalid header: two names differ only in case": check(func(m *gas.Email) {
			m.Headers = map[string]string{"X": "1", "x": "2"}
		}),
		"message too large: about 44481865 bytes, more than 40000000": check(func(m *gas.Email) {
			m.TextBody = strings.Repeat("a", 31<<20)
		}),
		`message too large: text "large.txt": renders more than 40000000 bytes`: render(
			gas.TemplatedEmail{TextTemplate: "large.txt", Data: map[string]any{
				"N": make([]int, 40), "S": strings.Repeat("a", 1<<20),
			}},
		),
		`template failed: subject "welcome-subject.txt": fetch failed`: render(
			gas.TemplatedEmail{SubjectTemplate: "welcome-subject.txt"},
		),
		`template failed: text "welcome.txt": parse failed`: render(
			gas.TemplatedEmail{TextTemplate: "welcome.txt"},
		),
		`template failed: HTML "welcome.html": execute failed`: render(
			gas.TemplatedEmail{HTMLTemplate: "welcome.html", Data: map[string][]int{"X": {1}}},
		),
	}

	for _, text := range slices.Sorted(maps.Keys(cases)) {
		require.EqualError(t, cases[text](), text)
	}

	assert.ElementsMatch(t, slices.Collect(maps.Keys(cases)), errorLines(t))

	_, err = email.NormalizeAddress("alice")
	require.EqualError(t, err, "invalid address: not one email address")
}

// trimmed is an error whose text is another's without a prefix.
type trimmed struct {
	// err is the error.
	err error
	// prefix is cut from its text.
	prefix string
}

// Error returns the text of err without prefix.
func (e *trimmed) Error() string { return strings.TrimPrefix(e.err.Error(), e.prefix) }
