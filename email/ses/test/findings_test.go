package ses_test

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config/configtest"
	"github.com/gasmod/gas/email"
	"github.com/gasmod/gas/email/internal/conformance"
	"github.com/gasmod/gas/email/ses"
)

// KI-034: gas.Email.Headers was dropped, as the SES API v1 SendEmail has
// no header field. The sender now uses the SES API v2, whose Simple
// content sends them, checked against SES's rules first.
func TestFinding_KI034(t *testing.T) {
	t.Parallel()

	f := newFake(t, nil)
	s := f.open(t, nil)

	m := message()

	m.Headers = map[string]string{"List-Unsubscribe": "<https://example.com/u>", "X-Order": "42"}

	require.NoError(t, s.Send(t.Context(), m))
	assert.Equal(t, m.Headers, f.sent(t)[0].Headers)

	m.Headers = map[string]string{"Bcc": "evil@example.com"}

	err := s.Send(t.Context(), m)
	require.ErrorIs(t, err, email.ErrInvalidHeader)
	assert.Len(t, f.received(), 1)
}

// KI-036: Send before Init dereferenced a nil client and panicked, and
// CheckReady reported a sender that would panic as ready. Both now return
// ErrNotInitialized.
func TestFinding_KI036(t *testing.T) {
	t.Parallel()

	s, _ := build(t, ses.WithConfig(config("https://email.example")))

	err := s.Send(t.Context(), message())
	require.ErrorIs(t, err, email.ErrNotInitialized)
	require.EqualError(t, err, "gas/email/ses: send: service not initialized")

	err = s.SendFromTemplate(t.Context(), &gas.TemplatedEmail{Email: *message()})
	require.ErrorIs(t, err, email.ErrNotInitialized)

	err = s.CheckReady(t.Context())
	require.ErrorIs(t, err, email.ErrNotInitialized)
	require.EqualError(t, err, "gas/email/ses: ready: service not initialized")
}

// A nil logger, a nil TemplateProvider, WithConfig(nil), WithClient(nil)
// and a nil message each panicked. They are now errors.
func TestFinding_NilArguments(t *testing.T) {
	t.Parallel()

	_, err := ses.New()(conformance.NewTemplates(nil), nil, nil)
	require.ErrorIs(t, err, email.ErrNilLogger)

	_, err = ses.New()(nil, nil, nop)
	require.ErrorIs(t, err, email.ErrNilTemplates)

	_, err = ses.New(ses.WithConfig(nil))(conformance.NewTemplates(nil), nil, nop)
	require.ErrorIs(t, err, email.ErrNilConfig)

	err = initErr(t, ses.WithConfig(config("https://email.example")), ses.WithClient(nil))
	require.ErrorIs(t, err, ses.ErrNilClient)

	s := open(t, config("https://email.example"))
	require.ErrorIs(t, s.Send(t.Context(), nil), email.ErrNilMessage)
	require.ErrorIs(t, s.SendFromTemplate(t.Context(), nil), email.ErrNilMessage)
}

// An empty body was sent as an empty part, `"Html":{"Data":""}`. An empty
// body is now left out of the request.
func TestFinding_EmptyPartOmitted(t *testing.T) {
	t.Parallel()

	f := newFake(t, nil)

	require.NoError(t, f.open(t, nil).Send(t.Context(), message()))

	raw := f.received()[0].raw
	assert.NotContains(t, raw, "Html")
	assert.NotContains(t, raw, "CcAddresses")
	assert.NotContains(t, raw, "ReplyToAddresses")
}

// The Region setting was required by its tag, so a bound configuration
// could never use AWS_REGION, while one given to WithConfig could. Both
// now leave an empty region to the AWS SDK's chain.
//
// Not parallel: it sets AWS variables.
func TestFinding_AWSRegion(t *testing.T) {
	isolateAWS(t)
	t.Setenv("AWS_REGION", "eu-west-1")

	p, err := configtest.NewMockConfigWithValues(map[string]any{
		"email": map[string]any{
			"endpoint":        "https://email.example",
			"accesskeyid":     testKeyID,
			"secretaccesskey": testSecret,
		},
	})
	require.NoError(t, err)

	s, err := ses.New()(conformance.NewTemplates(nil), p, nop)
	require.NoError(t, err)
	require.NoError(t, s.Init(t.Context()))
	t.Cleanup(func() { require.NoError(t, s.Close(context.Background())) })

	assert.Equal(t, "eu-west-1", s.Client().Options().Region)
}

// A message without a sender went out with an empty Source, and one with
// no recipient, 51 recipients or an address with a CR or an LF reached
// SES. They are now refused before any request.
func TestFinding_NoValidation(t *testing.T) {
	t.Parallel()

	f := newFake(t, nil)
	s := f.open(t, nil)

	noFrom := message()

	noFrom.From = ""

	crlf := message()

	crlf.To = []string{"a@example.com\r\nBcc: evil@example.com"}

	none := message()

	none.To = nil

	for _, m := range []*gas.Email{noFrom, crlf, none, {}} {
		require.Error(t, s.Send(t.Context(), m))
	}

	assert.Empty(t, f.received())
}

