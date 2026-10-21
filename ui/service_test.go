package ui_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/ui"
)

// newServiceForTest returns a Service over files. It runs Init unless skipInit
// is set.
func newServiceForTest(t *testing.T, files map[string]string, skipInit bool) *ui.Service {
	t.Helper()
	svc, err := ui.New[*testProvider](ui.WithConfig(ui.DefaultConfig()))(
		newTestProvider(files), nil, nil, &gas.NopLogger{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !skipInit {
		if err := svc.Init(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	return svc
}

var serviceRenderFiles = map[string]string{
	"layouts/base.html": `{{define "base"}}<!DOCTYPE html>{{block "content" .}}{{end}}{{end}}`,
	"teapot.html":       `{{define "content"}}<h1>{{.}}</h1>{{end}}`,
	"card.html":         `<div class="card">{{.}}</div>`,
}

func TestService_RenderWithStatus(t *testing.T) {
	svc := newServiceForTest(t, serviceRenderFiles, false)

	w := httptest.NewRecorder()
	if err := svc.RenderWithStatus(w, http.StatusTeapot, "teapot", "short and stout"); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusTeapot {
		t.Errorf("status = %d, want %d", w.Code, http.StatusTeapot)
	}
	if body := w.Body.String(); !strings.Contains(body, "<!DOCTYPE html>") ||
		!strings.Contains(body, "<h1>short and stout</h1>") {
		t.Errorf("body = %q, want the page inside the layout", body)
	}
}

// TestService_RenderFragment renders a page with top-level content, which
// RenderFragment writes without the layout.
func TestService_RenderFragment(t *testing.T) {
	svc := newServiceForTest(t, serviceRenderFiles, false)

	w := httptest.NewRecorder()
	if err := svc.RenderFragment(w, "card", "Item"); err != nil {
		t.Fatal(err)
	}
	body := w.Body.String()
	if strings.Contains(body, "<!DOCTYPE html>") {
		t.Errorf("fragment includes the layout: %q", body)
	}
	if !strings.Contains(body, `<div class="card">Item</div>`) {
		t.Errorf("body = %q, want the card", body)
	}
}

// renderMethods calls each Service render method on the same page.
var renderMethods = []struct {
	render func(*ui.Service, http.ResponseWriter) error
	name   string
}{
	{name: "Render", render: func(s *ui.Service, w http.ResponseWriter) error {
		return s.Render(w, "card", nil)
	}},
	{name: "RenderWithStatus", render: func(s *ui.Service, w http.ResponseWriter) error {
		return s.RenderWithStatus(w, http.StatusOK, "card", nil)
	}},
	{name: "RenderFragment", render: func(s *ui.Service, w http.ResponseWriter) error {
		return s.RenderFragment(w, "card", nil)
	}},
}

// TestService_RenderMethodsAfterClose checks that every render method answers
// 503 and returns an error once the service is closed.
func TestService_RenderMethodsAfterClose(t *testing.T) {
	for _, m := range renderMethods {
		t.Run(m.name, func(t *testing.T) {
			svc := newServiceForTest(t, serviceRenderFiles, false)
			if err := svc.Close(context.Background()); err != nil {
				t.Fatal(err)
			}

			w := httptest.NewRecorder()
			if err := m.render(svc, w); err == nil {
				t.Error("error = nil, want an error after Close")
			}
			if w.Code != http.StatusServiceUnavailable {
				t.Errorf("status = %d, want 503", w.Code)
			}
		})
	}
}

// TestService_RenderMethodsBeforeInit checks that every render method returns
// an error, and writes nothing, before Init has built the engine.
func TestService_RenderMethodsBeforeInit(t *testing.T) {
	for _, m := range renderMethods {
		t.Run(m.name, func(t *testing.T) {
			svc := newServiceForTest(t, serviceRenderFiles, true)

			w := httptest.NewRecorder()
			err := m.render(svc, w)
			if err == nil || !strings.Contains(err.Error(), "no template engine") {
				t.Errorf("error = %v, want a no template engine error", err)
			}
			if w.Body.Len() != 0 {
				t.Errorf("body = %q, want nothing written", w.Body.String())
			}
		})
	}
}

func TestService_Engine(t *testing.T) {
	svc := newServiceForTest(t, serviceRenderFiles, true)
	if svc.Engine() != nil {
		t.Fatal("Engine() before Init is not nil")
	}

	if err := svc.Init(t.Context()); err != nil {
		t.Fatal(err)
	}
	e := svc.Engine()
	if e == nil {
		t.Fatal("Engine() after Init is nil")
	}

	// The returned engine is the one the service renders with.
	w := httptest.NewRecorder()
	if err := e.RenderFragment(w, "card", "direct"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(w.Body.String(), `<div class="card">direct</div>`) {
		t.Errorf("Engine().RenderFragment body = %q", w.Body.String())
	}
}
