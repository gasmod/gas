package main

import (
	"context"
	"testing"
	"time"

	"github.com/gasmod/gas/config/configtest"
)

// #region testing
// NewMockConfigWithValues delegates to a real config seeded with the given
// values, without reading the process environment. Seed nested maps, not
// dotted keys.
func TestNotesBindsPageSize(t *testing.T) {
	cfg, err := configtest.NewMockConfigWithValues(map[string]any{
		"notes": map[string]any{"pagesize": 20, "timeout": "1s"},
	})
	if err != nil {
		t.Fatal(err)
	}

	notes, err := NewNotes(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := notes.Init(context.Background()); err != nil {
		t.Fatal(err)
	}

	if notes.cfg.Notes.PageSize != 20 || notes.cfg.Notes.Timeout != time.Second {
		t.Fatalf("got %+v", notes.cfg.Notes)
	}
	if cfg.CallCount("Bind") != 1 {
		t.Fatalf("Bind called %d times, want 1", cfg.CallCount("Bind"))
	}
}

// #endregion testing
