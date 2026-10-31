package ui

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"maps"
	"slices"
	"strings"
	"sync"
	texttemplate "text/template"
	"text/template/parse"
	"unicode/utf8"
	"uuid"

	"github.com/gasmod/gas"
)

// baseName names the root of the set of layouts and partials. Nothing
// executes it.
const baseName = "gas/ui base"

// compiled is the result of one build: every page, compiled or with its
// error. Its map of pages never changes once built; a page changes only
// as page describes, under its lock.
type compiled struct {
	// pages holds each page by its page name.
	pages map[string]*page
	// b is the builder of the build, which reads a page again.
	b *builder
	// base holds the layouts and partials, never executed, which a page
	// read again is added to.
	base *template.Template
	// hasLayouts reports whether the store held a layout.
	hasLayouts bool
}

// page is one page of a build. Its set is never executed: each template
// a render executes runs in a clone of the set of its own, made on first
// use and kept, so that html/template escapes it alone and the result
// never depends on which templates ran before. A page that failed to
// load is read again by the next render that needs it.
type page struct {
	// set holds the layouts, the partials and the page, or is nil when
	// err is set. It is the source of every clone and never executed.
	set *template.Template
	// err is the page's load or compile error, or nil.
	err error
	// runs holds a clone of set for each template executed, by its name.
	runs map[string]*template.Template
	// root is the name of the page's own top-level template, its store
	// name.
	root string
	// fragment is the template RenderFragment executes by default: the
	// fragment block when the page defines it, else root when the page
	// has top-level text, else empty.
	fragment string
	// mu guards set, err, runs and fragment.
	mu sync.Mutex
}

// file is a template read from the store.
type file struct {
	// name is the template's store name.
	name string
	// content is the template's source.
	content []byte
}

// builder builds one compiled set.
type builder struct {
	// store is the template store read.
	store gas.TemplateProvider
	// set holds the settings.
	set *Settings
	// funcs are the functions every template may call.
	funcs template.FuncMap
	// partialDefs holds the names a partial defines with something in
	// it.
	partialDefs map[string]bool
}

// build reads the store and compiles every page, as described in the
// package documentation. It returns a nil compiled and an error when no
// page can compile: the store cannot list, a layout or partial cannot be
// read or compiled, or ctx ends. Otherwise it returns the set and the
// joined errors of the pages that failed, or nil.
func build(
	ctx context.Context,
	store gas.TemplateProvider,
	set *Settings,
	funcs template.FuncMap,
) (*compiled, error) {
	all := defaultFuncs(uuid.New().String())
	maps.Copy(all, funcs)

	b := &builder{store: store, set: set, funcs: all, partialDefs: make(map[string]bool)}

	layouts, partials, pages, err := b.list(ctx)
	if err != nil {
		return nil, err
	}

	shared, err := b.readAll(ctx, slices.Concat(layouts, partials))
	if err != nil {
		return nil, err
	}

	base := template.New(baseName).Funcs(all)

	for _, f := range shared {
		var trees map[string]*parse.Tree

		trees, err = b.add(base, f)
		if err != nil {
			return nil, err
		}

		if strings.HasPrefix(f.name, PartialsPrefix) {
			for name := range trees {
				if defines(trees, name) {
					b.partialDefs[name] = true
				}
			}
		}
	}

	c := &compiled{b: b, base: base, hasLayouts: len(layouts) > 0}

	c.pages, err = b.pages(ctx, base, pages)
	if ctx.Err() != nil {
		return nil, ctx.Err() //nolint:wrapcheck // the caller names the operation.
	}

	return c, err
}

// pages compiles every page of names over base, and returns them by page
// name with the joined errors of those that failed, in byte order of
// their names, or nil.
func (b *builder) pages(
	ctx context.Context,
	base *template.Template,
	names []string,
) (map[string]*page, error) {
	pages := make(map[string]*page, len(names))

	var errs []error

	for _, name := range names {
		key := b.pageName(name)
		if checkPageName(key) != nil {
			errs = append(errs, fmt.Errorf("%w: %q leaves no page name", ErrCompile, name))

			continue
		}

		p := b.page(ctx, base, name)

		if other, ok := pages[key]; ok {
			p = &page{
				err: fmt.Errorf(
					"%w: %q and %q are both page %q",
					ErrCompile,
					other.root,
					name,
					key,
				),
			}
			other.set, other.err = nil, p.err
		}

		p.root = name
		pages[key] = p
	}

	for _, key := range slices.Sorted(maps.Keys(pages)) {
		perr := pages[key].err
		if perr != nil && !slices.Contains(errs, perr) {
			errs = append(errs, perr)
		}
	}

	return pages, errors.Join(errs...)
}

// list returns the names of the store's layouts, partials and pages, in
// byte order, without a name twice, leaving out the names without an
// extension of the settings.
func (b *builder) list(ctx context.Context) (layouts, partials, pages []string, err error) {
	names, err := b.store.List(ctx)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%w: %w", ErrLoad, err)
	}

	slices.Sort(names)

	for _, name := range slices.Compact(names) {
		switch {
		case !hasExtension(name, b.set.Extensions):
		case strings.HasPrefix(name, LayoutsPrefix):
			layouts = append(layouts, name)
		case strings.HasPrefix(name, PartialsPrefix):
			partials = append(partials, name)
		case strings.HasPrefix(name, b.set.PagesPrefix):
			pages = append(pages, name)
		}
	}

	return layouts, partials, pages, nil
}

