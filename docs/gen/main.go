// Command gen renders the Go doc of every public package of the Gas
// repository, the single source of the docs site's reference and of the
// gas agent skill.
//
//	go run . <repo root> <output dir>
//	go run . -skill <repo root> <skill dir>
//
// The first form writes the reference pages of the docs site: the package
// overview from its doc comment, then its constants, variables, functions
// and types, each with its declaration and doc comment. The second writes
// the gas skill: SKILL.md, from skill.md.tmpl and the site's installation
// page, and a reference per package holding its overview alone, leaving
// the API to go doc.
//
// It replaces the output directory's contents. It parses source files only,
// so it needs no dependencies and runs outside the workspace (GOWORK=off).
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/doc"
	"go/doc/comment"
	"go/parser"
	"go/printer"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const (
	modulePath = "github.com/gasmod/gas"
	// coreName is the name the core package's page and reference go by.
	coreName = "gas"
	editBase = "https://github.com/gasmod/gas/edit/main/"
	siteBase = "/gas/reference/"

	// Heading levels: a package's sections, a declaration's doc and a
	// function's doc.
	sectionLevel = 2
	declLevel    = 4
	funcLevel    = 5

	dirPerm  = 0o750
	filePerm = 0o600
)

var errNoPackages = errors.New("no packages found")

// skipDirs are directory names whose trees hold no public packages.
var skipDirs = []string{
	"internal", "test", "testdata", "docs", "node_modules", "scripts", "examples",
}

func main() {
	const (
		wantArgs  = 2
		usageExit = 2
	)

	skill := flag.Bool("skill", false, "write the gas skill instead of the site's reference")

	flag.Parse()

	args := flag.Args()
	if len(args) != wantArgs {
		fmt.Fprintln(os.Stderr, "usage: gen [-skill] <repo root> <output dir>")
		os.Exit(usageExit)
	}

	run := runSite
	if *skill {
		run = runSkill
	}

	err := run(args[0], args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "gen:", err)
		os.Exit(1)
	}
}

// runSite writes the site's reference pages to out.
func runSite(root, out string) error {
	dirs, known, err := prepare(root, out)
	if err != nil {
		return err
	}

	for _, d := range dirs {
		pkg, docFile, err := readPackage(root, d)
		if err != nil {
			return fmt.Errorf("%s: %w", d, err)
		}

		editURL := ""

		if docFile != "" {
			rel, _ := filepath.Rel(root, docFile)

			editURL = editBase + filepath.ToSlash(rel)
		}

		r := &renderer{pkg: pkg.doc, path: pkg.path, known: known, fset: pkg.fset}

		err = writeFile(
			filepath.Join(out, filepath.FromSlash(pagePath(pkg.path, known))+".md"),
			r.page(editURL),
		)
		if err != nil {
			return fmt.Errorf("%s: %w", d, err)
		}
	}

	fmt.Fprintf(os.Stderr, "gen: rendered %d packages\n", len(dirs))

	return nil
}

// prepare finds the public packages under root, and empties out.
func prepare(root, out string) ([]string, map[string]bool, error) {
	dirs, err := packageDirs(root)
	if err != nil {
		return nil, nil, err
	}

	if len(dirs) == 0 {
		return nil, nil, fmt.Errorf("%w under %s", errNoPackages, root)
	}

	known := map[string]bool{}
	for _, d := range dirs {
		known[importPath(root, d)] = true
	}

	err = os.RemoveAll(out)
	if err != nil {
		return nil, nil, fmt.Errorf("clear output: %w", err)
	}

	return dirs, known, nil
}

// writeFile writes data to path, creating its directory.
func writeFile(path string, data []byte) error {
	err := os.MkdirAll(
		filepath.Dir(path),
		dirPerm,
	)
	if err != nil {
		return fmt.Errorf("create dir: %w", err)
	}

	err = os.WriteFile(path, data, filePerm)
	if err != nil {
		return fmt.Errorf("write: %w", err)
	}

	return nil
}

