package sqs_test

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/queue/sqs"
)

// call is one request a fakeSQS received.
type call struct {
	// body is the request's JSON body, decoded.
	body map[string]any
	// op is the operation, such as SendMessage.
	op string
}

// reply is what a fakeSQS answers a request with.
type reply struct {
	// header holds extra response headers.
	header map[string]string
	// body is the response body.
	body string
	// status is the HTTP status; 0 is 200.
	status int
}

// fakeSQS is an HTTP server that speaks SQS's JSON protocol, answering
// each operation as its handler says. It is safe for concurrent use.
type fakeSQS struct {
	// handle answers a request; nil answers as ok does.
	handle func(c call) reply
	// srv is the server.
	srv *httptest.Server
	// calls are the requests received, in order.
	calls []call
	// opened and closed count the connections the server got and closed.
	opened, closed int
	// mu guards calls, handle, opened and closed.
	mu sync.Mutex
}

// newFake returns a fake SQS server answering with handle, closed when
// the test ends. A nil handle answers every request as ok does.
func newFake(t *testing.T, handle func(c call) reply) *fakeSQS {
	t.Helper()

	f := &fakeSQS{handle: handle}

	f.srv = httptest.NewUnstartedServer(http.HandlerFunc(f.serve))
	f.srv.Config.ConnState = f.track
	f.srv.Start()
	t.Cleanup(f.srv.Close)

	return f
}

// track counts the connections opened and closed.
func (f *fakeSQS) track(_ net.Conn, state http.ConnState) {
	f.mu.Lock()
	defer f.mu.Unlock()

	switch state {
	case http.StateNew:
		f.opened++
	case http.StateClosed, http.StateHijacked:
		f.closed++
	case http.StateActive, http.StateIdle:
	}
}

// openConns returns how many connections are open.
func (f *fakeSQS) openConns() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.opened - f.closed
}

// serve records a request and writes its reply.
func (f *fakeSQS) serve(w http.ResponseWriter, r *http.Request) {
	b, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)

		return
	}

	c := call{
		op:   strings.TrimPrefix(r.Header.Get("X-Amz-Target"), "AmazonSQS."),
		body: map[string]any{},
	}

	_ = json.Unmarshal(b, &c.body)

	f.mu.Lock()

	f.calls = append(f.calls, c)

	handle := f.handle
	f.mu.Unlock()

	rep := ok(c)
	if handle != nil {
		rep = handle(c)
	}

	for k, v := range rep.header {
		w.Header().Set(k, v)
	}

	w.Header().Set("Content-Type", "application/x-amz-json-1.0")

	if rep.status != 0 {
		w.WriteHeader(rep.status)
	}

	_, _ = io.WriteString(w, rep.body)
}

// ok answers a request with success: a URL for GetQueueUrl, no messages
// for ReceiveMessage, an ID for SendMessage, and an empty object else.
func ok(c call) reply {
	switch c.op {
	case "GetQueueUrl":
		return reply{
			body: `{"QueueUrl":"https://sqs.example/000000000000/` + c.body["QueueName"].(string) + `"}`,
		}
	case "ReceiveMessage":
		return reply{body: `{"Messages":[]}`}
	case "SendMessage":
		return reply{body: `{"MessageId":"m1"}`}
	default:
		return reply{body: `{}`}
	}
}

// awsError returns an error reply with code, as the type of the body,
// the query code, when it is not empty, in the X-Amzn-Query-Error header,
// and message.
func awsError(status int, code, queryCode, message string) reply {
	r := reply{
		status: status,
		body:   `{"__type":"com.amazonaws.sqs#` + code + `","message":` + quote(message) + `}`,
	}

	if queryCode != "" {
		r.header = map[string]string{"X-Amzn-Query-Error": queryCode + ";Sender"}
	}

	return r
}

// quote returns s, which is ASCII without `"` or `\`, as a JSON
// string.
func quote(s string) string { return `"` + s + `"` }

// setHandle replaces the handler.
func (f *fakeSQS) setHandle(handle func(c call) reply) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.handle = handle
}

// received returns a copy of the requests of op, or of every request
// when op is empty.
func (f *fakeSQS) received(op string) []call {
	f.mu.Lock()
	defer f.mu.Unlock()

	if op == "" {
		return slices.Clone(f.calls)
	}

	var out []call

	for _, c := range f.calls {
		if c.op == op {
			out = append(out, c)
		}
	}

	return out
}

// config returns a configuration pointed at the fake server, with the
// test region and static credentials.
func (f *fakeSQS) config() *sqs.Config { return config(f.srv.URL) }

// open returns a store on the fake server, with the settings of its
// configuration changed by edit, initialized and closed when the test
// ends.
func (f *fakeSQS) open(t *testing.T, edit func(*sqs.Settings)) *sqs.Service {
	t.Helper()

	cfg := f.config()
	if edit != nil {
		edit(&cfg.Queue)
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
		"AWS_ENDPOINT_URL_SQS",
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
