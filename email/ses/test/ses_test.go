package ses_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config/configtest"
	"github.com/gasmod/gas/email"
	"github.com/gasmod/gas/email/internal/conformance"
	"github.com/gasmod/gas/email/ses"
)

// message returns a valid message to alice from the shop.
func message() *gas.Email {
	return &gas.Email{
		From:     "shop@example.com",
		To:       []string{"alice@example.com"},
		Subject:  "Hi",
		TextBody: "Hello",
	}
}

// The secret access key and the session token are gas.Secrets, so a
// Config prints them as `[redacted]`.
func TestSecret_Redacted(t *testing.T) {
	t.Parallel()

	cfg := ses.DefaultConfig()

	cfg.Email.SecretAccessKey = "zqxsecret"
	cfg.Email.SessionToken = "zqxtoken"

	text := fmt.Sprintf("%+v", cfg)
	assert.NotContains(t, text, "zqx")
	assert.Contains(t, text, "[redacted]")
}

// DefaultConfig has no region, endpoint, default sender or credentials,
// and the default operation timeout and retries.
func TestDefaultConfig(t *testing.T) {
	t.Parallel()

	assert.Equal(t, &ses.Config{Email: ses.Settings{
		OpTimeout:  10 * time.Second,
		MaxRetries: 2,
	}}, ses.DefaultConfig())
}

// Init binds the `email` section from the ConfigProvider over
// DefaultConfig, the secret keys included, and normalizes the default
// sender.
func TestInit_Binds(t *testing.T) {
	t.Parallel()

	f := newFake(t, nil)

	p, err := configtest.NewMockConfigWithValues(map[string]any{
		"email": map[string]any{
			"region":          "eu-west-1",
			"endpoint":        f.srv.URL,
			"fromemail":       "Shop <shop@bücher.example>",
			"accesskeyid":     "AKID",
			"secretaccesskey": "zqxsecret",
			"sessiontoken":    "zqxtoken",
			"optimeout":       "2s",
			"maxretries":      0,
		},
	})
	require.NoError(t, err)

	s, err := ses.New()(conformance.NewTemplates(nil), p, nop)
	require.NoError(t, err)
	require.NoError(t, s.Init(t.Context()))
	t.Cleanup(func() { require.NoError(t, s.Close(context.Background())) })

	o := s.Client().Options()
	assert.Equal(t, "eu-west-1", o.Region)
	assert.Equal(t, f.srv.URL, aws.ToString(o.BaseEndpoint))

	creds, err := o.Credentials.Retrieve(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "AKID", creds.AccessKeyID)
	assert.Equal(t, "zqxsecret", creds.SecretAccessKey)
	assert.Equal(t, "zqxtoken", creds.SessionToken)
	assert.Equal(t, 1, o.Retryer.MaxAttempts())

	m := message()

	m.From = ""

	require.NoError(t, s.Send(t.Context(), m))
	assert.Equal(t, `"Shop" <shop@xn--bcher-kva.example>`, f.sent(t)[0].From)
}

// A configuration given to WithConfig is copied on each Init and checked
// against its validate tags, and the rules run on it, naming the setting
// and never its value.
func TestInit_ConfigRules(t *testing.T) {
	t.Parallel()

	cases := map[string]func(*ses.Settings){
		"invalid config: Email.Endpoint must be an http or https URL with a host and nothing more but /": func(
			s *ses.Settings,
		) {
			s.Endpoint = "http://zqxuser:zqxpass@127.0.0.1:1"
		},
		"invalid config: Email.FromEmail must be one email address": func(s *ses.Settings) {
			s.FromEmail = "zqx@example.com, other@example.com"
		},
		"invalid config: validate config: Key: 'Config.Email.OpTimeout' Error:Field validation for " +
			"'OpTimeout' failed on the 'gt' tag": func(s *ses.Settings) { s.OpTimeout = 0 },
		"invalid config: validate config: Key: 'Config.Email.MaxRetries' Error:Field validation for " +
			"'MaxRetries' failed on the 'max' tag": func(s *ses.Settings) { s.MaxRetries = ses.MaxRetries + 1 },
		"invalid config: validate config: Key: 'Config.Email.MaxRetries' Error:Field validation for " +
			"'MaxRetries' failed on the 'min' tag": func(s *ses.Settings) { s.MaxRetries = -1 },
	}

	for text, edit := range cases {
		cfg := config("http://127.0.0.1:1")
		edit(&cfg.Email)

		err := initErr(t, ses.WithConfig(cfg))
		require.ErrorIs(t, err, email.ErrInvalidConfig, text)
		require.EqualError(t, err, "gas/email/ses: init: "+text)
		assertNoSecrets(t, err.Error(), "example.com")
	}

	for _, endpoint := range []string{"ftp://host", "host:4566", "http://", ":"} {
		err := initErr(t, ses.WithConfig(config(endpoint)))
		require.ErrorIs(t, err, email.ErrInvalidConfig, endpoint)
	}

	cfg := config("https://email.example")

	cfg.Email.FromEmail = "Shop <shop@bücher.example>"

	s := open(t, cfg)
	require.NoError(t, s.CheckReady(t.Context()))
	assert.Equal(
		t,
		"Shop <shop@bücher.example>",
		cfg.Email.FromEmail,
		"Init changed the caller's Config",
	)

	cfg.Email.OpTimeout = 0

	require.NoError(t, s.Close(t.Context()))
	require.ErrorIs(
		t,
		s.Init(t.Context()),
		email.ErrInvalidConfig,
		"Init copies the Config each time",
	)
}

