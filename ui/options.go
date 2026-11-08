package ui

import (
	"html/template"
	"io/fs"
	"maps"
)

// Option configures a Service. Options only record what they are given;
// Init checks it.
type Option func(*Service)

// WithConfig makes Init use a copy of cfg instead of binding the
// configuration from the ConfigProvider. Init copies *cfg, and its
// slices, each time it runs and never changes it; the copy is checked
// against its validate tags as a bound configuration is. A nil cfg makes
// Init fail with ErrNilConfig.
//
// Default: bind the `ui` section from the ConfigProvider over
// DefaultConfig.
func WithConfig(cfg *Config) Option {
	return func(s *Service) { s.cfgSource.Set(cfg) }
}

// WithStaticFS makes the service serve static files from fsys, such as an
// embed.FS, instead of the StaticDir setting. A nil fsys makes Init fail
// with ErrNilFS.
//
// Default: the StaticDir setting.
func WithStaticFS(fsys fs.FS) Option {
	return func(s *Service) {
		s.staticFS = fsys
		s.staticFSSet = true
	}
}

// WithFuncs adds funcs to the template functions, over the defaults, as
// RegisterFuncs does. Init checks them, and fails with ErrInvalidFunc for
// one RegisterFuncs refuses. The map is copied; a nil map adds nothing.
//
// Default: the default functions only.
func WithFuncs(funcs template.FuncMap) Option {
	funcs = maps.Clone(funcs)

	return func(s *Service) { s.optFuncs = funcs }
}

// RenderOptions are the options of a render, resolved, as
// ApplyRenderOptions returns them.
type RenderOptions struct {
	// Layout is from WithLayout: the template Render executes, empty for
	// the LayoutName setting.
	Layout string
	// Block is from WithBlock: the template RenderFragment executes,
	// empty for the page's fragment.
	Block string
	// Status is from WithStatus: the status written before the page, 0
	// for none.
	Status int
}

// RenderOption configures one Render or RenderFragment call, as
// described in the package documentation.
type RenderOption func(*RenderOptions)

// WithLayout makes Render execute the template name of the page's set
// instead of the LayoutName setting. An empty name keeps the setting.
// RenderFragment refuses it with ErrInvalidOption.
func WithLayout(name string) RenderOption {
	return func(o *RenderOptions) { o.Layout = name }
}

// WithBlock makes RenderFragment execute the template name of the page's
// set instead of the page's fragment. An empty name keeps the fragment.
// Render refuses it with ErrInvalidOption.
func WithBlock(name string) RenderOption {
	return func(o *RenderOptions) { o.Block = name }
}

// WithStatus makes Render and RenderFragment write status before the
// page. It must be 200 to 599 and allow a body, so not 204 or 304, or the
// call fails with ErrInvalidStatus and writes nothing. 0 writes no
// status.
//
// Default: no status; the response writer sends 200 on the first write
// unless the handler wrote another.
func WithStatus(status int) RenderOption {
	return func(o *RenderOptions) { o.Status = status }
}

// ApplyRenderOptions returns opts resolved in order, a later option
// overriding an earlier one; nil options are skipped. Render and
// RenderFragment refuse a nil option instead.
func ApplyRenderOptions(opts ...RenderOption) RenderOptions {
	var o RenderOptions

	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}

	return o
}
