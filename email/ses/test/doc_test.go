package ses_test

import (
	"context"
	"maps"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/email"
	"github.com/gasmod/gas/email/internal/conformance"
	"github.com/gasmod/gas/email/ses"
)

// The example in the package documentation's Usage section, on the fake,
// which stands for SES.
func TestDoc_Usage(t *testing.T) {
	t.Parallel()

	f := newFake(t, nil)
	ctx := t.Context()
	logger := &gas.NopLogger{}
	templates := conformance.NewTemplates(nil)

	cfg := ses.DefaultConfig()

	cfg.Email.Region = "eu-west-1"
	cfg.Email.FromEmail = "Shop <shop@example.com>"
	cfg.Email.Endpoint = f.srv.URL
	cfg.Email.AccessKeyID, cfg.Email.SecretAccessKey = testKeyID, testSecret

	mailer, err := ses.New(ses.WithConfig(cfg))(templates, nil, logger)
	require.NoError(t, err)
	require.NoError(t, mailer.Init(ctx))

	defer func() { require.NoError(t, mailer.Close(ctx)) }()

	err = mailer.Send(ctx, &gas.Email{
		To:       []string{"alice@example.com"},
		Subject:  "Your order",
		TextBody: "It ships today.",
	})
	require.NoError(t, err)

	sent := f.sent(t)
	require.Len(t, sent, 1)
	assert.Equal(t, `"Shop" <shop@example.com>`, sent[0].From)
}

// replying returns a sender on a fake that answers every request with r,
// without retries.
func replying(t *testing.T, r reply) *ses.Service {
	t.Helper()

	return newFake(t, func(call) reply { return r }).open(t, func(s *ses.Settings) {
		s.MaxRetries = 0
	})
}