// Without WithConfig and without a ConfigProvider, Init fails with
// ErrNoConfigProvider; a binder's error is ErrInvalidConfig.
func TestInit_NoConfigProvider(t *testing.T) {
	t.Parallel()

	err := initErr(t)
	require.ErrorIs(t, err, email.ErrNoConfigProvider)
	require.EqualError(t, err, "gas/email/ses: init: no config provider")

	p, err := configtest.NewMockConfigWithValues(map[string]any{
		"email": map[string]any{"optimeout": "soon"},
	})
	require.NoError(t, err)

	s, err := ses.New()(conformance.NewTemplates(nil), p, nop)
	require.NoError(t, err)

	err = s.Init(t.Context())
	require.ErrorIs(t, err, email.ErrInvalidConfig)
	assert.True(t, strings.HasPrefix(err.Error(), "gas/email/ses: init: invalid config: "), err)
}

// The constructor refuses a nil TemplateProvider, a nil logger and
// WithConfig(nil), in that order.
func TestNew_Nil(t *testing.T) {
	t.Parallel()

	templates := conformance.NewTemplates(nil)

	_, err := ses.New()(nil, nil, nil)
	require.ErrorIs(t, err, email.ErrNilTemplates)
	require.EqualError(t, err, "gas/email/ses: template provider is nil")

	_, err = ses.New()(templates, nil, nil)
	require.ErrorIs(t, err, email.ErrNilLogger)
	require.EqualError(t, err, "gas/email/ses: logger is nil")

	_, err = ses.New(ses.WithConfig(nil))(templates, nil, nop)
	require.ErrorIs(t, err, email.ErrNilConfig)
	require.EqualError(t, err, "gas/email/ses: config is nil")
}

// Send is one POST to /v2/email/outbound-emails with Simple content in
// the JSON the SES API v2 reference describes: the normalized addresses,
// the subject and each body that is not empty with the UTF-8 character
// set, the headers in the order of their names, and no list or reply-to
// address that is empty.
func TestSend_Wire(t *testing.T) {
	t.Parallel()

	f := newFake(t, nil)
	s := f.open(t, nil)

	m := message()

	m.Headers = map[string]string{"X-B": "2", "X-A": "1"}

	require.NoError(t, s.Send(t.Context(), m))

	m = message()
	m.ReplyTo, m.Cc, m.TextBody, m.HTMLBody = "Help <help@example.com>", []string{
		"c@example.com",
	}, "", "<p>Hi</p>"

	require.NoError(t, s.Send(t.Context(), m))

	calls := f.received()
	require.Len(t, calls, 2)

	for _, c := range calls {
		assert.Equal(t, http.MethodPost, c.method)
		assert.Equal(t, "/v2/email/outbound-emails", c.path)
		assert.Equal(t, "application/json", c.header.Get("Content-Type"))
		assert.Contains(t, c.header.Get("Authorization"), "/"+testRegion+"/ses/aws4_request")
	}

	assert.JSONEq(t, `{
		"Content": {"Simple": {
			"Body": {"Text": {"Charset": "UTF-8", "Data": "Hello"}},
			"Headers": [{"Name": "X-A", "Value": "1"}, {"Name": "X-B", "Value": "2"}],
			"Subject": {"Charset": "UTF-8", "Data": "Hi"}
		}},
		"Destination": {"ToAddresses": ["alice@example.com"]},
		"FromEmailAddress": "shop@example.com"
	}`, calls[0].raw)
	assert.JSONEq(t, `{
		"Content": {"Simple": {
			"Body": {"Html": {"Charset": "UTF-8", "Data": "<p>Hi</p>"}},
			"Subject": {"Charset": "UTF-8", "Data": "Hi"}
		}},
		"Destination": {"CcAddresses": ["c@example.com"], "ToAddresses": ["alice@example.com"]},
		"FromEmailAddress": "shop@example.com",
		"ReplyToAddresses": ["\"Help\" <help@example.com>"]
	}`, calls[1].raw)
}