// AWS_MAX_ATTEMPTS overrode MaxRetries: the AWS configuration sets
// RetryMaxAttempts from it, which the client applies over the retryer
// built from MaxRetries. MaxRetries now holds whatever AWS_MAX_ATTEMPTS
// and AWS_RETRY_MODE say.
//
// Not parallel: it sets AWS variables.
func TestFinding_MaxAttemptsEnv(t *testing.T) {
	isolateAWS(t)
	t.Setenv("AWS_MAX_ATTEMPTS", "5")
	t.Setenv("AWS_RETRY_MODE", "adaptive")

	for retries, attempts := range map[int]int32{0: 1, 1: 2} {
		var n atomic.Int32

		f := newFake(t, func(call) reply {
			n.Add(1)

			return awsError(http.StatusInternalServerError, "InternalFailure", "boom")
		})
		s := f.open(t, func(set *ses.Settings) { set.MaxRetries = retries })

		err := s.Send(t.Context(), message())
		require.ErrorIs(t, err, ses.ErrCommand)
		assert.Equal(t, attempts, n.Load(), "MaxRetries %d", retries)
	}
}

// A subject that is not ASCII was sent as UTF-8 for SES to encode. The
// sender now sends it as RFC 2047 encoded words, base64 of its UTF-8; an
// ASCII subject, a tab included, is sent as it is.
func TestFinding_SubjectEncoded(t *testing.T) {
	t.Parallel()

	f := newFake(t, nil)
	s := f.open(t, nil)

	for _, subject := range []string{"Héllo", "Hi\tthere"} {
		m := message()

		m.Subject = subject

		require.NoError(t, s.Send(t.Context(), m))
	}

	calls := f.received()
	require.Len(t, calls, 2)
	assert.Contains(t, calls[0].raw, `"Subject":{"Charset":"UTF-8","Data":"=?UTF-8?b?SMOpbGxv?="}`)
	assert.Contains(t, calls[1].raw, `"Subject":{"Charset":"UTF-8","Data":"Hi\tthere"}`)

	sent := f.sent(t)
	assert.Equal(t, "Héllo", sent[0].Subject)
	assert.Equal(t, "Hi\tthere", sent[1].Subject)
}

// A throttled send whose retries a deadline cut short reported `timed
// out` and ErrConnect, though SES had replied. An AWS error code in the
// error now wins: it is `throttled`, wrapping ErrThrottled and
// context.DeadlineExceeded, not ErrConnect.
func TestFinding_ReplyWinsOverTimeout(t *testing.T) {
	t.Parallel()

	api := &fakeAPI{err: errors.Join(
		&smithy.GenericAPIError{Code: "TooManyRequestsException", Message: "zqx@example.com"},
		context.DeadlineExceeded,
	)}
	s, _ := build(t, ses.WithConfig(ses.DefaultConfig()), ses.WithClient(api))
	require.NoError(t, s.Init(t.Context()))

	err := s.Send(t.Context(), message())
	require.ErrorIs(t, err, ses.ErrThrottled)
	require.ErrorIs(t, err, ses.ErrCommand)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.NotErrorIs(t, err, ses.ErrConnect)
	require.EqualError(t, err, "gas/email/ses: send: throttled (TooManyRequestsException)")

	api.err = context.DeadlineExceeded

	err = s.Send(t.Context(), message())
	require.ErrorIs(t, err, ses.ErrConnect)
	require.EqualError(t, err, "gas/email/ses: send: timed out")
}

// An endpoint could hold a query, a fragment or a path, which could
// carry a secret, such as a token in a query, into every request. The
// endpoint must now be an absolute http or https URL with nothing after
// its host but `/`.
func TestFinding_EndpointOnlyHost(t *testing.T) {
	t.Parallel()

	for _, endpoint := range []string{
		"https://ses.example/?X-Token=zqx", "https://ses.example?", "https://ses.example/#zqx",
		"https://ses.example#", "https://ses.example/zqx", "https://ses.example//",
		"https:ses.example", "http://zqx@ses.example",
	} {
		err := initErr(t, ses.WithConfig(config(endpoint)))
		require.ErrorIs(t, err, email.ErrInvalidConfig, endpoint)
		require.EqualError(t, err, "gas/email/ses: init: invalid config: "+
			"Email.Endpoint must be an http or https URL with a host and nothing more but /")
		assertNoSecrets(t, err.Error(), "ses.example")
	}

	for _, endpoint := range []string{
		"https://ses.example", "https://ses.example/", "http://127.0.0.1:4566",
	} {
		s := open(t, config(endpoint))
		require.NoError(t, s.CheckReady(t.Context()), endpoint)
	}
}
