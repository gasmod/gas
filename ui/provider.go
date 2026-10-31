package ui

import (
	"context"
	"html/template"
	"net/http"
)

// Provider renders the pages of a template store as HTML, as described
// under Contract in the package documentation. Service implements it, and
// uitest.MockUI mocks it.
type Provider interface {
	// Render executes the page name through its layout and writes it to
	// w, or writes nothing and returns an error.
	Render(
		ctx context.Context,
		w http.ResponseWriter,
		name string,
		data any,
		opts ...RenderOption,
	) error
	// RenderFragment executes the fragment of the page name, without its
	// layout, and writes it to w, or writes nothing and returns an error.
	RenderFragment(
		ctx context.Context,
		w http.ResponseWriter,
		name string,
		data any,
		opts ...RenderOption,
	) error
	// RegisterFuncs adds funcs to the functions every page can call, or
	// adds none and returns an error.
	RegisterFuncs(funcs template.FuncMap) error
}
