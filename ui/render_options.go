package ui

// RenderOptions defines configuration options for rendering, such as layout, block, and HTTP status code.
type RenderOptions struct {
	layout string
	block  string
	status int
}

// apply applies one or more RenderFragmentOption to modify the RenderOptions instance.
func (r *RenderOptions) apply(op ...RenderFragmentOption) {
	for _, o := range op {
		o.apply(r)
	}
}

// RenderOption is an interface for applying configuration options to a RenderOptions instance.
type RenderOption interface {
	apply(*RenderOptions)
	r()
}

// RenderFragmentOption defines an interface for modifying RenderOptions during the rendering of a fragment.
type RenderFragmentOption interface {
	apply(*RenderOptions)
}

// RenderOptionFunc is a function type used to configure and modify the properties of a RenderOptions instance.
type RenderOptionFunc func(*RenderOptions)

// apply modifies the provided RenderOptions by applying the RenderOptionFunc.
func (f RenderOptionFunc) apply(o *RenderOptions) { f(o) }

// r is a method with no functionality, reserved for potential future use or to satisfy an interface contract.
func (RenderOptionFunc) r() {}

// RenderFragmentOptionFunc defines a function type used to configure or modify RenderOptions dynamically.
type RenderFragmentOptionFunc func(*RenderOptions)

// apply applies the RenderFragmentOptionFunc to modify the provided RenderOptions.
func (f RenderFragmentOptionFunc) apply(o *RenderOptions) { f(o) }

// WithLayout sets the layout property of RenderOptions to the specified layout string.
func WithLayout(layout string) RenderOptionFunc {
	return func(opts *RenderOptions) {
		opts.layout = layout
	}
}

// WithStatus sets the HTTP status code for the rendered response.
func WithStatus(status int) RenderOptionFunc {
	return func(opts *RenderOptions) {
		opts.status = status
	}
}

// WithBlock sets the block name in RenderOptions used for rendering a specific section or content fragment.
func WithBlock(block string) RenderFragmentOptionFunc {
	return func(opts *RenderOptions) {
		opts.block = block
	}
}
