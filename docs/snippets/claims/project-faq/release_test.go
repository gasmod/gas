package projectfaq_test

import (
	"encoding/json"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// Docs: project/faq.mdx#is-it-production-ready
// "Gas is pre-1.0."
func TestPreOnePointZero(t *testing.T) {
	t.Parallel()
	work := readFile(t, filepath.Join(repoRoot(t), "go.work"))
	m := regexp.MustCompile(`(?m)^\s*github\.com/gasmod/gas (v\S+) =>`).FindStringSubmatch(work)
	if m == nil {
		t.Fatal("go.work does not pin github.com/gasmod/gas")
	}
	if !strings.HasPrefix(m[1], "v0.") {
		t.Fatalf("core is at %s, want a v0 version", m[1])
	}
}

// changelogSection returns the body of the CHANGELOG.md section whose heading
// names version, or "" when there is none.
func changelogSection(src, version string) string {
	lines := strings.Split(src, "\n")
	start := slices.IndexFunc(lines, func(l string) bool {
		return strings.HasPrefix(l, "## ") && strings.Contains(l, version)
	})
	if start < 0 {
		return ""
	}
	var out []string
	for _, l := range lines[start+1:] {
		if strings.HasPrefix(l, "## ") {
			break
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

// Docs: project/faq.mdx#is-it-production-ready
// "Its API still changes between minor releases: the changelogs
// mark those changes **BREAKING**, and v0.6.0 has some."
// Docs: project/faq.mdx#how-is-gas-versioned
// "Each module records
// its changes in its own `CHANGELOG.md`, and the core's is the
// [root CHANGELOG]"
// Assumption: until v0.6.0 is tagged, its changes sit under [Unreleased],
// so a [0.6.0] section or, failing that, [Unreleased] counts as v0.6.0's.
func TestChangelogsAndBreakingV060(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	breaking := 0
	for _, dir := range append([]string{"."}, modules...) {
		src, err := os.ReadFile(filepath.Join(root, dir, "CHANGELOG.md"))
		if err != nil {
			t.Errorf("%s has no CHANGELOG.md: %v", dir, err)
			continue
		}
		sec := changelogSection(string(src), "[0.6.0]")
		if sec == "" {
			sec = changelogSection(string(src), "[Unreleased]")
		}
		if strings.Contains(sec, "**BREAKING") {
			breaking++
		}
	}
	if breaking == 0 {
		t.Error("no changelog marks a v0.6.0 change BREAKING")
	}
}

// Docs: project/faq.mdx#how-is-gas-versioned
// "The repository is a
// monorepo: the core is `github.com/gasmod/gas`, and each official module (for
// example `github.com/gasmod/gas/queue`) is a separate Go module in its own
// directory."
func TestMonorepoOfSeparateModules(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	if !slices.Contains(strings.Split(readFile(t, filepath.Join(root, "go.mod")), "\n"), "module github.com/gasmod/gas") {
		t.Error("the root go.mod does not declare module github.com/gasmod/gas")
	}
	for _, dir := range modules {
		mod := "github.com/gasmod/gas/" + dir
		if !slices.Contains(strings.Split(readFile(t, filepath.Join(root, dir, "go.mod")), "\n"), "module "+mod) {
			t.Errorf("%s/go.mod does not declare module %s", dir, mod)
		}
	}
}

// gasVersions maps each Gas module version named in a go.mod require or a
// go.work replace to the places that name it.
func gasVersions(t *testing.T, root string) map[string][]string {
	t.Helper()
	re := regexp.MustCompile(`(?m)^\s*(?:require\s+)?(github\.com/gasmod/gas(?:/[A-Za-z0-9._-]+)*)\s+(v\d+\.\d+\.\d+\S*)`)
	versions := map[string][]string{}
	for _, f := range append(goModFiles(t, root), "go.work") {
		for _, m := range re.FindAllStringSubmatch(readFile(t, filepath.Join(root, f)), -1) {
			versions[m[2]] = append(versions[m[2]], f+": "+m[1])
		}
	}
	return versions
}

// Docs: project/faq.mdx#how-is-gas-versioned
// "All modules share one version and are released together."
// and "Mixing versions across modules is not supported, so upgrade them
// together."
// Every requirement on a Gas module, in every go.mod and in the go.work
// replaces, names one version, and verify-versions.sh accepts it.
func TestModulesShareOneVersion(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	versions := gasVersions(t, root)
	if len(versions) != 1 {
		t.Fatalf("Gas module requirements name %d versions, want one: %v", len(versions), versions)
	}
	v := slices.Collect(maps.Keys(versions))[0]
	out, err := exec.CommandContext(t.Context(), "bash", filepath.Join(root, "scripts", "verify-versions.sh"), v).CombinedOutput()
	if err != nil {
		t.Fatalf("verify-versions.sh %s: %v\n%s", v, err, out)
	}
}

// Docs: project/faq.mdx#how-is-a-release-made
// "Releases are cut by `.github/workflows/release.yml` when a `v*` tag is pushed:"
func TestReleaseRunsOnVTagPush(t *testing.T) {
	t.Parallel()
	wf := readFile(t, filepath.Join(repoRoot(t), ".github", "workflows", "release.yml"))
	on := block(t, wf, regexp.MustCompile(`^on:\s*$`))
	if !regexp.MustCompile(`(?s)^\s*push:\s*\n\s*tags:\s*\n\s*- 'v\*'\s*$`).MatchString(on) {
		t.Fatalf("release.yml trigger is not a push of v* tags only:\n%s", on)
	}
}

// copyVersionTree copies the scripts and every go.mod and go.work of the
// repository into a temporary directory, so the version scripts can rewrite
// it.
func copyVersionTree(t *testing.T) string {
	t.Helper()
	root := repoRoot(t)
	dst := t.TempDir()
	files := append(goModFiles(t, root), "go.work", "scripts/bump.sh", "scripts/verify-versions.sh")
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(dst, filepath.Dir(f)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, f), data, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dst
}

func runScript(t *testing.T, dir, script string, args ...string) (string, error) {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "bash", append([]string{filepath.Join(dir, "scripts", script)}, args...)...).CombinedOutput()
	return string(out), err
}

// Docs: project/faq.mdx#how-is-a-release-made
// "`scripts/verify-versions.sh` checks that every `github.com/gasmod/gas...`
// requirement in every `go.mod`, and every replace in `go.work`, names the
// version being released, and fails otherwise. `scripts/bump.sh` is the script
// that rewrites them all before tagging."
func TestVerifyVersionsAndBump(t *testing.T) {
	t.Parallel()
	release := regexp.MustCompile(`(?m)^\s*-\s+name:.*\n\s*run: \./scripts/verify-versions\.sh "\$\{\{ github\.ref_name \}\}"\s*$`)
	wf := readFile(t, filepath.Join(repoRoot(t), ".github", "workflows", "release.yml"))
	if !release.MatchString(job(t, wf, "verify-versions")) {
		t.Error("release.yml verify-versions job does not run verify-versions.sh with the pushed tag")
	}

	t.Run("fails on a go.mod requirement", func(t *testing.T) {
		t.Parallel()
		dir := copyVersionTree(t)
		if out, err := runScript(t, dir, "verify-versions.sh", "v9.9.9"); err == nil {
			t.Fatalf("verify-versions.sh v9.9.9 passed on the current tree:\n%s", out)
		}
	})
	t.Run("fails on a go.work replace alone", func(t *testing.T) {
		t.Parallel()
		dir := copyVersionTree(t)
		if out, err := runScript(t, dir, "bump.sh", "v9.9.9"); err != nil {
			t.Fatalf("bump.sh: %v\n%s", err, out)
		}
		work := filepath.Join(dir, "go.work")
		src := readFile(t, work)
		changed := strings.Replace(src, "github.com/gasmod/gas/queue v9.9.9 =>", "github.com/gasmod/gas/queue v9.9.8 =>", 1)
		if changed == src {
			t.Fatal("go.work has no queue replace to change")
		}
		if err := os.WriteFile(work, []byte(changed), 0o644); err != nil {
			t.Fatal(err)
		}
		out, err := runScript(t, dir, "verify-versions.sh", "v9.9.9")
		if err == nil || !strings.Contains(out, "go.work") {
			t.Fatalf("verify-versions.sh with one stale go.work replace: err=%v, want a failure naming go.work\n%s", err, out)
		}
	})
	t.Run("bump rewrites every requirement and replace", func(t *testing.T) {
		t.Parallel()
		dir := copyVersionTree(t)
		if out, err := runScript(t, dir, "bump.sh", "v9.9.9"); err != nil {
			t.Fatalf("bump.sh: %v\n%s", err, out)
		}
		if versions := gasVersions(t, dir); len(versions) != 1 || versions["v9.9.9"] == nil {
			t.Fatalf("after bump.sh v9.9.9 the tree names %v", versions)
		}
		if out, err := runScript(t, dir, "verify-versions.sh", "v9.9.9"); err != nil {
			t.Fatalf("verify-versions.sh after bump.sh: %v\n%s", err, out)
		}
	})
}

// Docs: project/faq.mdx#how-is-a-release-made
// "golangci-lint, `go test -race`, `go vet`, and govulncheck run on every
// module except the examples and the docs snippets."
func TestReleaseChecksEveryModuleButExamplesAndDocs(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	wf := readFile(t, filepath.Join(root, ".github", "workflows", "release.yml"))
	for name, want := range map[string]string{
		"golangci-lint": "golangci/golangci-lint-action@",
		"go-test":       "go test -v -race ./...",
		"go-vet":        "go vet ./...",
		"govulncheck":   "golang.org/x/vuln/cmd/govulncheck@",
	} {
		j := job(t, wf, name)
		if !strings.Contains(j, want) || !strings.Contains(j, "needs: detect-modules") ||
			!strings.Contains(j, "fromJSON(needs.detect-modules.outputs.modules)") ||
			!strings.Contains(j, "${{ matrix.modules }}") {
			t.Errorf("job %s does not run %q over the detected modules:\n%s", name, want, j)
		}
	}

	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is not installed; the module list the workflow computes is not checked")
	}
	set := block(t, job(t, wf, "detect-modules"), regexp.MustCompile(`^\s*- id: set-modules\s*$`))
	cmd, ok := strings.CutPrefix(strings.TrimSpace(set), "run: ")
	if !ok {
		t.Fatalf("set-modules step has no one-line run:\n%s", set)
	}
	outFile := filepath.Join(t.TempDir(), "output")
	c := exec.CommandContext(t.Context(), "bash", "-c", cmd)
	c.Dir = root
	c.Env = append(os.Environ(), "GITHUB_OUTPUT="+outFile)
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("set-modules: %v\n%s", err, out)
	}
	got, ok := strings.CutPrefix(strings.TrimSpace(readFile(t, outFile)), "modules=")
	if !ok {
		t.Fatalf("set-modules wrote %q", readFile(t, outFile))
	}
	var dirs []string
	if err := json.Unmarshal([]byte(got), &dirs); err != nil {
		t.Fatal(err)
	}
	want := []string{root}
	for _, m := range modules {
		want = append(want, filepath.Join(root, m))
	}
	slices.Sort(dirs)
	slices.Sort(want)
	if !slices.Equal(dirs, want) {
		t.Fatalf("release checks run in %v, want core and the official modules %v", dirs, want)
	}
}

// Docs: project/faq.mdx#how-is-a-release-made
// "When all pass, the workflow tags each module directory with
// `<dir>/<tag>`; the core keeps the tag that was pushed."
// Docs: project/faq.mdx#how-is-gas-versioned
// "A release tags the core `vX.Y.Z` and each module `<dir>/vX.Y.Z`, such
// as `queue/vX.Y.Z`"
// The tag job's naming step runs as written, for the core and for queue.
func TestReleaseTagsModuleDirectories(t *testing.T) {
	t.Parallel()
	wf := readFile(t, filepath.Join(repoRoot(t), ".github", "workflows", "release.yml"))
	tag := job(t, wf, "tag")
	if !strings.Contains(tag, "needs: [ detect-modules, golangci-lint, go-test, go-vet, govulncheck ]") {
		t.Errorf("tag job does not wait for every check:\n%s", tag)
	}
	if !strings.Contains(tag, "TAG: ${{ steps.mod.outputs.name }}/${{ github.ref_name }}") ||
		!strings.Contains(tag, "if: steps.mod.outputs.skip == 'false'") {
		t.Errorf("tag job does not push <dir>/<tag> for non-root modules:\n%s", tag)
	}
	script := literalScript(t, tag, regexp.MustCompile(`^\s*- id: mod\s*$`))
	const ws = "/home/runner/work/gas/gas"
	for module, want := range map[string]string{ws: "skip=true", ws + "/queue": "skip=false\nname=queue"} {
		out := filepath.Join(t.TempDir(), "output")
		c := exec.CommandContext(t.Context(), "bash", "-c", script)
		c.Env = append(os.Environ(), "MODULE="+module, "GITHUB_WORKSPACE="+ws, "GITHUB_OUTPUT="+out)
		if b, err := c.CombinedOutput(); err != nil {
			t.Fatalf("mod step for %s: %v\n%s", module, err, b)
		}
		if got := strings.TrimSpace(readFile(t, out)); got != want {
			t.Errorf("mod step for %s wrote %q, want %q", module, got, want)
		}
	}
}

// Docs: project/faq.mdx#how-is-a-release-made
// "A GitHub release is published with generated release notes."
func TestReleasePublishedWithGeneratedNotes(t *testing.T) {
	t.Parallel()
	wf := readFile(t, filepath.Join(repoRoot(t), ".github", "workflows", "release.yml"))
	rel := job(t, wf, "release")
	for _, want := range []string{"needs: tag", "softprops/action-gh-release@", "generate_release_notes: true", "draft: false"} {
		if !strings.Contains(rel, want) {
			t.Errorf("release job lacks %q:\n%s", want, rel)
		}
	}
}

// Docs: project/faq.mdx#which-go-version-do-i-need
// "Every module requires Go 1.27.3, through the `go` directive in its `go.mod`."
// Docs: project/faq.mdx#which-go-version-do-i-need
// "`GOTOOLCHAIN=auto` switches to 1.27.3"
// A toolchain directive would make auto switch to that version instead, so
// none may name another.
func TestEveryModuleRequiresGo1273(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, f := range goModFiles(t, root) {
		src := readFile(t, filepath.Join(root, f))
		if !regexp.MustCompile(`(?m)^go 1\.27\.3$`).MatchString(src) {
			t.Errorf("%s does not have the directive go 1.27.3", f)
		}
		if m := regexp.MustCompile(`(?m)^toolchain (\S+)$`).FindStringSubmatch(src); m != nil && m[1] != "go1.27.3" {
			t.Errorf("%s names toolchain %s", f, m[1])
		}
	}
}

// Docs: project/faq.mdx#can-i-use-one-module-without-the-rest
// "Each module is its own Go module, so `go get` adds that module and what it
// requires, not the whole repository."
// No module requires every other official module.
func TestModuleRequiresAreNotTheWholeRepository(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, dir := range modules {
		src := readFile(t, filepath.Join(root, dir, "go.mod"))
		n := 0
		for _, other := range modules {
			if other != dir && regexp.MustCompile(`(?m)^\s*(?:require\s+)?github\.com/gasmod/gas/`+other+`\s+v`).MatchString(src) {
				n++
			}
		}
		if n == len(modules)-1 {
			t.Errorf("%s/go.mod requires every other module", dir)
		}
	}
}

// Docs: project/faq.mdx#where-is-the-api-reference
// "and each module page here links to its own packages."
// Docs: project/faq.mdx#where-is-the-api-reference
// "Each module's README points to both."
func TestModulePagesAndReadmesLinkReference(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	pages := map[string]string{
		"auth": "auth", "cache": "cache", "config": "config", "database": "database",
		"email": "email", "log": "logging", "migrate": "migrations", "queue": "queue",
		"storage": "storage", "template": "templates", "ui": "ui",
	}
	for dir, page := range pages {
		pkg := "https://pkg.go.dev/github.com/gasmod/gas/" + dir
		src := readFile(t, filepath.Join(root, "docs", "src", "content", "docs", "modules", page+".mdx"))
		if !strings.Contains(src, "("+pkg+")") {
			t.Errorf("modules/%s.mdx does not link %s", page, pkg)
		}
		readme := readFile(t, filepath.Join(root, dir, "README.md"))
		if !strings.Contains(readme, pkg) || !strings.Contains(readme, "https://gasmod.github.io/gas/") {
			t.Errorf("%s/README.md does not link both %s and the docs site", dir, pkg)
		}
	}
}

// Docs: project/faq.mdx#where-do-i-report-a-bug-or-ask-for-a-feature
// "Open an issue on [GitHub](https://github.com/gasmod/gas/issues), as described in
// [Contributing](/gas/project/contributing/)."
// and "Report a security vulnerability
// privately, as [`SECURITY.md`](https://github.com/gasmod/gas/blob/main/SECURITY.md)
// describes, never in a public issue."
func TestIssuesAndSecurityReporting(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	contributing := readFile(t, filepath.Join(root, "docs", "src", "content", "docs", "project", "contributing.mdx"))
	if !strings.Contains(contributing, "## Filing issues") {
		t.Error("the Contributing page has no section on filing issues")
	}
	sec := readFile(t, filepath.Join(root, "SECURITY.md"))
	if !strings.Contains(sec, "**Do not** file a public GitHub issue") || !strings.Contains(sec, "private vulnerability reporting") {
		t.Error("SECURITY.md does not describe private reporting instead of a public issue")
	}
}
