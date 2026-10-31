package log

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"
)

// The shipping defaults.
const (
	// DefaultShippingName is the gas.Service name of a ShippingLogger.
	DefaultShippingName = "gas/log-shipping"
	// DefaultBatchSize is the most records in one request.
	DefaultBatchSize = 100
	// DefaultQueueSize is the most records waiting to be sent.
	DefaultQueueSize = 1024
	// DefaultFlushInterval is the longest a record waits for its batch to
	// fill, and the shortest time between two error reports of a class.
	DefaultFlushInterval = 2 * time.Second
	// DefaultHTTPTimeout is the timeout of the default HTTP client.
	DefaultHTTPTimeout = 10 * time.Second
	// DefaultFlushTimeout bounds ShippingLogger.Flush.
	DefaultFlushTimeout = 10 * time.Second
	// DefaultShippingLevel is the lowest level shipped.
	DefaultShippingLevel = slog.LevelInfo
)

// maxDiscard is the most of a response body read, and discarded, so the
// connection can be reused.
const maxDiscard = 64 << 10

// ShippingOption configures a ShippingLogger or a ShippingHandler. Options
// only record their value; the constructor checks them.
type ShippingOption func(*shippingConfig)

// shippingConfig holds what the options set.
type shippingConfig struct {
	// local is the handler given to WithLocalHandler.
	local slog.Handler
	// level is the lowest level shipped.
	level slog.Leveler
	// client is the HTTP client.
	client *http.Client
	// onError receives the error reports.
	onError func(error)
	// name is the gas.Service name.
	name string
	// headers are the headers given to WithHeader, in order.
	headers []header
	// batchSize, queueSize and flushInterval are as their options say.
	batchSize     int
	queueSize     int
	flushInterval time.Duration
	// localSet, onErrorSet and noLocal record which options were given, so
	// a nil value given to one is told apart from no option.
	localSet   bool
	onErrorSet bool
	noLocal    bool
}

// header is a header given to WithHeader.
type header struct {
	// key and value are as given.
	key, value string
}

// WithHeader adds a header sent with every request, such as an API key.
// Given again for a key, it replaces the value. It can replace the
// Content-Type the Marshaler names. The key must be a token and the value
// must hold no control character but a horizontal tab, as RFC 9110 says.
// Header names and values never appear in an error. It has no default.
func WithHeader(key, value string) ShippingOption {
	return func(c *shippingConfig) { c.headers = append(c.headers, header{key: key, value: value}) }
}

// WithBatchSize sets the most records in one request. It must be
// positive.
//
// Default: DefaultBatchSize.
func WithBatchSize(n int) ShippingOption {
	return func(c *shippingConfig) { c.batchSize = n }
}

// WithQueueSize sets the most records waiting to be sent; a record logged
// when the queue is full is dropped. It must be positive.
//
// Default: DefaultQueueSize.
func WithQueueSize(n int) ShippingOption {
	return func(c *shippingConfig) { c.queueSize = n }
}

// WithFlushInterval sets the longest a record waits for its batch to
// fill, which is also the shortest time between two error reports of a
// class. It must be positive.
//
// Default: DefaultFlushInterval.
func WithFlushInterval(d time.Duration) ShippingOption {
	return func(c *shippingConfig) { c.flushInterval = d }
}

// WithHTTPClient sets the HTTP client that posts the batches, used as it
// is, redirect policy included. It must not be nil.
//
// Default: a client with the timeout DefaultHTTPTimeout that follows no
// redirect, so a 3xx response is a failed post.
func WithHTTPClient(client *http.Client) ShippingOption {
	return func(c *shippingConfig) { c.client = client }
}

// WithLevel sets the lowest level shipped. It is read on every record, so
// a *slog.LevelVar changes it at run time. It must not be nil.
//
// Default: DefaultShippingLevel.
func WithLevel(level slog.Leveler) ShippingOption {
	return func(c *shippingConfig) { c.level = level }
}

// WithLocalHandler sets the handler that also receives every record it
// enables, before it is shipped. It must not be nil; WithoutLocalHandler
// turns local logging off.
//
// Default: a slog.JSONHandler on standard error at the shipping level.
func WithLocalHandler(h slog.Handler) ShippingOption {
	return func(c *shippingConfig) { c.local, c.localSet = h, true }
}

// WithoutLocalHandler turns local logging off, so records are only
// shipped. It wins over WithLocalHandler.
func WithoutLocalHandler() ShippingOption {
	return func(c *shippingConfig) { c.noLocal = true }
}

