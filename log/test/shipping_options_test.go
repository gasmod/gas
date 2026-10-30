package log_test

import (
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/log"
)

// The constructors check the Marshaler, then the endpoint, then the
// options in the documented order, and both shipping constructors check
// the same.
func TestNewShipping_CheckOrder(t *testing.T) {
	t.Parallel()

	bad := []log.ShippingOption{
		log.WithHeader("Bad Name", "v"),
		log.WithName(""),
		log.WithErrorHandler(nil),
		log.WithLocalHandler(nil),
		log.WithLevel(nil),
		log.WithHTTPClient(nil),
		log.WithFlushInterval(-time.Second),
		log.WithQueueSize(0),
		log.WithBatchSize(-1),
	}

	_, err := log.NewShippingHandler("not a url", nil, bad...)
	require.ErrorIs(t, err, log.ErrNilMarshaler)

	_, err = log.NewShippingLogger("not a url", &capture{}, bad...)()
	require.ErrorIs(t, err, log.ErrInvalidEndpoint)

	for len(bad) > 0 {
		_, err = log.NewShippingLogger(endpoint, &capture{}, bad...)()
		require.ErrorIs(t, err, log.ErrInvalidOption)

		_, herr := log.NewShippingHandler(endpoint, &capture{}, bad...)
		require.EqualError(t, herr, err.Error())

		bad = bad[:len(bad)-1]
	}

	l, err := log.NewShippingLogger(endpoint, &capture{})()
	require.NoError(t, err)
	assert.NotNil(t, l)
}

// A WithHeader name that is not an RFC 9110 token, or a value with a
// control character other than a horizontal tab, fails the constructor
// with ErrInvalidOption naming the option and neither the name nor the
// value. A tab and bytes above 0x7f are allowed in a value.
func TestWithHeader_Invalid(t *testing.T) {
	t.Parallel()

	const (
		badName  = "gas/log: invalid option: WithHeader requires a token as the name"
		badValue = "gas/log: invalid option: WithHeader requires a value without control characters"
	)

	for _, tc := range []struct {
		key, value, want string
	}{
		{key: "", value: "v", want: badName},
		{key: "X Secret", value: "v", want: badName},
		{key: "X-Secret:", value: "v", want: badName},
		{key: "X-Secret\r\nX-Injected", value: "v", want: badName},
		{key: "X-Secrét", value: "v", want: badName},
		{key: "Authorization", value: "Bearer secret\r\nX-Injected: 1", want: badValue},
		{key: "Authorization", value: "Bearer secret\n", want: badValue},
		{key: "Authorization", value: "Bearer secret\x00", want: badValue},
		{key: "Authorization", value: "Bearer secret\x7f", want: badValue},
	} {
		_, err := log.NewShippingHandler(endpoint, &capture{}, log.WithHeader(tc.key, tc.value))
		require.ErrorIs(t, err, log.ErrInvalidOption, "%q: %q", tc.key, tc.value)
		require.EqualError(t, err, tc.want)
		assert.NotContains(t, err.Error(), "secret")
		assert.NotContains(t, err.Error(), "Secret")
	}

	srv := newCollector(t)
	h, err := log.NewShippingHandler(srv.srv.URL, &capture{}, quiet(
		log.WithHeader("X-Api_Key.v1!#$%&'*+^`|~", "a\tb \xff"),
	)...)
	require.NoError(t, err)

	slog.New(h).Info("x")
	require.NoError(t, h.Close(t.Context()))

	srv.mu.Lock()
	defer srv.mu.Unlock()

	require.Len(t, srv.headers, 1)
	assert.Equal(t, "a\tb \xff", srv.headers[0].Get("X-Api_key.v1!#$%&'*+^`|~"))
}

// WithoutLocalHandler wins over WithLocalHandler, in either order.
func TestWithoutLocalHandler_Wins(t *testing.T) {
	t.Parallel()

	var buf lockedBuffer

	local := slog.NewJSONHandler(&buf, nil)

	for _, opts := range [][]log.ShippingOption{
		{log.WithLocalHandler(local), log.WithoutLocalHandler()},
		{log.WithoutLocalHandler(), log.WithLocalHandler(local)},
	} {
		h := shippingHandler(t, &capture{}, append(opts, log.WithLevel(slog.LevelError))...)
		slog.New(h).Error("x")
		assert.False(t, h.Enabled(t.Context(), slog.LevelInfo))
	}

	assert.Empty(t, buf.lines(t))
}