// packageDirs returns the directories of the public packages: they hold a
// non-test Go file, and no element of their path below root is hidden or
// one of skipDirs.
func packageDirs(root string) ([]string, error) {
	var dirs []string

	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return err
		}

		name := d.Name()
		if p != root && (strings.HasPrefix(name, ".") || slices.Contains(skipDirs, name)) {
			return filepath.SkipDir
		}

		files, err := goFiles(p)
		if err != nil {
			return err
		}

		if len(files) > 0 {
			dirs = append(dirs, p)
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", root, err)
	}

	return dirs, nil
}

// goFiles returns the paths of the non-test Go files in dir.
func goFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read dir: %w", err)
	}

	var files []string

	for _, e := range entries {
		n := e.Name()
		if e.Type().IsRegular() && strings.HasSuffix(n, ".go") &&
			!strings.HasSuffix(n, "_test.go") {
			files = append(files, filepath.Join(dir, n))
		}
	}

	return files, nil
}

func importPath(root, dir string) string {
	rel, _ := filepath.Rel(root, dir)
	if rel == "." {
		return modulePath
	}

	return modulePath + "/" + filepath.ToSlash(rel)
}

// pagePath returns the site path of an import path's page, relative to the
// reference directory: the module root is gas, and a package with packages
// below it is its directory's index page.
func pagePath(path string, known map[string]bool) string {
	rel := strings.TrimPrefix(strings.TrimPrefix(path, modulePath), "/")
	if rel == "" {
		return coreName
	}

	for k := range known {
		if strings.HasPrefix(k, path+"/") {
			return rel + "/index"
		}
	}

	return rel
}

// parsedPackage is a package read for rendering.
type parsedPackage struct {
	doc  *doc.Package
	fset *token.FileSet
	path string
}

// readPackage reads the package in dir and returns it with the path of the
// file that holds its doc comment, or "".
func readPackage(root, dir string) (parsedPackage, string, error) {
	fset := token.NewFileSet()

	files, docFile, err := parseDir(fset, dir)
	if err != nil {
		return parsedPackage{}, "", err
	}

	path := importPath(root, dir)

	pkg, err := doc.NewFromFiles(fset, files, path)
	if err != nil {
		return parsedPackage{}, "", fmt.Errorf("read package doc: %w", err)
	}

	return parsedPackage{doc: pkg, fset: fset, path: path}, docFile, nil
}

// parseDir parses the non-test Go files of dir and returns them with the
// path of the file that holds the package doc, or "" when none does.
func parseDir(fset *token.FileSet, dir string) ([]*ast.File, string, error) {
	paths, err := goFiles(dir)
	if err != nil {
		return nil, "", err
	}

	var (
		files   []*ast.File
		docFile string
	)

	for _, p := range paths {
		f, err := parser.ParseFile(fset, p, nil, parser.ParseComments)
		if err != nil {
			return nil, "", fmt.Errorf("parse: %w", err)
		}

		files = append(files, f)

		// The package doc lives in doc.go by convention; any other file
		// with a package comment is the fallback.
		if f.Doc != nil && (docFile == "" || filepath.Base(p) == "doc.go") {
			docFile = p
		}
	}

	return files, docFile, nil
}

type renderer struct {
	pkg   *doc.Package
	known map[string]bool
	fset  *token.FileSet
	path  string
	buf   bytes.Buffer
	// skill renders for the skill: doc links as Go names, and no
	// Markdown escapes.
	skill bool
}

func (r *renderer) page(editURL string) []byte {
	rel := strings.TrimPrefix(strings.TrimPrefix(r.path, modulePath), "/")

	title := coreName
	if rel != "" {
		title = rel
	}

	edit := "false"
	if editURL != "" {
		edit = fmt.Sprintf("%q", editURL)
	}

	fmt.Fprintf(
		&r.buf,
		"---\ntitle: %q\ndescription: %q\neditUrl: %s\nsidebar:\n  label: %q\n---\n\n",
		title,
		r.pkg.Synopsis(r.pkg.Doc),
		edit,
		sidebarLabel(rel),
	)
	fmt.Fprintf(&r.buf, "```go\nimport %q\n```\n\n", r.path)

	r.doc(r.pkg.Doc, sectionLevel)

	if r.hasAPI() {
		r.buf.WriteString("\n## API reference\n\n")
		r.values(r.pkg.Consts, "Constants")
		r.values(r.pkg.Vars, "Variables")

		for _, f := range r.pkg.Funcs {
			r.fn(f, f.Name)
		}

		for _, t := range r.pkg.Types {
			r.typ(t)
		}
	}

	return r.buf.Bytes()
}

