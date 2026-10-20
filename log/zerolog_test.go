//nolint:revive // intentional package name
package log

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/gasmod/gas"
)

// newBufferedZeroLog returns a ZeroLogLogger at trace level that writes JSON
// lines to the returned buffer.
func newBufferedZeroLog(t *testing.T) (*ZeroLogLogger, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	zl := zerolog.New(&buf).Level(zerolog.TraceLevel)
	l, err := NewZeroLogLogger(WithZeroLogInstance(&zl))()
	if err != nil {
		t.Fatal(err)
	}
	return l, &buf
}

// decodeLines parses each JSON line in buf.
func decodeLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for line := range strings.Lines(buf.String()) {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("decode %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

func TestZeroLogLogger_Levels(t *testing.T) {
	t.Parallel()

	l, buf := newBufferedZeroLog(t)
	tests := []struct {
		emit func(string) gas.LogEvent
		want string
	}{
		{emit: l.Trace, want: "trace"},
		{emit: l.Debug, want: "debug"},
		{emit: l.Info, want: "info"},
		{emit: l.Warn, want: "warn"},
		{emit: l.Error, want: "error"},
	}
	for _, tt := range tests {
		tt.emit("msg-" + tt.want).Send()
	}

	lines := decodeLines(t, buf)
	if len(lines) != len(tests) {
		t.Fatalf("got %d lines, want %d: %s", len(lines), len(tests), buf)
	}
	for i, tt := range tests {
		if got := lines[i]["level"]; got != tt.want {
			t.Errorf("line %d level = %v, want %s", i, got, tt.want)
		}
		if got := lines[i]["message"]; got != "msg-"+tt.want {
			t.Errorf("line %d message = %v, want msg-%s", i, got, tt.want)
		}
	}
}

func TestZeroLogLogger_EventFields(t *testing.T) {
	t.Parallel()

	l, buf := newBufferedZeroLog(t)
	l.Info("fields").
		Str("s", "v").
		Int("i", 1).
		Int64("i64", 2).
		Float64("f", 1.5).
		Bool("b", true).
		Err("err", errors.New("boom")).
		Duration("d", 1500*time.Millisecond).
		Any("any", map[string]int{"k": 3}).
		Send()

	lines := decodeLines(t, buf)
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1", len(lines))
	}
	got := lines[0]
	want := map[string]any{
		"s":   "v",
		"i":   1.0,
		"i64": 2.0,
		"f":   1.5,
		"b":   true,
		"err": "boom",
		// zerolog writes durations in milliseconds by default.
		"d":   1500.0,
		"any": map[string]any{"k": 3.0},
	}
	for k, v := range want {
		if !jsonEqual(got[k], v) {
			t.Errorf("field %q = %#v, want %#v", k, got[k], v)
		}
	}
}

func TestZeroLogLogger_WithBakesFieldsIntoSubLogger(t *testing.T) {
	t.Parallel()

	l, buf := newBufferedZeroLog(t)
	sub := l.With().
		Str("svc", "api").
		Int("i", 1).
		Int64("i64", 2).
		Float64("f", 0.5).
		Bool("b", true).
		Err("err", errors.New("ctx-err")).
		Duration("d", time.Second).
		Any("any", []int{1}).
		Logger()

	sub.Info("from sub").Send()
	l.Info("from parent").Send()

	lines := decodeLines(t, buf)
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}
	want := map[string]any{
		"svc": "api", "i": 1.0, "i64": 2.0, "f": 0.5, "b": true,
		"err": "ctx-err", "d": 1000.0, "any": []any{1.0},
	}
	for k, v := range want {
		if !jsonEqual(lines[0][k], v) {
			t.Errorf("sub-logger field %q = %#v, want %#v", k, lines[0][k], v)
		}
	}
	if _, ok := lines[1]["svc"]; ok {
		t.Errorf("parent logger got the sub-logger's field: %v", lines[1])
	}
}

func TestNewZeroLogLogger_DefaultsToGlobalLogger(t *testing.T) {
	t.Parallel()

	l, err := NewZeroLogLogger()()
	if err != nil {
		t.Fatal(err)
	}
	if l.logger == nil {
		t.Fatal("default logger is nil")
	}
	l.Flush() // a no-op; must not panic
}

// jsonEqual compares two decoded JSON values.
func jsonEqual(a, b any) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(ja, jb)
}
