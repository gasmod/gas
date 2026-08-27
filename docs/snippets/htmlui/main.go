// Package main shows server-rendered HTML with gas/ui.
package main

// #region imports
import (
	"log"
	"net/http"
	"os"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/config/providers"
	gaslog "github.com/gasmod/gas/log"
	templatefs "github.com/gasmod/gas/template/fs"
	"github.com/gasmod/gas/ui"
)

// #endregion imports

func main() {
	cfg := config.New(config.WithProvider(providers.NewJSONProvider(
		providers.WithJSONFilePath("config.json"),
	)))
	if err := cfg.Load(); err != nil {
		log.Fatal(err)
	}

	// #region wiring
	app := gas.NewApp(
		gas.WithServiceInstance[gas.ConfigProvider](cfg),
		gas.WithSingletonService[gas.Logger](gaslog.NewSlogLogger()),

		// Where templates come from. The fs store takes an fs.FS, so os.DirFS
		// during development and an embed.FS in a shipped binary.
		gas.WithSingletonService[gas.TemplateProvider](
			templatefs.NewStore(os.DirFS("templates")),
		),

		// What renders them. The type parameter names the template provider to
		// inject; the registration key is the interface consumers ask for.
		gas.WithSingletonService[gas.UIProvider](ui.New[gas.TemplateProvider]()),
	)
	// #endregion wiring

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

// #region render
// A service takes gas.UIProvider and never imports gas/ui.
type Service struct {
	ui gas.UIProvider
}

func New(ui gas.UIProvider) *Service { return &Service{ui: ui} }

func (s *Service) home(w http.ResponseWriter, r *http.Request) {
	_ = s.ui.Render(w, "home", map[string]any{
		"SiteName": "Gas",
		"Name":     "Ahmed",
	})
}

// #endregion render

// #region fragment
// HTMX asks for a fragment, a browser asks for a full page. Same template.
func (s *Service) notes(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{"Notes": []string{"first", "second"}}

	if r.Header.Get("HX-Request") == "true" {
		_ = s.ui.RenderFragment(w, "notes/list", data)
		return
	}
	_ = s.ui.Render(w, "notes/list", data)
}

// #endregion fragment

// #region funcs
// Any service can contribute template helpers during Init. Templates are built
// lazily on first render, so registrations made in Init are always in time.
func (s *Service) Init() error {
	s.ui.RegisterFuncs(map[string]any{
		"formatDate": func(layout string) string { return layout },
	})
	return nil
}

// #endregion funcs