func sidebarLabel(rel string) string {
	if rel == "" {
		return "gas (core)"
	}

	parts := strings.Split(rel, "/")
	if len(parts) == 1 { // a module's own package
		return "Overview"
	}

	return parts[len(parts)-1]
}

func (r *renderer) hasAPI() bool {
	return len(r.pkg.Consts)+len(r.pkg.Vars)+len(r.pkg.Funcs)+len(r.pkg.Types) > 0
}

func (r *renderer) values(vs []*doc.Value, label string) {
	if len(vs) == 0 {
		return
	}

	fmt.Fprintf(&r.buf, "### %s\n\n", label)

	for _, v := range vs {
		r.value(v)
	}
}

func (r *renderer) value(v *doc.Value) {
	for _, n := range v.Names {
		fmt.Fprintf(&r.buf, "<a id=%q></a>", n)
	}

	r.buf.WriteString("\n\n")
	r.code(v.Decl)
	r.doc(v.Doc, declLevel)
}

func (r *renderer) fn(f *doc.Func, id string) {
	head := f.Name
	if f.Recv != "" {
		head = "(" + f.Recv + ") " + f.Name
	}

	fmt.Fprintf(&r.buf, "<a id=%q></a>\n\n#### func %s\n\n", id, head)

	decl := *f.Decl

	decl.Doc, decl.Body = nil, nil
	r.code(&decl)
	r.doc(f.Doc, funcLevel)
}

func (r *renderer) typ(t *doc.Type) {
	fmt.Fprintf(&r.buf, "<a id=%q></a>", t.Name)

	// An interface's methods are part of its declaration, not methods go/doc
	// lists, so each gets its anchor here for doc links to land on.
	for _, spec := range t.Decl.Specs {
		ts, ok := spec.(*ast.TypeSpec)
		if !ok {
			continue
		}

		it, ok := ts.Type.(*ast.InterfaceType)
		if !ok {
			continue
		}

		for _, m := range it.Methods.List {
			for _, n := range m.Names {
				fmt.Fprintf(&r.buf, "<a id=%q></a>", t.Name+"."+n.Name)
			}
		}
	}

	fmt.Fprintf(&r.buf, "\n\n### type %s\n\n", t.Name)

	decl := *t.Decl

	decl.Doc = nil
	r.code(&decl)
	r.doc(t.Doc, declLevel)

	for _, v := range t.Consts {
		r.value(v)
	}

	for _, v := range t.Vars {
		r.value(v)
	}

	for _, f := range t.Funcs {
		r.fn(f, f.Name)
	}

	for _, m := range t.Methods {
		r.fn(m, t.Name+"."+m.Name)
	}
}

func (r *renderer) code(node any) {
	var b bytes.Buffer

	const tabWidth = 4

	cfg := printer.Config{Mode: printer.UseSpaces | printer.TabIndent, Tabwidth: tabWidth}

	_ = cfg.Fprint(&b, r.fset, node)

	fmt.Fprintf(&r.buf, "```go\n%s\n```\n\n", b.String())
}

// doc renders a doc comment, its headings at level.
func (r *renderer) doc(text string, level int) {
	if strings.TrimSpace(text) == "" {
		return
	}

	d := r.pkg.Parser().Parse(text)

	for _, b := range d.Content {
		r.block(b, level, "")
	}
}

