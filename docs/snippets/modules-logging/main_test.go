package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/gasmod/gas"
	gaslog "github.com/gasmod/gas/log"
)

// #region testing
// Capture output by giving the slog backend a handler over a buffer. Where
// output does not matter, &gas.NopLogger{} discards everything.
func TestFluentLogsNoteCreated(t *testing.T) {
	var buf bytes.Buffer
	h := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	logger, err := gaslog.NewSlogLogger(gaslog.WithSlogInstance(slog.New(h)))()
	if err != nil {
		t.Fatal(err)
	}

	fluent(logger)

	if !strings.Contains(buf.String(), `"msg":"note created"`) {
		t.Fatalf("log output %q lacks the record", buf.String())
	}
	fluent(&gas.NopLogger{})
}

// #endregion testing