// An endpoint that is not an absolute http or https URL fails with
// ErrInvalidEndpoint, and the error never quotes it.
func TestNewShippingHandler_InvalidEndpoint(t *testing.T) {
	t.Parallel()

	for _, ep := range []string{
		"",
		"collector.example/v1/logs",
		"/v1/logs",
		"ftp://collector.example/v1/logs",
		"http:///v1/logs",
		"http://user:hunter2@[::1/v1/logs",
		"http://127.0.0.1:1/\x7f?api_key=hunter2",
		"mailto:hunter2@example.com",
	} {
		_, err := log.NewShippingHandler(ep, &capture{})
		require.ErrorIs(t, err, log.ErrInvalidEndpoint, ep)
		require.EqualError(
			t,
			err,
			"gas/log: invalid endpoint: must be an absolute http or https URL",
		)
	}
}

// A token in the endpoint's query or user info, or a header value, never
// appears in a report, whether the post fails on the network or with a
// status, or in Close's error.
func TestShipping_NoSecrets(t *testing.T) {
	t.Parallel()

	for _, ep := range []string{
		"http://user:hunter2@127.0.0.1:1/v1/logs",
		"http://hunter2@127.0.0.1:1/v1/logs",
		"http://127.0.0.1:1/v1/logs?api_key=hunter2",
		"http://127.0.0.1:1/v1/logs#hunter2",
	} {
		for _, client := range []*http.Client{
			{Timeout: time.Second},
			status(http.StatusUnauthorized),
			failing(errRefused),
		} {
			r := newReports()

			h, err := log.NewShippingHandler(ep, &capture{},
				quiet(log.WithHTTPClient(client), log.WithHeader("Authorization", "Bearer hunter2"),
					log.WithErrorHandler(r.handler))...)
			require.NoError(t, err)

			slog.New(h).Info("x")

			closeErr := h.Close(t.Context())
			require.Error(t, closeErr)

			report := r.next(t)
			assert.NotContains(t, report.Error(), "hunter2")
			assert.NotContains(t, closeErr.Error(), "hunter2")
			assert.Contains(t, report.Error(), "127.0.0.1:1/v1/logs")
		}
	}
}

// errorShape is what an endpoint error looks like; it holds no input.
var errorShape = regexp.MustCompile(
	`^gas/log: invalid endpoint: must be an absolute http or https URL$`,
)

// FuzzEndpoint checks that the endpoint check never panics, that a
// rejected endpoint gets the one fixed error, and that an accepted one
// posts to the endpoint and is named in a report without its user info,
// query or fragment.
func FuzzEndpoint(f *testing.F) {
	for _, seed := range []string{
		"https://collector.example:4318/v1/logs",
		"http://user:hunter2@host/p?api_key=hunter2#frag",
		"http://[::1/x",
		"ftp://x",
		"",
		"http://h/\x7f",
		"HTTP://Host:80",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, ep string) {
		var posted string

		client := clientOf(func(r *http.Request) (*http.Response, error) {
			posted = r.URL.String()

			return nil, errFake
		})
		r := newReports()

		h, err := log.NewShippingHandler(ep, &capture{},
			quiet(log.WithHTTPClient(client), log.WithErrorHandler(r.handler))...)
		if err != nil {
			require.ErrorIs(t, err, log.ErrInvalidEndpoint)
			require.Regexp(t, errorShape, err.Error())

			return
		}

		slog.New(h).Info("x")
		require.Error(t, h.Close(t.Context()))
		require.NotEmpty(t, posted)

		report := r.next(t)
		require.ErrorIs(t, report, log.ErrPost)

		if i := strings.IndexAny(
			ep,
			"?#",
		); i >= 0 && len(ep[i:]) > 1 &&
			!strings.Contains(ep[:i], ep[i+1:]) {
			require.NotContains(t, report.Error(), ep[i:])
		}
	})
}