func (r *renderer) block(b comment.Block, level int, indent string) {
	switch b := b.(type) {
	case *comment.Heading:
		fmt.Fprintf(&r.buf, "%s %s\n\n", strings.Repeat("#", level), r.inline(b.Text))
	case *comment.Paragraph:
		fmt.Fprintf(&r.buf, "%s%s\n\n", indent, r.inline(b.Text))
	case *comment.Code:
		fmt.Fprintf(&r.buf, "%s```go\n", indent)

		for l := range strings.SplitSeq(strings.TrimRight(b.Text, "\n"), "\n") {
			fmt.Fprintf(&r.buf, "%s%s\n", indent, l)
		}

		fmt.Fprintf(&r.buf, "%s```\n\n", indent)
	case *comment.List:
		r.list(b, level, indent)
	}
}

func (r *renderer) list(l *comment.List, level int, indent string) {
	for i, it := range l.Items {
		marker := "- "
		if it.Number != "" {
			marker = fmt.Sprintf("%d. ", i+1)
		}

		for j, c := range it.Content {
			p, ok := c.(*comment.Paragraph)
			if !ok {
				r.block(c, level, indent+"  ")

				continue
			}

			pre := indent + "  "
			if j == 0 {
				pre = indent + marker
			}

			fmt.Fprintf(&r.buf, "%s%s\n", pre, r.inline(p.Text))
		}
	}

	r.buf.WriteString("\n")
}

func (r *renderer) inline(ts []comment.Text) string {
	var b strings.Builder

	// inCode tracks the backtick code spans across the pieces of the text:
	// go/doc makes a URL a link even inside one.
	inCode := false

	for _, t := range ts {
		switch t := t.(type) {
		case comment.Plain:
			b.WriteString(r.escape(string(t), &inCode))
		case comment.Italic:
			fmt.Fprintf(&b, "*%s*", r.escape(string(t), &inCode))
		case *comment.Link:
			if inCode {
				b.WriteString(plainText(t.Text))

				continue
			}

			fmt.Fprintf(&b, "[%s](%s)", r.inline(t.Text), t.URL)
		case *comment.DocLink:
			if r.skill {
				// A name to look up with go doc.
				fmt.Fprintf(&b, "`%s`", plainText(t.Text))

				continue
			}

			fmt.Fprintf(&b, "[%s](%s)", r.inline(t.Text), r.linkURL(t))
		}
	}

	return b.String()
}

// linkURL sends a doc link to its anchor on this page, to another reference
// page for a package of the repository, and to pkg.go.dev otherwise.
func (r *renderer) linkURL(l *comment.DocLink) string {
	frag := l.Name
	if l.Recv != "" {
		frag = l.Recv + "." + l.Name
	}

	path := l.ImportPath
	if path == "" {
		path = r.path
	}

	if !r.known[path] {
		return l.DefaultURL("https://pkg.go.dev")
	}

	target := ""
	if path != r.path {
		target = siteBase + strings.TrimSuffix(pagePath(path, r.known), "/index") + "/"
	}

	if frag == "" {
		return target
	}

	return target + "#" + frag
}

// plainText returns the text of ts without markup.
func plainText(ts []comment.Text) string {
	var b strings.Builder

	for _, t := range ts {
		switch t := t.(type) {
		case comment.Plain:
			b.WriteString(string(t))
		case comment.Italic:
			b.WriteString(string(t))
		}
	}

	return b.String()
}

// escape escapes s for the site, and leaves it as it is for the skill,
// which an agent reads as raw text. inCode is whether s starts inside a
// backtick code span, and is left as whether it ends inside one.
func (r *renderer) escape(s string, inCode *bool) string {
	out := escape(s, inCode)
	if r.skill {
		return s
	}

	return out
}

// escape keeps Markdown from reading text as markup: outside backtick code
// spans, which Gas's doc comments use for literal text, it escapes the
// characters that start emphasis, HTML or links. inCode is whether s
// starts inside a code span, and is left as whether it ends inside one.
func escape(s string, inCode *bool) string {
	var b strings.Builder

	for _, c := range s {
		if c == '`' {
			*inCode = !*inCode

			b.WriteRune(c)

			continue
		}

		if !*inCode && strings.ContainsRune(`*_<>[]\`, c) {
			b.WriteRune('\\')
		}

		b.WriteRune(c)
	}

	return b.String()
}