// readAll returns the content of every template of names.
func (b *builder) readAll(ctx context.Context, names []string) ([]file, error) {
	files := make([]file, 0, len(names))

	for _, name := range names {
		f, err := b.read(ctx, name)
		if err != nil {
			return nil, err
		}

		files = append(files, f)
	}

	return files, nil
}

// read returns the content of the template name.
func (b *builder) read(ctx context.Context, name string) (file, error) {
	content, err := b.store.Get(ctx, name)
	if err != nil {
		return file{}, fmt.Errorf("%w: %q: %w", ErrLoad, name, err)
	}

	return file{name: name, content: content}, nil
}

// page returns the page of the store name: a copy of base with the page
// added, or the error of the page alone. When the page does not define
// the fragment block but has top-level text, that text is the block too,
// so that a layout's block shows what RenderFragment does, unless the
// text calls the block itself or a partial defines it.
func (b *builder) page(ctx context.Context, base *template.Template, name string) *page {
	f, err := b.read(ctx, name)
	if err != nil {
		return &page{err: err}
	}

	set, err := base.Clone()
	if err != nil {
		return &page{err: fmt.Errorf("%w: %w", ErrCompile, err)}
	}

	trees, err := b.add(set, f)
	if err != nil {
		return &page{err: err}
	}

	p := &page{set: set, runs: make(map[string]*template.Template)}

	switch {
	case defines(trees, b.set.FragmentBlock):
		p.fragment = b.set.FragmentBlock
	case defines(trees, name):
		p.fragment = name

		if b.partialDefs[b.set.FragmentBlock] || calls(trees[name].Root, b.set.FragmentBlock) {
			break
		}

		_, err = set.AddParseTree(b.set.FragmentBlock, trees[name])
		if err != nil {
			return &page{err: fmt.Errorf("%w: %w", ErrCompile, err)}
		}
	}

	return p
}

// add parses f and adds its templates to set, and returns them. Its
// top-level text is the template of its store name. A template f defines
// replaces one of the same name in set, as html/template's Parse does,
// unless it is empty.
func (b *builder) add(set *template.Template, f file) (map[string]*parse.Tree, error) {
	trees, err := b.parse(f)
	if err != nil {
		return nil, err
	}

	for _, name := range slices.Sorted(maps.Keys(trees)) {
		tree := trees[name]
		if parse.IsEmptyTree(tree.Root) && set.Lookup(name) != nil {
			continue
		}

		_, err = set.AddParseTree(name, tree)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrCompile, err)
		}
	}

	return trees, nil
}

// parse returns the templates of f by name: its top-level text under its
// store name, and each template it defines. Defining a name that ends in
// an extension of the settings is an error, as those name files.
func (b *builder) parse(f file) (map[string]*parse.Tree, error) {
	t, err := texttemplate.New(f.name).Funcs(b.funcs).Parse(string(f.content))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrCompile, err)
	}

	trees := make(map[string]*parse.Tree)

	for _, x := range t.Templates() {
		name := x.Name()
		if name != f.name && hasExtension(name, b.set.Extensions) {
			return nil, fmt.Errorf(
				"%w: %s defines %q, a name ending in an extension, which only files have",
				ErrCompile,
				f.name,
				name,
			)
		}

		trees[name] = x.Tree
	}

	return trees, nil
}

// pageName returns the page name of the store name of a page: without the
// pages prefix and the longest extension it ends in.
func (b *builder) pageName(name string) string {
	key := strings.TrimPrefix(name, b.set.PagesPrefix)

	ext := ""

	for _, e := range b.set.Extensions {
		if strings.HasSuffix(key, e) && len(e) > len(ext) {
			ext = e
		}
	}

	return strings.TrimSuffix(key, ext)
}

// defines reports whether trees holds a template name with something in
// it.
func defines(trees map[string]*parse.Tree, name string) bool {
	tree, ok := trees[name]

	return ok && !parse.IsEmptyTree(tree.Root)
}

// calls reports whether node, or a node inside it, calls the template
// name with {{template}}.
func calls(node parse.Node, name string) bool {
	switch n := node.(type) {
	case *parse.ListNode:
		if n == nil {
			return false
		}

		for _, c := range n.Nodes {
			if calls(c, name) {
				return true
			}
		}
	case *parse.TemplateNode:
		return n.Name == name
	case *parse.IfNode:
		return branchCalls(&n.BranchNode, name)
	case *parse.RangeNode:
		return branchCalls(&n.BranchNode, name)
	case *parse.WithNode:
		return branchCalls(&n.BranchNode, name)
	}

	return false
}

// branchCalls reports whether a list of n calls the template name.
func branchCalls(n *parse.BranchNode, name string) bool {
	return calls(n.List, name) || calls(n.ElseList, name)
}

// hasExtension reports whether name ends in one of exts.
func hasExtension(name string, exts []string) bool {
	for _, ext := range exts {
		if strings.HasSuffix(name, ext) {
			return true
		}
	}

	return false
}

// checkPageName returns nil when name is a name a page can have, and
// otherwise an error wrapping ErrInvalidName that gives the reason and
// never the name.
func checkPageName(name string) error {
	var reason string

	switch {
	case name == "":
		reason = "empty"
	case !utf8.ValidString(name):
		reason = "not valid UTF-8"
	case strings.ContainsRune(name, 0):
		reason = "holds a NUL byte"
	case strings.ContainsRune(name, '\\'):
		reason = "holds a backslash"
	case !fs.ValidPath(name):
		reason = "not a clean slash path"
	default:
		return nil
	}

	return fmt.Errorf("%w: %s", ErrInvalidName, reason)
}
