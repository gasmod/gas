package main

import (
	"context"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config"
	templatefs "github.com/gasmod/gas/template/fs"
	"github.com/gasmod/gas/ui"
	"github.com/gasmod/gas/ui/uitest"
)

// TestPagesEndToEnd starts the app wired as on the page and checks each
// route through App.Handler.
func TestPagesEndToEnd(t *testing.T) {
	t.Parallel()
	templates, err := fs.Sub(assets, "templates")
	if err != nil {
		t.Fatal(err)
	}
	static, err := fs.Sub(assets, "static")
	if err != nil {
		t.Fatal(err)
	}
	app := gas.NewApp(
		gas.WithServiceInstance[gas.ConfigProvider](config.New()),
		gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
		gas.WithSingletonService[gas.TemplateProvider](templatefs.NewStore(templates)),
		gas.WithSingletonService[gas.UIProvider](ui.New[gas.TemplateProvider](ui.WithStaticFS(static))),
		gas.WithSingletonService[*Pages](NewPages),
		gas.WithSingletonService[*Markdown](NewMarkdown),
	)
	if err := app.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		if err := app.Shutdown(context.Background()); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
	})

	tests := []struct {
		name, path string
		htmx       bool
		status     int
		want       []string
		notWant    []string
	}{
		{name: "page", path: "/", status: 200, want: []string{"<title>Home</title>", "<main><h1>Hello, visitor</h1></main>", `<nav>`}},
		{name: "full list", path: "/notes", status: 200, want: []string{`<main><ul id="notes"><li>first</li>`}},
		{name: "fragment", path: "/notes", htmx: true, status: 200, want: []string{"<li>second</li>"}, notWant: []string{"<main>", "<nav>"}},
		{name: "not found", path: "/missing", status: 404, want: []string{"<h1>Page not found</h1>"}},
		{name: "static file", path: "/static/app.css", status: 200, want: []string{"font-family"}},
		{name: "static listing", path: "/static/", status: 404},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			if tt.htmx {
				req.Header.Set("HX-Request", "true")
			}
			rec := httptest.NewRecorder()
			app.Handler().ServeHTTP(rec, req)

			body := rec.Body.String()
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d; body %q", rec.Code, tt.status, body)
			}
			for _, w := range tt.want {
				if !strings.Contains(body, w) {
					t.Errorf("body %q does not contain %q", body, w)
				}
			}
			for _, w := range tt.notWant {
				if strings.Contains(body, w) {
					t.Errorf("body %q contains %q", body, w)
				}
			}
		})
	}
}

// #region test
func TestHomeRendersHomePage(t *testing.T) {
	t.Parallel()
	mock := &uitest.MockUI{}
	p := &Pages{ui: mock}

	rec := httptest.NewRecorder()
	ctx := gas.NewContext(context.Background(), rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if err := p.home(ctx); err != nil {
		t.Fatalf("home: %v", err)
	}

	if got := mock.CallCount("Render"); got != 1 {
		t.Fatalf("Render calls = %d, want 1", got)
	}
	if name := mock.Calls[0].Args[0]; name != "home" {
		t.Fatalf("rendered %v, want home", name)
	}
}

// #endregion test
