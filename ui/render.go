package ui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"sync"
)

// contentType is the Content-Type of a rendered page.
const contentType = "text/html; charset=utf-8"

// maxPooledBuffer is the largest buffer, in bytes, kept for another
// render: 1 MiB.
const maxPooledBuffer = 1 << 20

// buffers holds the buffers pages are executed into.
var buffers = sync.Pool{New: func() any { return new(bytes.Buffer) }}

// Render executes the page name with data through its layout and writes
// it to w, as described in the package documentation. On error it writes
// nothing.
func (s *Service) Render(
	ctx context.Context,
	w http.ResponseWriter,
	name string,
	data any,
	opts ...RenderOption,
) error {
	return s.render(ctx, w, opRender, name, data, opts)
}

// RenderFragment executes the fragment of the page name with data,
// without its layout, and writes it to w, as described in the package
// documentation. On error it writes nothing.
func (s *Service) RenderFragment(
	ctx context.Context,
	w http.ResponseWriter,
	name string,
	data any,
	opts ...RenderOption,
) error {
	return s.render(ctx, w, opRenderFragment, name, data, opts)
}

// render renders the page name for op, Render or RenderFragment.
func (s *Service) render(
	ctx context.Context,
	w http.ResponseWriter,
	op, name string,
	data any,
	opts []RenderOption,
) error {
	err := checkPageName(name)
	if err != nil {
		return opError(op, "", err)
	}

	st, err := s.loaded(ctx, op, name)
	if err != nil {
		return err
	}

	o, err := resolve(op, opts)
	if err != nil {
		return opError(op, name, err)
	}

	run, entry, err := s.target(ctx, st, op, name, &o)
	if err != nil {
		return opError(op, name, err)
	}

	buf := buffers.Get().(*bytes.Buffer) //nolint:forcetypeassert // the pool holds only buffers.
	defer release(buf)

	err = execute(run, buf, entry, data)
	if err != nil {
		return opError(op, name, fmt.Errorf("%w: %w", ErrExecute, err))
	}

	err = write(w, buf, o.Status)
	if err != nil {
		return opError(op, name, fmt.Errorf("%w: %w", ErrWrite, err))
	}

	return nil
}

// target returns the set of the page name, in st's set built if need
// be, that op executes with o, and the name of the template to execute
// in it.
func (s *Service) target(
	ctx context.Context,
	st *state,
	op, name string,
	o *RenderOptions,
) (*template.Template, string, error) {
	c, err := s.compiledSet(ctx, st)
	if err != nil {
		return nil, "", err
	}

	p, ok := c.pages[name]
	if !ok {
		return nil, "", ErrPageNotFound
	}

	c.reread(ctx, p)

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.err != nil {
		return nil, "", p.err
	}

	var entry string

	if op == opRenderFragment {
		entry, err = p.fragmentEntry(o, &st.set)
	} else {
		entry, err = p.layoutEntry(o, &st.set, c.hasLayouts)
	}

	if err != nil {
		return nil, "", err
	}

	run, err := p.run(entry)
	if err != nil {
		return nil, "", err
	}

	return run, entry, nil
}

// reread reads and compiles the page p again within ctx, over the
// layouts and partials of c, when it failed to load, and records the
// outcome in p unless another call recorded one that loaded meanwhile. It
// holds p.mu only to look and to record, never while it reads the store,
// so that a read that hangs blocks no other caller of p.mu.
func (c *compiled) reread(ctx context.Context, p *page) {
	p.mu.Lock()

	failed := errors.Is(p.err, ErrLoad)

	p.mu.Unlock()

	if !failed {
		return
	}

	np := c.b.page(ctx, c.base, p.root)

	p.mu.Lock()
	defer p.mu.Unlock()

	if errors.Is(p.err, ErrLoad) {
		p.set, p.err, p.runs, p.fragment = np.set, np.err, np.runs, np.fragment
	}
}

