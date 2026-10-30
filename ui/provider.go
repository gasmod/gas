package ui

import (
	"html/template"
	"net/http"
)

// Provider abstracts template rendering. Implemented by gas/ui or any
// other UI service. Template storage and retrieval is handled by
// TemplateProvider; Provider focuses on compilation and rendering.
type Provider interface {
	Render(
		w http.ResponseWriter,
		name string,
		data any,
		opts ...RenderOption,
	) error
	RenderFragment(
		w http.ResponseWriter,
		name string,
		data any,
		opts ...RenderFragmentOption,
	) error
	RegisterFuncs(funcs template.FuncMap)
}
