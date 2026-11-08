package ses_test

import (
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/email/ses"
)

// The static credentials and the region the tests give the fake, which
// checks neither.
const (
	testKeyID  = "AKIDZQXTEST"
	testSecret = "zqxsecret"
	testRegion = "us-east-1"
)

// call is one request a fakeSES received.
type call struct {
	// body is the request's JSON body, decoded.
	body map[string]any
	// header holds the request's headers.
	header http.Header
	// raw is the request's body as sent.
	raw string
	// method is the HTTP method.
	method string
	// path is the request's path.
	path string
}

// reply is what a fakeSES answers a request with.
type reply struct {
	// header holds extra response headers.
	header map[string]string
	// body is the response body.
	body string
	// status is the HTTP status; 0 is 200.
	status int
}

// sendReply is the reply SES sends to an accepted SendEmail.
var sendReply = reply{body: `{"MessageId":"0100zqx-message-id"}`}

// fakeSES is an HTTP server that speaks the REST-JSON protocol of the SES
// API v2, answering each request as its handler says. It is safe for
// concurrent use.
type fakeSES struct {
	// handle answers a request; nil answers with sendReply.
	handle func(c call) reply
	// srv is the server.
	srv *httptest.Server
	// calls are the requests received, in order.
	calls []call
	// mu guards calls and handle.
	mu sync.Mutex
}

// newFake returns a fake SES server answering with handle, closed when
// the test ends. A nil handle answers every request with sendReply.
func newFake(t *testing.T, handle func(c call) reply) *fakeSES {
	t.Helper()

	f := &fakeSES{handle: handle}

	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)

	return f
}

// serve records a request and writes its reply.
func (f *fakeSES) serve(w http.ResponseWriter, r *http.Request) {
	b, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)

		return
	}

	c := call{
		method: r.Method,
		path:   r.URL.Path,
		header: r.Header.Clone(),
		raw:    string(b),
		body:   map[string]any{},
	}

	_ = json.Unmarshal(b, &c.body)

	f.mu.Lock()

	f.calls = append(f.calls, c)

	handle := f.handle
	f.mu.Unlock()

	rep := sendReply
	if handle != nil {
		rep = handle(c)
	}

	for k, v := range rep.header {
		w.Header().Set(k, v)
	}

	w.Header().Set("Content-Type", "application/json")

	if rep.status != 0 {
		w.WriteHeader(rep.status)
	}

	_, _ = io.WriteString(w, rep.body)
}

// awsError returns an error reply of status with code, in the
// X-Amzn-ErrorType header as SES sends it, and message.
func awsError(status int, code, message string) reply {
	return reply{
		status: status,
		header: map[string]string{"X-Amzn-ErrorType": code},
		body:   `{"message":` + quote(message) + `}`,
	}
}

// quote returns s, which is ASCII without `"` or `\`, as a JSON
// string.
func quote(s string) string { return `"` + s + `"` }

// received returns a copy of the requests received.
func (f *fakeSES) received() []call {
	f.mu.Lock()
	defer f.mu.Unlock()

	return slices.Clone(f.calls)
}

// sent returns the messages of the requests received, as gas.Email.
func (f *fakeSES) sent(t *testing.T) []gas.Email {
	t.Helper()

	calls := f.received()
	out := make([]gas.Email, 0, len(calls))

	for _, c := range calls {
		out = append(out, decode(t, c.raw))
	}

	return out
}

// sendRequest is the JSON body of a SendEmail request, as the SES API
// v2 reference describes it.
type sendRequest struct {
	Content struct {
		Simple *struct {
			Body struct {
				Text *content `json:"Text"`
				HTML *content `json:"Html"`
			} `json:"Body"`
			Subject content `json:"Subject"`
			Headers []struct {
				Name  string `json:"Name"`
				Value string `json:"Value"`
			} `json:"Headers"`
		} `json:"Simple"`
	} `json:"Content"`
	Destination *struct {
		ToAddresses  []string `json:"ToAddresses"`
		CcAddresses  []string `json:"CcAddresses"`
		BccAddresses []string `json:"BccAddresses"`
	} `json:"Destination"`
	FromEmailAddress string   `json:"FromEmailAddress"`
	ReplyToAddresses []string `json:"ReplyToAddresses"`
}