// WithErrorHandler sets the function that receives the error reports, as
// described in the package documentation. It must not be nil.
//
// Default: a function that writes the error and a newline to standard
// error.
func WithErrorHandler(fn func(error)) ShippingOption {
	return func(c *shippingConfig) { c.onError, c.onErrorSet = fn, true }
}

// WithName sets the gas.Service name of a ShippingLogger. Two shipping
// loggers in one app need distinct names. It must not be empty.
//
// Default: DefaultShippingName.
func WithName(name string) ShippingOption {
	return func(c *shippingConfig) { c.name = name }
}

// newShippingConfig returns the configuration with opts applied over the
// defaults, and checked, in the order the package documentation gives.
func newShippingConfig(opts []ShippingOption) (*shippingConfig, error) {
	c := &shippingConfig{
		client:        &http.Client{Timeout: DefaultHTTPTimeout, CheckRedirect: noRedirect},
		level:         DefaultShippingLevel,
		onError:       writeTo(os.Stderr),
		name:          DefaultShippingName,
		batchSize:     DefaultBatchSize,
		queueSize:     DefaultQueueSize,
		flushInterval: DefaultFlushInterval,
	}
	for _, opt := range opts {
		opt(c)
	}

	err := c.check()
	if err != nil {
		return nil, err
	}

	switch {
	case c.noLocal:
		c.local = nil
	case !c.localSet:
		c.local = slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: c.level})
	}

	return c, nil
}

// check returns the error for the first option, in a fixed order, that
// holds a value it does not accept.
//
//nolint:cyclop // one case per option.
func (c *shippingConfig) check() error {
	switch {
	case c.batchSize <= 0:
		return optionError("WithBatchSize", "must be positive")
	case c.queueSize <= 0:
		return optionError("WithQueueSize", "must be positive")
	case c.flushInterval <= 0:
		return optionError("WithFlushInterval", "must be positive")
	case c.client == nil:
		return optionError("WithHTTPClient", "requires a client")
	case c.level == nil:
		return optionError("WithLevel", "requires a level")
	case c.localSet && c.local == nil:
		return optionError("WithLocalHandler", "requires a handler")
	case c.onErrorSet && c.onError == nil:
		return optionError("WithErrorHandler", "requires a function")
	case c.name == "":
		return optionError("WithName", "requires a name")
	default:
		return checkHeaders(c.headers)
	}
}

// checkHeaders returns the error for the first header whose key is not a
// token or whose value holds a control character other than a horizontal
// tab, as RFC 9110 section 5 defines them. The error names neither.
func checkHeaders(headers []header) error {
	for _, h := range headers {
		if h.key == "" || strings.IndexFunc(h.key, notTokenChar) >= 0 {
			return optionError("WithHeader", "requires a token as the name")
		}

		if strings.IndexFunc(h.value, controlChar) >= 0 {
			return optionError("WithHeader", "requires a value without control characters")
		}
	}

	return nil
}

// notTokenChar reports whether r is not a tchar of RFC 9110 section 5.6.2.
func notTokenChar(r rune) bool {
	switch {
	case 'a' <= r && r <= 'z', 'A' <= r && r <= 'Z', '0' <= r && r <= '9':
		return false
	default:
		return !strings.ContainsRune("!#$%&'*+-.^_`|~", r)
	}
}

// controlChar reports whether r is a control character a field value of
// RFC 9110 section 5.5 may not hold: one below a space but a horizontal
// tab, or DEL.
func controlChar(r rune) bool {
	return (r < ' ' && r != '\t') || r == 0x7f
}

// noRedirect is the CheckRedirect of the default client: it follows no
// redirect, so a request's headers and body never reach another URL, and
// the 3xx response is returned as it is.
func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// parseEndpoint checks that endpoint is an absolute http or https URL, and
// returns the POST request every batch is cloned from, without a body,
// and the text errors name the endpoint by: its scheme and host only,
// without user info, path, query and fragment, any of which can hold a
// token. The error never holds the endpoint or the parser's error, which
// quotes it.
func parseEndpoint(endpoint string) (*http.Request, string, error) {
	invalid := fmt.Errorf(
		"%s: %w: must be an absolute http or https URL",
		errPrefix,
		ErrInvalidEndpoint,
	)

	req, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		endpoint,
		http.NoBody,
	)
	if err != nil || (req.URL.Scheme != "http" && req.URL.Scheme != "https") || req.URL.Host == "" {
		return nil, "", invalid
	}

	return req, req.URL.Scheme + "://" + req.URL.Host, nil
}

// writeTo returns the default error handler, writing to w, standard
// error as it is when the handler is made.
func writeTo(w io.Writer) func(error) {
	return func(err error) {
		// Standard error is the last resort; a failed write has nowhere to go.
		_, _ = fmt.Fprintln(w, err)
	}
}
