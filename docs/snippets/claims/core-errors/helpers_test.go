package coreerrors_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gasmod/gas"
	gaslog "github.com/gasmod/gas/log"
)

// start starts app and stops it at the end of the test.
func start(t *testing.T, app *gas.App) {
	t.Helper()
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
}

func serve(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func get(h http.Handler, path string) *httptest.ResponseRecorder {
	return serve(h, httptest.NewRequest(http.MethodGet, path, nil))
}

// getAccept sends a GET with the given Accept header.
func getAccept(h http.Handler, path, accept string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	return serve(h, req)
}

// decode returns the error inside a JSON envelope body.
func decode(t *testing.T, rr *httptest.ResponseRecorder) *gas.Error {
	t.Helper()
	var body gas.ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil || body.Error == nil {
		t.Fatalf("body %q is not the error envelope: %v", rr.Body.String(), err)
	}
	return body.Error
}

// sameJSON reports whether two JSON documents decode to equal values.
func sameJSON(t *testing.T, got, want string) bool {
	t.Helper()
	var g, w any
	if err := json.Unmarshal([]byte(got), &g); err != nil {
		t.Fatalf("decode %q: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("decode want %q: %v", want, err)
	}
	gb, _ := json.Marshal(g)
	wb, _ := json.Marshal(w)
	return bytes.Equal(gb, wb)
}

// routeApp starts an App with a capturing logger registered and the given
// DI-aware GET routes.
func routeApp(t *testing.T, routes map[string]func(gas.Context) error, opts ...gas.Option) (*gas.App, *logSink) {
	t.Helper()
	sink, logger := newSink()
	app := gas.NewApp(append([]gas.Option{gas.WithSingletonService[gas.Logger](logger)}, opts...)...)
	for path, h := range routes {
		app.Router().Handle(nil, http.MethodGet, path, h)
	}
	start(t, app)
	return app, sink
}

// logSink captures JSON log lines written by a slog-backed gas.Logger.
type logSink struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *logSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *logSink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// records returns every captured line decoded as a JSON object.
func (s *logSink) records(t *testing.T) []map[string]any {
	t.Helper()
	var out []map[string]any
	sc := bufio.NewScanner(strings.NewReader(s.String()))
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("decode log line %q: %v", sc.Text(), err)
		}
		out = append(out, m)
	}
	return out
}

// mentioning returns the captured records whose rendering contains substr.
func (s *logSink) mentioning(t *testing.T, substr string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, r := range s.records(t) {
		raw, _ := json.Marshal(r)
		if strings.Contains(string(raw), substr) {
			out = append(out, r)
		}
	}
	return out
}

// newSink returns a capturing sink and a gas.Logger constructor writing JSON
// lines to it at debug level and above.
func newSink() (*logSink, gaslog.SlogLoggerCtor) {
	sink := &logSink{}
	l := slog.New(slog.NewJSONHandler(sink, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return sink, gaslog.NewSlogLogger(gaslog.WithSlogInstance(l))
}

// recordingHandler is an ErrorHandler that records each error it receives
// and writes nothing.
type recordingHandler struct {
	mu   sync.Mutex
	errs []error
}

func (h *recordingHandler) handle(_ gas.Context, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.errs = append(h.errs, err)
}

func (h *recordingHandler) seen() []error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]error(nil), h.errs...)
}
