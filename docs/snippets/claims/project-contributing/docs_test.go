package projectcontributing_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// Docs: project/contributing.mdx#working-on-these-docs
// "The site is [Starlight](https://starlight.astro.build/) and lives in `docs/`. It
// uses npm, with the lock file in `docs/package-lock.json`:"
// and "npm run dev # local preview
// npm run build # what CI runs"
func TestStarlightSiteWithNpm(t *testing.T) {
	t.Parallel()
	docs := filepath.Join(repoRoot(t), "docs")
	cfg := readFile(t, filepath.Join(docs, "astro.config.mjs"))
	if !strings.Contains(cfg, "from '@astrojs/starlight'") || !strings.Contains(cfg, "starlight({") {
		t.Error("docs/astro.config.mjs does not build a Starlight site")
	}
	var lock struct {
		Name            string `json:"name"`
		LockfileVersion int    `json:"lockfileVersion"`
	}
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(docs, "package-lock.json"))), &lock); err != nil || lock.LockfileVersion == 0 {
		t.Errorf("docs/package-lock.json is not an npm lock file: %v", err)
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(docs, "package.json"))), &pkg); err != nil {
		t.Fatal(err)
	}
	if pkg.Scripts["dev"] != "astro dev" || pkg.Scripts["build"] != "astro build" {
		t.Errorf("docs/package.json scripts dev=%q build=%q, want astro dev and astro build", pkg.Scripts["dev"], pkg.Scripts["build"])
	}
	wf := readFile(t, filepath.Join(repoRoot(t), ".github", "workflows", "docs.yml"))
	if !strings.Contains(job(t, wf, "build"), "run: npm run build") {
		t.Error("the docs workflow does not run npm run build")
	}
}

// runRegion calls region() from docs/src/lib/snippet.ts in node on source
// and name, and returns its result, or the error message it threw.
func runRegion(t *testing.T, source, name string) (string, bool) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	lib := filepath.Join(repoRoot(t), "docs", "src", "lib", "snippet.ts")
	script := `import { region } from ` + jsString(lib) + `;
const [src, name] = JSON.parse(process.env.REGION_ARGS);
try { process.stdout.write(JSON.stringify({ ok: region(src, name) })); }
catch (e) { process.stdout.write(JSON.stringify({ err: e.message })); }`
	c := exec.CommandContext(t.Context(), node, "--no-warnings", "--input-type=module", "-e", script)
	args, _ := json.Marshal([]string{source, name})
	c.Env = append(os.Environ(), "REGION_ARGS="+string(args))
	out, err := c.Output()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, out)
	}
	var res struct {
		Ok  *string `json:"ok"`
		Err string  `json:"err"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("node printed %q: %v", out, err)
	}
	if res.Ok == nil {
		return res.Err, false
	}
	return *res.Ok, true
}

func jsString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// Docs: project/contributing.mdx#code-samples-are-compiled-go
// "`region()` in `docs/src/lib/snippet.ts` returns the lines between the markers,
// dedented, with nested markers dropped. A missing or unclosed region throws"
// Docs: project/contributing.mdx#working-on-these-docs
// "The build fails when a page imports a snippet region that does not exist"
// The build half follows from the throw, as pages call region() at build
// time; the site build itself is not run.
func TestRegion(t *testing.T) {
	t.Parallel()
	src := "package main\n\nfunc main() {\n\t// #region r\n\ta := 1\n\t\tb := a\n\t// #endregion r\n\t// #region open\n\tc := 2\n}\n"
	if got, ok := runRegion(t, src, "r"); !ok || got != "a := 1\n\tb := a" {
		t.Errorf("region(r) = %q, %v; want the lines between the markers, dedented", got, ok)
	}
	if got, ok := runRegion(t, src, "missing"); ok || !strings.Contains(got, "not found") {
		t.Errorf("region(missing) = %q, %v; want a throw", got, ok)
	}
	if got, ok := runRegion(t, src, "open"); ok || !strings.Contains(got, "not closed") {
		t.Errorf("region(open) = %q, %v; want a throw", got, ok)
	}
}

// Docs: project/contributing.mdx#code-samples-are-compiled-go
// "`region()` in `docs/src/lib/snippet.ts` returns the lines between the markers,
// dedented, with nested markers dropped."
// A region with another region nested inside it: the outer region holds
// everything up to its own end marker, without the inner markers.
func TestRegionNested(t *testing.T) {
	t.Parallel()
	src := "package main\n\nfunc main() {\n\t// #region outer\n\ta := 1\n\t// #region inner\n\tb := a\n\t// #endregion inner\n\t_ = b\n\t// #endregion outer\n}\n"
	if got, ok := runRegion(t, src, "outer"); !ok || got != "a := 1\nb := a\n_ = b" {
		t.Errorf("region(outer) = %q, %v; want %q", got, ok, "a := 1\nb := a\n_ = b")
	}
	if got, ok := runRegion(t, src, "inner"); !ok || got != "b := a" {
		t.Errorf("region(inner) = %q, %v; want %q", got, ok, "b := a")
	}
}

// sidebarPages returns the page files the site's sidebar lists, relative to
// the content directory.
func sidebarPages(t *testing.T) []string {
	t.Helper()
	cfg := readFile(t, filepath.Join(repoRoot(t), "docs", "astro.config.mjs"))
	var out []string
	for _, m := range regexp.MustCompile(`slug: '([^']+)'`).FindAllStringSubmatch(cfg, -1) {
		out = append(out, m[1]+".mdx")
	}
	if len(out) == 0 {
		t.Fatal("no sidebar slugs in astro.config.mjs")
	}
	return out
}