// run returns the clone of p's set that entry executes in, made on first
// use. The caller holds p.mu.
func (p *page) run(entry string) (*template.Template, error) {
	run, ok := p.runs[entry]
	if ok {
		return run, nil
	}

	run, err := p.set.Clone()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrExecute, err)
	}

	p.runs[entry] = run

	return run, nil
}

// errPanic reports a panic while a page executed.
var errPanic = errors.New("panic")

// execute executes the template entry of run with data into w. A panic
// of the execution is returned as an error.
func execute(run *template.Template, w io.Writer, entry string, data any) (err error) {
	defer func() {
		r := recover()
		if r != nil {
			err = fmt.Errorf("%w: %v", errPanic, r)
		}
	}()

	//nolint:wrapcheck // render wraps it with ErrExecute.
	return run.ExecuteTemplate(w, entry, data)
}

// write writes buf to w, after the HTML Content-Type unless w has one,
// and after status unless it is 0.
func write(w http.ResponseWriter, buf *bytes.Buffer, status int) error {
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", contentType)
	}

	if status != 0 {
		w.WriteHeader(status)
	}

	_, err := buf.WriteTo(w)

	return err //nolint:wrapcheck // render wraps it with ErrWrite.
}

// release returns buf to the pool, unless it grew past maxPooledBuffer.
func release(buf *bytes.Buffer) {
	if buf.Cap() > maxPooledBuffer {
		return
	}

	buf.Reset()
	buffers.Put(buf)
}

// resolve returns the options of op, or an error wrapping
// ErrInvalidOption or ErrInvalidStatus.
func resolve(op string, opts []RenderOption) (RenderOptions, error) {
	for _, opt := range opts {
		if opt == nil {
			return RenderOptions{}, fmt.Errorf("%w: nil", ErrInvalidOption)
		}
	}

	o := ApplyRenderOptions(opts...)

	switch {
	case op == opRender && o.Block != "":
		return o, fmt.Errorf("%w: WithBlock applies to RenderFragment only", ErrInvalidOption)
	case op == opRenderFragment && o.Layout != "":
		return o, fmt.Errorf("%w: WithLayout applies to Render only", ErrInvalidOption)
	case o.Status != 0 && !bodyStatus(o.Status):
		return o, fmt.Errorf("%w: must be 200 to 599, and not 204 or 304", ErrInvalidStatus)
	}

	return o, nil
}

// bodyStatus reports whether status is 200 to 599 and allows a body.
func bodyStatus(status int) bool {
	return status >= http.StatusOK && status <= 599 &&
		status != http.StatusNoContent && status != http.StatusNotModified
}

// fragmentEntry returns the template of the page RenderFragment
// executes with o, under set.
func (p *page) fragmentEntry(o *RenderOptions, set *Settings) (string, error) {
	switch {
	case o.Block != "":
		if !has(p.set, o.Block) {
			return "", fmt.Errorf("%w: %q", ErrBlockNotFound, o.Block)
		}

		return o.Block, nil
	case p.fragment == "":
		return "", fmt.Errorf("%w: the page defines no %q block and has no top-level text",
			ErrNoFragment, set.FragmentBlock)
	default:
		return p.fragment, nil
	}
}

// layoutEntry returns the template of the page Render executes with o,
// under set, in a build that held layouts when hasLayouts is true.
func (p *page) layoutEntry(o *RenderOptions, set *Settings, hasLayouts bool) (string, error) {
	layout := o.Layout
	if layout == "" {
		layout = set.LayoutName
	}

	switch {
	case has(p.set, layout):
		return layout, nil
	case o.Layout == "" && !hasLayouts:
		return p.root, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrLayoutNotFound, layout)
	}
}

// has reports whether set holds a template of name with a body. The root
// of the layouts and partials has none.
func has(set *template.Template, name string) bool {
	t := set.Lookup(name)

	return t != nil && t.Tree != nil
}
