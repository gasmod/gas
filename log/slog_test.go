//nolint:revive // intentional package name
package log

import (
	"bytes"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/gasmod/gas"
)

// newBufferedSlog returns a SlogLogger at debug level that writes JSON lines
// to the returned buffer.
func newBufferedSlog(t *testing.T) (*SlogLogger, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	h := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	l, err := NewSlogLogger(WithSlogInstance(slog.New(h)))()
	if err != nil {
		t.Fatal(err)
	}
	return l, &buf
}

// TestSlogLogger_Levels checks how each gas level maps onto slog's levels.
// slog has no trace level, so Trace logs at Debug.
func TestSlogLogger_Levels(t *testing.T) {
	t.Parallel()

	l, buf := newBufferedSlog(t)
	tests := []struct {
		emit func(string) gas.LogEvent
		msg  string
		want string
	}{
		{emit: l.Trace, msg: "trace", want: "DEBUG"},
		{emit: l.Debug, msg: "debug", want: "DEBUG"},
		{emit: l.Info, msg: "info", want: "INFO"},
		{emit: l.Warn, msg: "warn", want: "WARN"},
		{emit: l.Error, msg: "error", want: "ERROR"},
	}
	for _, tt := range tests {
		tt.emit(tt.msg).Send()
	}

	lines := decodeLines(t, buf)
	if len(lines) != len(tests) {
		t.Fatalf("got %d lines, want %d: %s", len(lines), len(tests), buf)
	}
	for i, tt := range tests {
		if got := lines[i]["level"]; got != tt.want {
			t.Errorf("%s: level = %v, want %s", tt.msg, got, tt.want)
		}
		if got := lines[i]["msg"]; got != tt.msg {
			t.Errorf("%s: msg = %v, want %s", tt.msg, got, tt.msg)
		}
	}
}

func TestSlogLogger_EventFields(t *testing.T) {
	t.Parallel()

	l, buf := newBufferedSlog(t)
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
	want := map[string]any{
		"s":   "v",
		"i":   1.0,
		"i64": 2.0,
		"f":   1.5,
		"b":   true,
		"err": "boom",
		// slog's JSON handler writes durations in nanoseconds.
		"d":   float64(1500 * time.Millisecond),
		"any": map[string]any{"k": 3.0},
	}
	for k, v := range want {
		if !jsonEqual(lines[0][k], v) {
			t.Errorf("field %q = %#v, want %#v", k, lines[0][k], v)
		}
	}
}

func TestSlogLogger_WithBakesFieldsIntoSubLogger(t *testing.T) {
	t.Parallel()

	l, buf := newBufferedSlog(t)
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
		"err": "ctx-err", "d": float64(time.Second), "any": []any{1.0},
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