// Docs: project/contributing.mdx#working-on-these-docs
// "and
// when an internal link does not resolve. Internal links are written with the
// `/gas/` prefix, as in `/gas/modules/queue/`."
// The links validator is installed in the Starlight config, the site's base
// is /gas, and every root-relative Markdown link on the sidebar's pages
// starts with /gas/.
func TestInternalLinks(t *testing.T) {
	t.Parallel()
	docs := filepath.Join(repoRoot(t), "docs")
	cfg := readFile(t, filepath.Join(docs, "astro.config.mjs"))
	if !strings.Contains(cfg, "import starlightLinksValidator from 'starlight-links-validator'") ||
		!regexp.MustCompile(`plugins: \[[^\]]*starlightLinksValidator\(`).MatchString(cfg) {
		t.Error("the Starlight config does not run starlight-links-validator")
	}
	if !strings.Contains(cfg, "base: '/gas'") {
		t.Error("the site's base is not /gas")
	}
	link := regexp.MustCompile(`\]\((/[^)\s]*)\)`)
	for _, p := range sidebarPages(t) {
		src, err := os.ReadFile(filepath.Join(docs, "src", "content", "docs", p))
		if err != nil {
			t.Errorf("sidebar page %s: %v", p, err)
			continue
		}
		for _, m := range link.FindAllStringSubmatch(string(src), -1) {
			if !strings.HasPrefix(m[1], "/gas/") {
				t.Errorf("%s links %s without the /gas/ prefix", p, m[1])
			}
		}
	}
}

// Docs: project/contributing.mdx#code-samples-are-compiled-go
// "It lives in
// `docs/snippets/`, a Go module in the workspace, so `go build ./...` and CI
// catch a sample that drifts from the framework. Each snippet directory is named
// after the page that owns it, `<section>-<slug>` (for example
// `docs/snippets/modules-queue/` for `modules/queue`); the landing page, which
// has no section, uses `docs/snippets/landing/`. A page may also import from
// another page's directory."
// Every snippet directory the sidebar pages or the landing page import from
// is named after one of the pages that import it: <section>-<slug>, or
// landing for index.mdx. The module builds, and CI's test jobs run over every
// workspace module, docs/snippets included.
// Assumption: the owning page is one of those pages that imports from the
// directory; directories only other pages use are not checked.
func TestSnippetsAreCompiledPerPage(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	work := readFile(t, filepath.Join(root, "go.work"))
	if !regexp.MustCompile(`(?m)^\s*\./docs/snippets\s*$`).MatchString(work) {
		t.Error("go.work does not use ./docs/snippets")
	}
	snippetImport := regexp.MustCompile(`^import \w+ from '[./]*/snippets/([^/']+)/[^']*\?raw';$`)
	importers := map[string][]string{} // snippet directory -> pages importing it
	for _, p := range append(sidebarPages(t), "index.mdx") {
		src, err := os.ReadFile(filepath.Join(root, "docs", "src", "content", "docs", p))
		if err != nil {
			continue
		}
		page := strings.ReplaceAll(strings.TrimSuffix(p, ".mdx"), "/", "-")
		if p == "index.mdx" {
			page = "landing"
		}
		inFence := false
		for line := range strings.SplitSeq(string(src), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "```") {
				inFence = !inFence
				continue
			}
			if m := snippetImport.FindStringSubmatch(line); m != nil && !inFence && !slices.Contains(importers[m[1]], page) {
				importers[m[1]] = append(importers[m[1]], page)
			}
		}
	}
	for dir, pages := range importers {
		if !slices.Contains(pages, dir) {
			t.Errorf("docs/snippets/%s/ is imported by %v but named after none of them", dir, pages)
		}
	}

	c := exec.CommandContext(t.Context(), "go", "build", "./...")
	c.Dir = filepath.Join(root, "docs", "snippets")
	if out, err := c.CombinedOutput(); err != nil {
		t.Errorf("go build ./... in docs/snippets: %v\n%s", err, out)
	}

	wf := readFile(t, filepath.Join(root, ".github", "workflows", "test.yml"))
	if !strings.Contains(wf, `echo "all=$(go list -m -json | jq -sc '[.[].Dir]')" >> "$GITHUB_OUTPUT"`) {
		t.Error("test.yml does not list every workspace module, unfiltered, as the check matrix")
	}
	for _, name := range []string{"go-test", "go-vet"} {
		if !strings.Contains(job(t, wf, name), "fromJSON(needs.detect-modules.outputs.all)") {
			t.Errorf("test.yml %s does not run over every module", name)
		}
	}
}

// Docs: project/contributing.mdx#every-claim-has-a-test
// "Each behavioral statement on a page is backed by a Go test under
// `docs/snippets/claims/<section>-<slug>/`, in a package named
// `<section><slug>_test` with the hyphens dropped (`claims/modules-queue` is
// package `modulesqueue_test`)."
func TestClaimPackageNames(t *testing.T) {
	t.Parallel()
	claims := filepath.Join(repoRoot(t), "docs", "snippets", "claims")
	entries, err := os.ReadDir(claims)
	if err != nil {
		t.Fatal(err)
	}
	pkg := regexp.MustCompile(`(?m)^package (\w+)$`)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		want := strings.ReplaceAll(e.Name(), "-", "") + "_test"
		files, _ := filepath.Glob(filepath.Join(claims, e.Name(), "*.go"))
		for _, f := range files {
			if m := pkg.FindStringSubmatch(readFile(t, f)); m == nil || m[1] != want {
				t.Errorf("%s/%s is not in package %s", e.Name(), filepath.Base(f), want)
			}
		}
	}
}
