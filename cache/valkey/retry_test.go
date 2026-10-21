package valkey

import (
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/gasmod/gas"
)

// TestInit_RetryBackoffDoublesAndStops checks that a failing connection is
// retried exactly ConnRetries times, with the wait doubling from
// ConnRetryInterval before each retry. Nothing listens on the address, so
// every attempt fails.
func TestInit_RetryBackoffDoublesAndStops(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig()
	cfg.Cache.Addr = "127.0.0.1:1"
	cfg.Cache.DialTimeout = 50 * time.Millisecond
	cfg.Cache.ConnRetries = 3
	cfg.Cache.ConnRetryInterval = time.Millisecond

	logger := &retryLogger{}
	s, err := New(WithConfig(cfg))(nil, logger)
	if err != nil {
		t.Fatal(err)
	}

	if err := s.Init(t.Context()); err == nil {
		t.Fatal("Init succeeded, want an error after exhausting retries")
	}
	if got, want := logger.recordedWaits(), []string{
		"1ms",
		"2ms",
		"4ms",
	}; !slices.Equal(
		got,
		want,
	) {
		t.Errorf("retry waits = %v, want %v", got, want)
	}
}

// retryLogger records the next_retry_in field of every Warn event, which the
// connection retry loop logs before each wait.
type retryLogger struct {
	gas.NopLogger
	waits []string
	mu    sync.Mutex
}

func (l *retryLogger) Warn(string) gas.LogEvent { return &retryEvent{logger: l} }

// With keeps the recording logger for the sub-logger the service derives.
func (l *retryLogger) With() gas.LoggerContext { return &retryLoggerContext{logger: l} }

func (l *retryLogger) recordedWaits() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.waits)
}

type retryEvent struct {
	logger *retryLogger
	wait   string
}

func (e *retryEvent) Str(key, val string) gas.LogEvent {
	if key == "next_retry_in" {
		e.wait = val
	}
	return e
}
func (e *retryEvent) Int(string, int) gas.LogEvent                { return e }
func (e *retryEvent) Int64(string, int64) gas.LogEvent            { return e }
func (e *retryEvent) Float64(string, float64) gas.LogEvent        { return e }
func (e *retryEvent) Bool(string, bool) gas.LogEvent              { return e }
func (e *retryEvent) Err(string, error) gas.LogEvent              { return e }
func (e *retryEvent) Duration(string, time.Duration) gas.LogEvent { return e }
func (e *retryEvent) Any(string, any) gas.LogEvent                { return e }

func (e *retryEvent) Send() {
	if e.wait == "" {
		return
	}
	e.logger.mu.Lock()
	defer e.logger.mu.Unlock()
	e.logger.waits = append(e.logger.waits, e.wait)
}

type retryLoggerContext struct{ logger *retryLogger }

func (c *retryLoggerContext) Str(string, string) gas.LoggerContext             { return c }
func (c *retryLoggerContext) Int(string, int) gas.LoggerContext                { return c }
func (c *retryLoggerContext) Int64(string, int64) gas.LoggerContext            { return c }
func (c *retryLoggerContext) Float64(string, float64) gas.LoggerContext        { return c }
func (c *retryLoggerContext) Bool(string, bool) gas.LoggerContext              { return c }
func (c *retryLoggerContext) Err(string, error) gas.LoggerContext              { return c }
func (c *retryLoggerContext) Duration(string, time.Duration) gas.LoggerContext { return c }
func (c *retryLoggerContext) Any(string, any) gas.LoggerContext                { return c }
func (c *retryLoggerContext) Logger() gas.Logger                               { return c.logger }
