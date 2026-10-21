// Package main shows server-rendered HTML with gas/ui.
package main

import (
	"context"
	"embed"
	"io/fs"
	"log"
	"net/http"
	"strings"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/config/providers"
	gaslog "github.com/gasmod/gas/log"
	templatefs "github.com/gasmod/gas/template/fs"
	"github.com/gasmod/gas/ui"
)

// #region embed

// assets holds the templates and static files, compiled into the binary.
//
//go:embed templates static
var assets embed.FS

// #endregion embed

func main() {
	cfg := config.New(config.WithProvider(providers.NewEnvProvider()))
	if err := cfg.Load(); err != nil {
		log.Fatal(err)
	}

	templates, err := fs.Sub(assets, "templates")
	if err != nil {
		log.Fatal(err)
	}
	static, err := fs.Sub(assets, "static")
	if err != nil {
		log.Fatal(err)
	}

	// #region wiring
	app := gas.NewApp(
		gas.WithServiceInstance[gas.ConfigProvider](cfg),
		gas.WithSingletonService[gas.Logger](gaslog.NewSlogLogger()),

		// Where templates come from: any gas.TemplateProvider.
		gas.WithSingletonService[gas.TemplateProvider](templatefs.NewStore(templates)),

		// What renders them. ui.New's type parameter is the type the
		// template store is registered under; the registration key is the
		// interface the app's services ask for.
		gas.WithSingletonService[gas.UIProvider](
			ui.New[gas.TemplateProvider](ui.WithStaticFS(static)),
		),

		gas.WithSingletonService[*Pages](NewPages),
	)
	// #endregion wiring

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

// #region pages
// Pages registers the HTML routes. It takes gas.UIProvider and never
// imports gas/ui.
type Pages struct {
	router *gas.Router
	ui     gas.UIProvider
}

func NewPages(router *gas.Router, ui gas.UIProvider) *Pages {
	return &Pages{router: router, ui: ui}
}

func (p *Pages) Name() string { return "pages" }

func (p *Pages) Init(context.Context) error {
	p.router.Get(p, "/", p.home)
	p.router.Get(p, "/notes", p.notes)
	p.router.NotFound(p, p.notFound)
	return nil
}

func (p *Pages) Close(context.Context) error { return nil }

// home renders templates/home.html inside the base layout.
func (p *Pages) home(ctx gas.Context) error {
	return p.ui.Render(ctx.ResponseWriter(), "home", map[string]any{"Name": "visitor"})
}

// notFound renders templates/404.html with a 404 status.
func (p *Pages) notFound(ctx gas.Context) error {
	return p.ui.RenderWithStatus(ctx.ResponseWriter(), http.StatusNotFound, "404", nil)
}

// #endregion pages

// #region fragment
// notes serves templates/notes/list.html two ways: the whole page to a
// browser, and only the list to an HTMX request.
func (p *Pages) notes(ctx gas.Context) error {
	data := map[string]any{"Notes": []string{"first", "second"}}

	if ctx.Request().Header.Get("HX-Request") == "true" {
		return p.ui.RenderFragment(ctx.ResponseWriter(), "notes/list", data)
	}
	return p.ui.Render(ctx.ResponseWriter(), "notes/list", data)
}

// #endregion fragment

// #region funcs
// Markdown contributes a template function. It depends on gas.UIProvider,
// so it starts after the ui service, whose engine is then ready to take
// functions.
type Markdown struct {
	ui gas.UIProvider
}

func NewMarkdown(ui gas.UIProvider) *Markdown { return &Markdown{ui: ui} }

func (m *Markdown) Name() string { return "markdown" }

func (m *Markdown) Init(context.Context) error {
	m.ui.RegisterFuncs(map[string]any{
		"shout": func(s string) string { return strings.ToUpper(s) + "!" },
	})
	return nil
}

func (m *Markdown) Close(context.Context) error { return nil }

// #endregion funcs

// #region config
// staticPaths serves two URL trees from ./public. With StaticPaths set and
// no StaticStripPrefix nothing is stripped, so /css/app.css is served from
// public/css/app.css.
func staticPaths() gas.Option {
	cfg := ui.DefaultConfig()
	cfg.UI.StaticDir = "public"
	cfg.UI.StaticPaths = []string{"/css/*", "/js/*"}

	return gas.WithSingletonService[gas.UIProvider](
		ui.New[gas.TemplateProvider](ui.WithConfig(cfg)),
	)
}

// #endregion config
