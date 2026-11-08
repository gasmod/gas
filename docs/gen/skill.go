package main

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/template"
)

// skillTemplate is SKILL.md, with the installation guide and the package
// index to fill in.
//
//go:embed skill.md.tmpl
var skillTemplate string

// installPage is the site's installation page, relative to the repo root,
// which SKILL.md carries.
const installPage = "docs/src/content/docs/start/installation.md"

// siteURL is where the docs site is published.
const siteURL = "https://gasmod.github.io/gas/"

// skillModule is a module of the package index of SKILL.md.
type skillModule struct {
	Name     string
	Packages []skillPackage
}

// skillPackage is a package of the package index of SKILL.md.
type skillPackage struct {
	Path     string
	File     string
	Synopsis string
}

// runSkill writes the gas skill to out: SKILL.md and a reference per
// package.
func runSkill(root, out string) error {
	dirs, known, err := prepare(root, out)
	if err != nil {
		return err
	}

	modules := map[string]*skillModule{}

	for _, d := range dirs {
		pkg, _, pErr := readPackage(root, d)
		if pErr != nil {
			return fmt.Errorf("%s: %w", d, pErr)
		}

		file := referenceFile(pkg.path)
		r := &renderer{pkg: pkg.doc, path: pkg.path, known: known, fset: pkg.fset, skill: true}

		err = writeFile(filepath.Join(out, filepath.FromSlash(file)), r.reference())
		if err != nil {
			return fmt.Errorf("%s: %w", d, err)
		}

		name := moduleName(pkg.path)
		if modules[name] == nil {
			modules[name] = &skillModule{Name: name}
		}

		modules[name].Packages = append(modules[name].Packages, skillPackage{
			Path:     pkg.path,
			File:     file,
			Synopsis: pkg.doc.Synopsis(pkg.doc.Doc),
		})
	}

	skill, err := skillMD(root, modules)
	if err != nil {
		return err
	}

	err = writeFile(filepath.Join(out, "SKILL.md"), skill)
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "gen: wrote the skill with %d package references\n", len(dirs))

	return nil
}

// referenceFile returns the reference of an import path, relative to the
// skill directory: references/gas.md for the core, and else the path below
// the module path.
func referenceFile(path string) string {
	rel := strings.TrimPrefix(strings.TrimPrefix(path, modulePath), "/")
	if rel == "" {
		rel = coreName
	}

	return "references/" + rel + ".md"
}

// moduleName returns the name of the module of an import path: core for
// the core package, and else the first element below the module path.
func moduleName(path string) string {
	rel := strings.TrimPrefix(strings.TrimPrefix(path, modulePath), "/")
	if rel == "" {
		return "core"
	}

	name, _, _ := strings.Cut(rel, "/")

	return name
}

// reference renders the package's overview for the skill: its import path
// and doc comment, then where its API is.
func (r *renderer) reference() []byte {
	fmt.Fprintf(&r.buf, "# %s\n\n", r.path)
	fmt.Fprintf(&r.buf, "```go\nimport %q\n```\n\n", r.path)

	r.doc(r.pkg.Doc, sectionLevel)

	if r.hasAPI() {
		fmt.Fprintf(
			&r.buf,
			"## API\n\nRun `go doc -all %s` for every declaration, or `go doc %s.<Name>` for one.\n",
			r.path,
			r.path,
		)
	}

	return r.buf.Bytes()
}

// skillMD renders SKILL.md from the template, the installation page and
// modules, the core first and the others by name.
func skillMD(root string, modules map[string]*skillModule) ([]byte, error) {
	install, err := installGuide(root)
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(modules))
	for name := range modules {
		if name != "core" {
			names = append(names, name)
		}
	}

	sort.Strings(names)

	ordered := []*skillModule{modules["core"]}
	for _, name := range names {
		ordered = append(ordered, modules[name])
	}

	tmpl, err := template.New("SKILL.md").Parse(skillTemplate)
	if err != nil {
		return nil, fmt.Errorf("parse skill template: %w", err)
	}

	var b bytes.Buffer

	err = tmpl.Execute(&b, struct {
		Install string
		Modules []*skillModule
	}{install, ordered})
	if err != nil {
		return nil, fmt.Errorf("render SKILL.md: %w", err)
	}

	return b.Bytes(), nil
}

var (
	// frontMatter matches the front matter of a site page.
	frontMatter = regexp.MustCompile(`(?s)\A---\n.*?\n---\n+`)
	// referenceLink matches a link to a reference page of the site.
	referenceLink = regexp.MustCompile(`\]\(/gas/reference/([^)#]*?)/?\)`)
	// siteLink matches any other link to a page of the site.
	siteLink = regexp.MustCompile(`\]\(/gas/([^)]*)\)`)
	// heading matches a Markdown heading outside a code block.
	heading = regexp.MustCompile(`(?m)^(#+) `)
)

// installGuide returns the site's installation page as a section of
// SKILL.md: without its front matter, its headings a level down, its links
// to reference pages sent to the skill's references and its other links
// to the published site.
func installGuide(root string) (string, error) {
	//nolint:gosec // a page of the caller's own repo.
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(installPage)))
	if err != nil {
		return "", fmt.Errorf("read the installation page: %w", err)
	}

	page := frontMatter.ReplaceAllString(string(data), "")

	page = referenceLink.ReplaceAllString(page, "](references/$1.md)")
	page = siteLink.ReplaceAllString(page, "]("+siteURL+"$1)")

	var b strings.Builder

	inCode := false

	for line := range strings.SplitSeq(page, "\n") {
		if strings.HasPrefix(line, "```") {
			inCode = !inCode
		}

		if !inCode {
			line = heading.ReplaceAllString(line, "#$1 ")
		}

		b.WriteString(line + "\n")
	}

	return strings.TrimSpace(b.String()), nil
}