// The error messages the package documentation lists are the ones the
// sender returns, and it lists every message this test makes.
//
// Not parallel: it sets AWS variables.
//
//nolint:funlen,contextcheck // one case per documented message, made with the test helpers.
func TestDoc_Errors(t *testing.T) {
	isolateAWS(t)

	rule := func(edit func(*ses.Settings)) error {
		cfg := config("http://127.0.0.1:1")
		edit(&cfg.Email)

		return initErr(t, ses.WithConfig(cfg))
	}
	initial := func() *ses.Service { return open(t, config("http://127.0.0.1:1")) }
	canceled, cancel := context.WithCancel(t.Context())
	cancel()

	hole := "http://" + blackhole(t)
	send := func(s *ses.Service) error { return s.Send(t.Context(), message()) }
	sendReply := func(r reply) error { return send(replying(t, r)) }

	cases := map[string]func() error{
		"gas/email/ses: template provider is nil": func() error {
			_, err := ses.New()(nil, nil, nop)

			return err
		},
		"gas/email/ses: logger is nil": func() error {
			_, err := ses.New()(conformance.NewTemplates(nil), nil, nil)

			return err
		},
		"gas/email/ses: config is nil": func() error {
			_, err := ses.New(ses.WithConfig(nil))(conformance.NewTemplates(nil), nil, nop)

			return err
		},
		"gas/email/ses: init: no config provider": func() error {
			return initErr(t)
		},
		"gas/email/ses: init: invalid config: Email.Endpoint must be an http or https URL with a host " +
			"and nothing more but /": func() error {
			return rule(func(s *ses.Settings) { s.Endpoint = "localhost:8005" })
		},
		"gas/email/ses: init: invalid config: Email.FromEmail must be one email address": func() error {
			return rule(func(s *ses.Settings) { s.FromEmail = "shop" })
		},
		"gas/email/ses: init: invalid config: AWS configuration failed to load": func() error {
			t.Setenv("AWS_PROFILE", "missing")
			defer t.Setenv("AWS_PROFILE", "")

			return rule(func(*ses.Settings) {})
		},
		"gas/email/ses: init: no region": func() error {
			return rule(func(s *ses.Settings) { s.Region = "" })
		},
		"gas/email/ses: init: no credentials: failed": func() error {
			return rule(func(s *ses.Settings) { s.AccessKeyID = "" })
		},
		"gas/email/ses: init: no credentials: timed out": func() error {
			t.Setenv("AWS_CONTAINER_CREDENTIALS_FULL_URI", hole+"/creds")
			defer t.Setenv("AWS_CONTAINER_CREDENTIALS_FULL_URI", "")

			return rule(
				func(s *ses.Settings) { s.AccessKeyID, s.OpTimeout = "", 200*time.Millisecond },
			)
		},
		"gas/email/ses: init: no credentials: timed out: context deadline exceeded": func() error {
			t.Setenv("AWS_CONTAINER_CREDENTIALS_FULL_URI", hole+"/creds")
			defer t.Setenv("AWS_CONTAINER_CREDENTIALS_FULL_URI", "")

			cfg := config(hole)

			cfg.Email.AccessKeyID = ""

			s, _ := build(t, ses.WithConfig(cfg))

			ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
			defer cancel()

			return s.Init(ctx)
		},
		"gas/email/ses: init: client is nil": func() error {
			return initErr(t, ses.WithConfig(ses.DefaultConfig()), ses.WithClient(nil))
		},
		"gas/email/ses: init: service already initialized": func() error {
			return initial().Init(t.Context())
		},
		"gas/email/ses: init: context canceled": func() error {
			s, _ := build(t, ses.WithConfig(config(hole)))

			return s.Init(canceled)
		},
		"gas/email/ses: ready: service not initialized": func() error {
			s, _ := build(t)

			return s.CheckReady(t.Context())
		},
		"gas/email/ses: send: message is nil": func() error {
			return initial().Send(t.Context(), nil)
		},
		"gas/email/ses: send: service not initialized": func() error {
			s, _ := build(t)

			return send(s)
		},
		"gas/email/ses: send: service closed": func() error {
			s := initial()
			require.NoError(t, s.Close(t.Context()))

			return send(s)
		},
		"gas/email/ses: send: context canceled": func() error {
			return initial().Send(canceled, message())
		},
		"gas/email/ses: send: context deadline exceeded": func() error {
			ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
			defer cancel()

			return open(t, config(hole)).Send(ctx, message())
		},
		"gas/email/ses: send: invalid address: To[0]: not one email address": func() error {
			m := message()

			m.To = []string{"alice"}

			return initial().Send(t.Context(), m)
		},
		"gas/email/ses: send: timed out": func() error {
			cfg := config(hole)

			cfg.Email.OpTimeout = 200 * time.Millisecond

			return send(open(t, cfg))
		},
		"gas/email/ses: send: connection refused": func() error {
			cfg := config("http://" + closedAddr(t))

			cfg.Email.MaxRetries = 0

			return send(open(t, cfg))
		},
		"gas/email/ses: send: unreachable": func() error {
			cfg := config("http://gas-email-test.invalid")

			cfg.Email.MaxRetries = 0

			return send(open(t, cfg))
		},
		"gas/email/ses: send: not verified (MessageRejected)": func() error {
			return sendReply(awsError(http.StatusBadRequest, "MessageRejected",
				"Email address is not verified."))
		},
		"gas/email/ses: send: not verified (MailFromDomainNotVerifiedException)": func() error {
			return sendReply(
				awsError(http.StatusBadRequest, "MailFromDomainNotVerifiedException", "no"),
			)
		},
		"gas/email/ses: send: rejected (MessageRejected)": func() error {
			return sendReply(awsError(http.StatusBadRequest, "MessageRejected", "bad"))
		},
		"gas/email/ses: send: throttled (TooManyRequestsException)": func() error {
			return sendReply(
				awsError(http.StatusTooManyRequests, "TooManyRequestsException", "slow"),
			)
		},
		"gas/email/ses: send: sending paused (SendingPausedException)": func() error {
			return sendReply(awsError(http.StatusBadRequest, "SendingPausedException", "paused"))
		},
		"gas/email/ses: send: access denied (AccessDeniedException)": func() error {
			return sendReply(awsError(http.StatusForbidden, "AccessDeniedException", "no"))
		},
		"gas/email/ses: send: failed (BadRequestException)": func() error {
			return sendReply(awsError(http.StatusBadRequest, "BadRequestException", "bad"))
		},
		"gas/email/ses: send: failed": func() error {
			return sendReply(reply{status: http.StatusBadRequest, body: "not json"})
		},
		"gas/email/ses: send: invalid reply": func() error {
			return sendReply(reply{body: string(make([]byte, 2<<20))})
		},
		`gas/email/ses: send from template: template failed: subject "welcome-subject.txt": fetch failed`: func() error {
			return initial().SendFromTemplate(t.Context(), &gas.TemplatedEmail{
				SubjectTemplate: "welcome-subject.txt",
				Email:           *message(),
			})
		},
		"gas/email/ses: close: context deadline exceeded": func() error {
			cfg := config(hole)

			cfg.Email.OpTimeout = time.Minute

			s := open(t, cfg)
			sent := make(chan error, 1)

			go func() { sent <- send(s) }()

			time.Sleep(100 * time.Millisecond)

			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			defer cancel()

			err := s.Close(ctx)

			require.ErrorIs(t, <-sent, email.ErrClosed)

			return err
		},
	}

	for _, text := range slices.Sorted(maps.Keys(cases)) {
		require.EqualError(t, cases[text](), text)
	}

	documented := docLines(t, "../doc.go", "gas/email/ses: ")
	assert.ElementsMatch(t, slices.Collect(maps.Keys(cases)), documented)

	cfg := config(hole)

	cfg.Email.OpTimeout = 0

	err := initErr(t, ses.WithConfig(cfg))
	require.EqualError(t, err, "gas/email/ses: init: invalid config: validate config: "+
		docLines(t, "../doc.go", "Key: ")[0])
}
