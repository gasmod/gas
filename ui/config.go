package ui

import (
	"errors"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"strings"
	"testing"

	env "github.com/gasmod/gas/config/extensions/gasenv"
)

const (
	defaultStaticDir    = "static/"
	defaultUIStaticPath = "/static/*"
	defaultUILayout     = "base"
)

// Config holds all user-facing settings for the gas/ui service.
type Config struct {
	// Embedded GasEnv
	env.WithGasEnv

	// FuncMap supplies additional template functions. These are merged on
	// top of the built-in defaults; collisions override the default.
	FuncMap template.FuncMap

	UI Settings
}

// Settings represents the configuration for user-facing template and static file settings.
type Settings struct {
	// StaticDir is the root directory for static files (CSS, JS, images).
	// Leave empty to disable static file serving.
	StaticDir string

	// StaticPath is the URL route pattern for static assets. Default: "/static/*".
	StaticPath string

	// LayoutName is the {{define}} name of the entry-point layout template.
	// Default: "base".
	LayoutName string

	// StaticStripPrefix is the URL prefix stripped from requests before looking
	// up files in the static directory or FS. Defaults to StaticPath when empty.
	// Set this independently when the route paths differ from the FS layout.
	StaticStripPrefix string

	// StaticPaths is a slice of URL patterns for static assets (e.g. ["/css/*", "/js/*"]).
	// When set, routes are registered for each path instead of using StaticPath.
	StaticPaths []string
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		UI: Settings{
			StaticDir:  defaultStaticDir,
			StaticPath: defaultUIStaticPath,
			LayoutName: defaultUILayout,
		},
	}
}

// Validate checks that the Config fields are valid.
// hasStaticFS should be true if a custom fs.FS has been provided for static
// files, in which case UIStaticDir is not required.
func (c *Config) Validate(hasStaticFS bool) error {
	wd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("could not determine working directory: %w", err)
	}

	if c.UI.StaticDir != "" && !hasStaticFS {
		if err = validateDir(c.UI.StaticDir, wd); err != nil {
			return fmt.Errorf("UI.StaticDir: %w", err)
		}
	}

	if c.UI.StaticPath != "" {
		if err = validateRoutePattern(c.UI.StaticPath); err != nil {
			return fmt.Errorf("UI.StaticPath: %w", err)
		}
	}

	for i, sp := range c.UI.StaticPaths {
		if err = validateRoutePattern(sp); err != nil {
			return fmt.Errorf("UI.StaticPaths[%d]: %w", i, err)
		}
	}

	if c.UI.StaticStripPrefix != "" {
		if err = validateStaticPath(c.UI.StaticStripPrefix); err != nil {
			return fmt.Errorf("UI.StaticStripPrefix: %w", err)
		}
	}

	if c.UI.LayoutName == "" {
		return errors.New("UI.LayoutName must not be empty")
	}

	return nil
}

func validateDir(dir, projectRoot string) error {
	// skip in test environment
	if testing.Testing() {
		return nil
	}

	abs, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("could not resolve path %q: %w", dir, err)
	}
	// Ensure the path is within the project root
	if !strings.HasPrefix(abs, projectRoot+string(filepath.Separator)) && abs != projectRoot {
		return fmt.Errorf("%q is outside the project directory", dir)
	}
	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%q does not exist", dir)
		}
		return fmt.Errorf("could not stat %q: %w", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%q is not a directory", dir)
	}
	return nil
}

func validateRoutePattern(path string) error {
	if !strings.HasPrefix(path, "/") {
		return fmt.Errorf("%q must start with '/'", path)
	}
	return nil
}

func validateStaticPath(path string) error {
	if !strings.HasPrefix(path, "/") || !strings.HasSuffix(path, "/") {
		return fmt.Errorf("%q must start and end with '/'", path)
	}
	return nil
}