// content is a Content of the SES API v2: data and its character set.
type content struct {
	Data    string `json:"Data"`
	Charset string `json:"Charset"`
}

// decode returns the message of the SendEmail request body raw, with its
// subject decoded from RFC 2047 encoded words, and fails the test when a
// part is sent without the UTF-8 character set or the subject is sent
// with a character outside ASCII.
func decode(t *testing.T, raw string) gas.Email {
	t.Helper()

	var req sendRequest

	require.NoError(t, json.Unmarshal([]byte(raw), &req))
	require.NotNil(t, req.Content.Simple)
	require.NotNil(t, req.Destination)

	simple := req.Content.Simple

	for _, r := range simple.Subject.Data {
		require.Less(t, r, rune(0x80), "subject sent unencoded: %q", simple.Subject.Data)
	}

	subject, err := new(mime.WordDecoder).DecodeHeader(simple.Subject.Data)
	require.NoError(t, err)

	m := gas.Email{
		From:    req.FromEmailAddress,
		To:      req.Destination.ToAddresses,
		Cc:      req.Destination.CcAddresses,
		Bcc:     req.Destination.BccAddresses,
		Subject: subject,
	}

	require.Equal(t, "UTF-8", simple.Subject.Charset)

	if simple.Body.Text != nil {
		require.Equal(t, "UTF-8", simple.Body.Text.Charset)

		m.TextBody = simple.Body.Text.Data
	}

	if simple.Body.HTML != nil {
		require.Equal(t, "UTF-8", simple.Body.HTML.Charset)

		m.HTMLBody = simple.Body.HTML.Data
	}

	if len(req.ReplyToAddresses) > 0 {
		require.Len(t, req.ReplyToAddresses, 1)

		m.ReplyTo = req.ReplyToAddresses[0]
	}

	for _, h := range simple.Headers {
		if m.Headers == nil {
			m.Headers = map[string]string{}
		}

		m.Headers[h.Name] = h.Value
	}

	return m
}

// config returns a configuration pointed at endpoint, with the test
// region and static credentials.
func config(endpoint string) *ses.Config {
	cfg := ses.DefaultConfig()

	cfg.Email.Region = testRegion
	cfg.Email.Endpoint = endpoint
	cfg.Email.AccessKeyID = testKeyID
	cfg.Email.SecretAccessKey = testSecret

	return cfg
}

// open returns a sender on the fake server, with the settings of its
// configuration changed by edit, initialized and closed when the test
// ends.
func (f *fakeSES) open(t *testing.T, edit func(*ses.Settings)) *ses.Service {
	t.Helper()

	cfg := config(f.srv.URL)
	if edit != nil {
		edit(&cfg.Email)
	}

	return open(t, cfg)
}

// isolateAWS clears the ambient AWS configuration for the test: the
// shared files, the instance metadata, and the AWS variables.
func isolateAWS(t *testing.T) {
	t.Helper()

	empty := filepath.Join(t.TempDir(), "empty")
	require.NoError(t, os.WriteFile(empty, nil, 0o600))

	t.Setenv("AWS_CONFIG_FILE", empty)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", empty)
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")

	for _, name := range []string{
		"AWS_PROFILE",
		"AWS_DEFAULT_PROFILE",
		"AWS_REGION",
		"AWS_DEFAULT_REGION",
		"AWS_ACCESS_KEY_ID",
		"AWS_SECRET_ACCESS_KEY",
		"AWS_SESSION_TOKEN",
		"AWS_ENDPOINT_URL",
		"AWS_ENDPOINT_URL_SESV2",
		"AWS_WEB_IDENTITY_TOKEN_FILE",
		"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI",
		"AWS_CONTAINER_CREDENTIALS_FULL_URI",
		"AWS_MAX_ATTEMPTS",
		"AWS_RETRY_MODE",
	} {
		t.Setenv(name, "")
		require.NoError(t, os.Unsetenv(name))
	}
}