// A send that fails with a server error is retried up to MaxRetries
// times, so a retried send can reach SES twice; MaxRetries 0 sends once.
func TestSend_Retries(t *testing.T) {
	t.Parallel()

	var n atomic.Int32

	f := newFake(t, func(call) reply {
		if n.Add(1) == 1 {
			return awsError(http.StatusInternalServerError, "InternalFailure", "boom")
		}

		return sendReply
	})
	s := f.open(t, nil)

	require.NoError(t, s.Send(t.Context(), message()))
	assert.Len(t, f.received(), 2)

	f = newFake(t, func(call) reply {
		return awsError(http.StatusInternalServerError, "InternalFailure", "boom")
	})
	s = f.open(t, func(set *ses.Settings) { set.MaxRetries = 0 })

	err := s.Send(t.Context(), message())
	require.ErrorIs(t, err, ses.ErrCommand)
	require.EqualError(t, err, "gas/email/ses: send: failed (InternalFailure)")
	assert.Len(t, f.received(), 1)

	f = newFake(t, func(call) reply {
		return awsError(http.StatusTooManyRequests, "TooManyRequestsException", "slow down")
	})
	s = f.open(t, func(set *ses.Settings) { set.MaxRetries = 1 })

	err = s.Send(t.Context(), message())
	require.ErrorIs(t, err, ses.ErrThrottled)
	require.EqualError(t, err, "gas/email/ses: send: throttled (TooManyRequestsException)")
	assert.Len(t, f.received(), 2)
}

// An error reply of SES is classified by its code into the sentinels,
// with the code shown when it is a known one, and never the reply's
// message, which can quote an address.
//
//nolint:funlen // one case per reply.
func TestErrors_Replies(t *testing.T) {
	t.Parallel()

	const leak = "Email address is not verified: zqx@example.com"

	cases := []struct {
		reply reply
		text  string
		errs  []error
	}{
		{
			awsError(400, "MessageRejected", leak), "not verified (MessageRejected)",
			[]error{ses.ErrNotVerified, ses.ErrRejected, ses.ErrCommand},
		},
		{
			awsError(
				400,
				"MessageRejected",
				"zqx@example.com bad content",
			),
			"rejected (MessageRejected)",
			[]error{ses.ErrRejected, ses.ErrCommand},
		},
		{
			awsError(400, "MailFromDomainNotVerifiedException", leak),
			"not verified (MailFromDomainNotVerifiedException)",
			[]error{ses.ErrNotVerified, ses.ErrCommand},
		},
		{
			awsError(400, "LimitExceededException", leak), "throttled (LimitExceededException)",
			[]error{ses.ErrThrottled, ses.ErrCommand},
		},
		{
			awsError(
				400,
				"SendingPausedException",
				leak,
			),
			"sending paused (SendingPausedException)",
			[]error{ses.ErrSendingPaused, ses.ErrCommand},
		},
		{
			awsError(
				400,
				"AccountSuspendedException",
				leak,
			),
			"sending paused (AccountSuspendedException)",
			[]error{ses.ErrSendingPaused, ses.ErrCommand},
		},
		{
			awsError(403, "AccessDeniedException", leak), "access denied (AccessDeniedException)",
			[]error{ses.ErrAccessDenied, ses.ErrCommand},
		},
		{
			awsError(403, "com.amazonaws.sesv2#InvalidSignatureException:http://zqx", leak),
			"access denied (InvalidSignatureException)",
			[]error{ses.ErrAccessDenied, ses.ErrCommand},
		},
		{
			awsError(400, "BadRequestException", leak),
			"failed (BadRequestException)",
			[]error{ses.ErrCommand},
		},
		{awsError(400, "ZqxUnknownCode", leak), "failed", []error{ses.ErrCommand}},
		{reply{status: 400, body: "not json zqx@example.com"}, "failed", []error{ses.ErrCommand}},
		{reply{body: "not json zqx@example.com"}, "failed", []error{ses.ErrCommand}},
		{reply{body: strings.Repeat(" ", 2<<20) + "{}"}, "invalid reply", []error{ses.ErrCommand}},
		{
			reply{status: 400, body: strings.Repeat(" ", 2<<20) + "{}"},
			"invalid reply",
			[]error{ses.ErrCommand},
		},
	}

	for _, c := range cases {
		s := newFake(t, func(call) reply { return c.reply }).open(t, func(set *ses.Settings) {
			set.MaxRetries = 0
		})

		err := s.Send(t.Context(), message())
		for _, want := range c.errs {
			require.ErrorIs(t, err, want, c.text)
		}

		require.EqualError(t, err, "gas/email/ses: send: "+c.text)
		assertNoSecrets(t, err.Error(), "example.com", "verified:")
	}
}

