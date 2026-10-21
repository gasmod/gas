package main

import (
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gasmod/gas"
	templatefs "github.com/gasmod/gas/template/fs"
	"github.com/gasmod/gas/ui"
)

// TestFragmentTemplate renders templates/notes/list.html both ways and wants
// the notes in each: inside the layout from Render, alone from RenderFragment.
func TestFragmentTemplate(t *testing.T) {
	t.Parallel()
	store, err := templatefs.NewStore(os.DirFS("templates"))()
	if err != nil {
		t.Fatal(err)
	}
	engine := ui.NewEngine(store, nil, "", false, &gas.NopLogger{})
	data := map[string]any{"Notes": []string{"first", "second"}}

	page := httptest.NewRecorder()
	if err := engine.Render(page, "notes/list", data); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if body := page.Body.String(); !strings.Contains(body, "<main><ul id=\"notes\"><li>first</li>") {
		t.Fatalf("Render body = %q, want the notes inside the layout", body)
	}

	frag := httptest.NewRecorder()
	if err := engine.RenderFragment(frag, "notes/list", data); err != nil {
		t.Fatalf("RenderFragment: %v", err)
	}
	body := frag.Body.String()
	if !strings.Contains(body, "<li>second</li>") || strings.Contains(body, "<main>") {
		t.Fatalf("RenderFragment body = %q, want the notes without the layout", body)
	}
}
