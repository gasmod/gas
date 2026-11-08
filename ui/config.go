package ui

import (
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// DefaultStaticDir is the directory static files are served from in
	// DefaultConfig: none, so that no static files are served unless
	// configured.
	DefaultStaticDir = ""
	// DefaultStaticPath is the route static files are served on in
	// DefaultConfig.
	DefaultStaticPath = "/static/*"
	// DefaultLayoutName is the name of the template Render executes in
	// DefaultConfig.
	DefaultLayoutName = "base"
	// DefaultFragmentBlock is the name of the block RenderFragment
	// executes in DefaultConfig.
	DefaultFragmentBlock = "content"
	// DefaultExtension is the one template extension of DefaultConfig.
	DefaultExtension = ".html"
	// LayoutsPrefix starts the name of every layout in the template
	// store.
	LayoutsPrefix = "layouts/"
	// PartialsPrefix starts the name of every partial in the template
	// store.
	PartialsPrefix = "partials/"
)

// Config holds the service's configuration, read from the `ui` section.
type Config struct {
	// UI holds the settings.
	UI Settings
}

// Settings are the service's settings, as described in the package
// documentation.
type Settings struct {
	StaticDir         string
	StaticPath        string
	StaticStripPrefix string
	PagesPrefix       string
	LayoutName        string `validate:"required"`
	FragmentBlock     string `validate:"required"`
	Extensions        []string
	StaticPaths       []string
	StaticMaxAge      time.Duration `validate:"min=0"`
	StaticDotfiles    bool
	Reload            bool
}

// DefaultConfig returns the configuration the service starts from before
// binding: DefaultExtension, DefaultStaticDir, DefaultStaticPath,
// DefaultLayoutName and DefaultFragmentBlock, and every other setting
// zero.
func DefaultConfig() *Config {
	return &Config{
		UI: Settings{
			Extensions:    []string{DefaultExtension},
			StaticDir:     DefaultStaticDir,
			StaticPath:    DefaultStaticPath,
			LayoutName:    DefaultLayoutName,
			FragmentBlock: DefaultFragmentBlock,
		},
	}
}

// The rules the validate tags cannot express. Each names the setting,
// never its value.
var (
	errNoExtension = errors.New("UI.Extensions must hold at least one extension")
	errPagesPrefix = errors.New(
		"UI.PagesPrefix must be empty or a clean path ending in /, not in layouts/ or partials/",
	)
	errNoStaticPath = errors.New(
		"UI.StaticPath must be set when static files are served and UI.StaticPaths is empty",
	)
	errStripPrefix = errors.New("UI.StaticStripPrefix must be empty or start and end with `/`")
	errMaxAge      = errors.New("UI.StaticMaxAge must be 0 or at least 1s")
	// errExtension is the rule of one extension, after its setting.
	errExtension = errors.New("must be a dot and characters, none a slash, backslash or NUL")
	// errRoute is the rule of a static route, after its setting.
	errRoute = errors.New("must start with `/`, hold `*` only last, and pair `{` with `}`")
)

// rules returns the check of the rules the validate tags cannot express,
// for a service with a static fs.FS when staticFS is true.
func rules(staticFS bool) func(*Config) error {
	return func(cfg *Config) error {
		set := &cfg.UI

		err := checkExtensions(set.Extensions)
		if err != nil {
			return err
		}

		if !validPagesPrefix(set.PagesPrefix) {
			return errPagesPrefix
		}

		if !staticFS && set.StaticDir == "" {
			return nil
		}

		return checkStatic(set)
	}
}

// checkExtensions returns nil when exts holds at least one extension and
// each is a dot and one or more characters, none a slash, a backslash or
// NUL.
func checkExtensions(exts []string) error {
	if len(exts) == 0 {
		return errNoExtension
	}

	for i, ext := range exts {
		if len(ext) < len(".x") || ext[0] != '.' || !utf8.ValidString(ext) ||
			strings.ContainsAny(ext, "/\\\x00") {
			return fmt.Errorf("UI.Extensions[%d] %w", i, errExtension)
		}
	}

	return nil
}

// validPagesPrefix reports whether p is empty or a clean slash path
// ending in `/` that is not inside the layouts or the partials.
func validPagesPrefix(p string) bool {
	if p == "" {
		return true
	}

	dir, ok := strings.CutSuffix(p, "/")

	return ok && dir != "." && fs.ValidPath(dir) && !strings.ContainsRune(dir, '\\') &&
		!strings.HasPrefix(p, LayoutsPrefix) && !strings.HasPrefix(p, PartialsPrefix)
}

// checkStatic checks the static settings of a service that serves static
// files.
func checkStatic(set *Settings) error {
	if len(set.StaticPaths) == 0 {
		if set.StaticPath == "" {
			return errNoStaticPath
		}

		if !validRoute(set.StaticPath) {
			return fmt.Errorf("UI.StaticPath %w", errRoute)
		}
	}

	for i, p := range set.StaticPaths {
		if !validRoute(p) {
			return fmt.Errorf("UI.StaticPaths[%d] %w", i, errRoute)
		}
	}

	if set.StaticStripPrefix != "" && !validPrefix(set.StaticStripPrefix) {
		return errStripPrefix
	}

	if set.StaticMaxAge != 0 && set.StaticMaxAge < time.Second {
		return errMaxAge
	}

	return nil
}

// validRoute reports whether p is a route the router takes: it starts
// with `/`, holds `*` only as its last character, and holds `{` and `}`
// only in pairs that do not nest.
func validRoute(p string) bool {
	if !strings.HasPrefix(p, "/") {
		return false
	}

	if i := strings.IndexByte(p, '*'); i >= 0 && i != len(p)-1 {
		return false
	}

	return pairedBraces(p)
}

// pairedBraces reports whether p holds `{` and `}` only in pairs that do
// not nest.
func pairedBraces(p string) bool {
	open := false

	for _, c := range p {
		switch {
		case c == '{' && !open:
			open = true
		case c == '}' && open:
			open = false
		case c == '{' || c == '}':
			return false
		}
	}

	return !open
}

// validPrefix reports whether p starts and ends with `/`.
func validPrefix(p string) bool {
	return strings.HasPrefix(p, "/") && strings.HasSuffix(p, "/")
}

// stripPrefix returns the prefix the static handler removes: the
// StaticStripPrefix setting, else nothing with StaticPaths set, else
// StaticPath up to the last `/` before its first `*` or `{`.
func stripPrefix(set *Settings) string {
	switch {
	case set.StaticStripPrefix != "":
		return set.StaticStripPrefix
	case len(set.StaticPaths) > 0:
		return ""
	}

	literal := set.StaticPath
	if i := strings.IndexAny(literal, "*{"); i >= 0 {
		literal = literal[:i]
	}

	return literal[:strings.LastIndexByte(literal, '/')+1]
}

// staticRoutes returns the routes static files are served on.
func staticRoutes(set *Settings) []string {
	if len(set.StaticPaths) > 0 {
		return slices.Clone(set.StaticPaths)
	}

	return []string{set.StaticPath}
}