// fakeAPI is an API whose SendEmail returns out and err, and records its
// inputs.
type fakeAPI struct {
	// err is what SendEmail returns.
	err error
	// out is what SendEmail returns.
	out *sesv2.SendEmailOutput
	// in are the inputs SendEmail got.
	in []*sesv2.SendEmailInput
}

// SendEmail records params and returns out and err.
func (f *fakeAPI) SendEmail(
	_ context.Context,
	params *sesv2.SendEmailInput,
	_ ...func(*sesv2.Options),
) (*sesv2.SendEmailOutput, error) {
	f.in = append(f.in, params)

	return f.out, f.err
}

// WithClient sends with the given API, and Init then needs no region or
// credentials; Client returns nil for an API that is not an
// *sesv2.Client. A client that returns neither an output nor an error
// gets an invalid reply. A nil client, or a nil *sesv2.Client, fails
// Init with ErrNilClient.
func TestWithClient(t *testing.T) {
	t.Parallel()

	api := &fakeAPI{out: &sesv2.SendEmailOutput{}}
	cfg := ses.DefaultConfig()
	s, log := build(t, ses.WithConfig(cfg), ses.WithClient(api))

	require.NoError(t, s.Init(t.Context()))
	require.NoError(t, s.Send(t.Context(), message()))
	assert.Len(t, api.in, 1)
	assert.Nil(t, s.Client())
	assert.NotContains(t, log.entries()[0].fields, "credentials")
	assert.Equal(t, true, log.entries()[0].fields["client"])

	api.out = nil

	err := s.Send(t.Context(), message())
	require.ErrorIs(t, err, ses.ErrCommand)
	require.EqualError(t, err, "gas/email/ses: send: invalid reply")

	for _, client := range []ses.API{nil, (*sesv2.Client)(nil)} {
		err := initErr(t, ses.WithConfig(cfg), ses.WithClient(client))
		require.ErrorIs(t, err, ses.ErrNilClient)
		require.EqualError(t, err, "gas/email/ses: init: client is nil")
	}
}

// Client returns the *sesv2.Client Init built, and nil before Init and
// after Close.
func TestClient(t *testing.T) {
	t.Parallel()

	s, _ := build(t, ses.WithConfig(config("https://email.example")))
	assert.Nil(t, s.Client())

	require.NoError(t, s.Init(t.Context()))
	require.NotNil(t, s.Client())
	assert.Equal(t, testRegion, s.Client().Options().Region)

	require.NoError(t, s.Close(t.Context()))
	assert.Nil(t, s.Client())
}

// Init logs the region, whether an endpoint, a client and a default
// sender were given, the credential source, the operation timeout and the
// retries, and Close logs `service closed`; no log holds an address or a
// credential.
func TestLogs(t *testing.T) {
	t.Parallel()

	f := newFake(t, nil)
	cfg := config(f.srv.URL)

	cfg.Email.FromEmail = "zqx-shop@example.com"

	s, log := build(t, ses.WithConfig(cfg))
	require.NoError(t, s.Init(t.Context()))
	require.NoError(t, s.Send(t.Context(), message()))
	require.NoError(t, s.Close(t.Context()))

	assert.Equal(t, []entry{
		{level: "info", msg: "service initialized", fields: map[string]any{
			"service":     ses.ServiceName,
			"region":      testRegion,
			"endpoint":    true,
			"client":      false,
			"from":        true,
			"credentials": "static",
			"op_timeout":  10 * time.Second,
			"max_retries": 2,
		}},
		{level: "info", msg: "service closed", fields: map[string]any{"service": ses.ServiceName}},
	}, log.entries())
	assertNoSecrets(t, log.text(), "example.com", testKeyID)
}

// Name returns ServiceName.
func TestName(t *testing.T) {
	t.Parallel()

	s, _ := build(t)
	assert.Equal(t, "gas/email/ses", s.Name())
}
