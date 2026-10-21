package projectknownissues_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/gasmod/gas"
	gaslog "github.com/gasmod/gas/log"
)

// repoRoot is the directory holding go.work.
func repoRoot(t *testing.T) string {
	t.Helper()
	d, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(d, "go.work")); err == nil {
			return d
		}
		if filepath.Dir(d) == d {
			t.Fatal("go.work not found")
		}
		d = filepath.Dir(d)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// panicValue runs fn and returns what it panicked with, or nil.
func panicValue(fn func()) (v any) {
	defer func() { v = recover() }()
	fn()
	return nil
}

// panicText runs fn and returns the text of its panic, or "" when it returns.
func panicText(fn func()) string {
	v := panicValue(fn)
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

func startApp(t *testing.T, app *gas.App) {
	t.Helper()
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
}

// logSink captures JSON log lines.
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

// newSink returns a capturing sink and a gas.Logger constructor writing JSON
// lines to it at debug level and above.
func newSink() (*logSink, gaslog.SlogLoggerCtor) {
	sink := &logSink{}
	l := slog.New(slog.NewJSONHandler(sink, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return sink, gaslog.NewSlogLogger(gaslog.WithSlogInstance(l))
}

func nopLogger() gas.Logger { return &gas.NopLogger{} }
