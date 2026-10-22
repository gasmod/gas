package projectcontributing_test

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// fixturePage is a page for the TestQuotes runs below. It has bold, code
// spans, a link, a table, and a code region imported with <Code>.
const fixturePage = "---\ntitle: Fixture\n---\n\n" +
	"import { Code } from '@astrojs/starlight/components';\n" +
	"import snip from '../../../../snippets/fixture/main.go?raw';\n" +
	"import { region } from '../../../lib/snippet';\n\n" +
	"## First heading\n\n" +
	"The **router** answers with a\n[link](/gas/x/) and `code` here, then more words follow.\n\n" +
	"| Key | Default |\n|---|---|\n| `port` | 8080 |\n\n" +
	"<Code code={region(snip, 'r')} lang=\"go\" />\n"

const fixtureSnippet = "package main\n\n// #region r\nfunc hello() {} // greeting from the snippet\n// #endregion r\n"

// quotesRun copies TestQuotes into a scratch tree laid out like docs/, with
// the fixture page at sec/page.mdx and claims, a claims file's text, and
// runs it. It returns the output and whether TestQuotes passed.
func quotesRun(t *testing.T, claims string) (string, bool) {
	t.Helper()
	snippets := filepath.Join(repoRoot(t), "docs", "snippets")
	tree := t.TempDir()
	writeFile(t, filepath.Join(tree, "docs", "src", "content", "docs", "sec", "page.mdx"), fixturePage)
	writeFile(t, filepath.Join(tree, "docs", "snippets", "fixture", "main.go"), fixtureSnippet)
	writeFile(t, filepath.Join(tree, "docs", "snippets", "go.mod"), "module quotesfixture\n\ngo 1.27.3\n")
	for _, f := range []string{"quotes_test.go", "slugger_table_test.go"} {
		writeFile(t, filepath.Join(tree, "docs", "snippets", "claims", f), readFile(t, filepath.Join(snippets, "claims", f)))
	}
	// Tags in claims may be written "@docs ", so that this file's own source
	// holds no tag for the real TestQuotes to check.
	claims = strings.ReplaceAll(claims, "@docs ", "// Docs: ")
	writeFile(t, filepath.Join(tree, "docs", "snippets", "claims", "case", "case_test.go"), "package case_test\n\n"+claims)
	out, err := goTest(t, filepath.Join(tree, "docs", "snippets", "claims"),
		env([]string{"GOWORK", "GOFLAGS"}, "GOWORK=off"), "-run", "^TestQuotes$", ".")
	return out, err == nil
}

// Docs: project/contributing.mdx#every-claim-has-a-test
// "The test carries a `// Docs:` tag naming the page,
// optionally with a heading anchor, and the comment lines right after it quote
// the claim:"
// Docs: project/contributing.mdx#every-claim-has-a-test
// "A quote may span several comment lines, `...` marks
// a gap between fragments that must appear in order, and ` / ` joins text that is
// on separate lines or table cells on the page. Whitespace, Markdown markup, and
// link targets are ignored, and code imported into a page with `<Code>` counts as
// page text."
func TestQuotesMatching(t *testing.T) {
	t.Parallel()
	out, ok := quotesRun(t, `@docs sec/page.mdx#first-heading
// "The router answers with a
// link and code here"
@docs sec/page.mdx
// "The **router** ... more words follow."
@docs sec/page.mdx
// "`+"`port` / 8080"+`"
@docs sec/page.mdx
// "func hello() {} // greeting from the snippet"
`)
	if !ok {
		t.Fatalf("TestQuotes rejected quotes the page says it accepts:\n%s", out)
	}
	out, ok = quotesRun(t, `@docs sec/page.mdx
// "more words follow ... The router answers"
`)
	if ok {
		t.Errorf("TestQuotes accepted fragments out of order:\n%s", out)
	}
}

// Docs: project/contributing.mdx#every-claim-has-a-test
// "`TestQuotes` in `docs/snippets/claims` reads every tag and checks that the page
// exists, that the anchor is the id of one of its headings, and that the quote
// still appears on the page."
// and "So editing a sentence that a test quotes fails `TestQuotes` until the
// test is updated to match, and deleting a page lists every claim that pointed at
// it."
func TestQuotesFailures(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		claims string
		want   []*regexp.Regexp
	}{
		"edited sentence": {
			"// Docs: sec/page.mdx\n// \"The router replies with a\"\n",
			[]*regexp.Regexp{regexp.MustCompile(`case_test\.go:3: sec/page\.mdx: not on sec/page\.mdx`)},
		},
		"unknown anchor": {
			"// Docs: sec/page.mdx#no-such-heading\n// \"The router answers\"\n",
			[]*regexp.Regexp{regexp.MustCompile(`no heading on sec/page\.mdx has id "no-such-heading"`)},
		},
		"deleted page": {
			"// Docs: sec/gone.mdx\n// \"one\"\n// Docs: sec/gone.mdx#x\n// \"two\"\n",
			[]*regexp.Regexp{
				regexp.MustCompile(`case_test\.go:3: sec/gone\.mdx: page \S+sec/gone\.mdx does not exist`),
				regexp.MustCompile(`case_test\.go:5: sec/gone\.mdx#x: page \S+sec/gone\.mdx does not exist`),
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			out, ok := quotesRun(t, c.claims)
			if ok {
				t.Fatalf("TestQuotes passed:\n%s", out)
			}
			for _, re := range c.want {
				if !re.MatchString(out) {
					t.Errorf("TestQuotes output lacks %s:\n%s", re, out)
				}
			}
		})
	}
}

var importsTestcontainers = regexp.MustCompile(`(?m)^\s*(?:[\w.]+\s+)?"github\.com/testcontainers/testcontainers-go[^"]*"\s*$`)

// Docs: project/contributing.mdx#every-claim-has-a-test
// "Claim tests that need a database or LocalStack start it with testcontainers and
// skip without Docker, unless `GAS_TEST_REQUIRE_DOCKER` is set, in which case they
// fail."
// Checked on the source of the claims' requireDocker helpers, for the reason
// TestRequireDockerFailsInsteadOfSkipping gives, and every claims package
// that imports testcontainers has one.
func TestClaimTestsSkipOrFailWithoutDocker(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	claims := filepath.Join("docs", "snippets", "claims")
	guarded := map[string]bool{}
	for _, g := range dockerGuards(t, root, claims) {
		guarded[filepath.Dir(g)] = true
		if !guardShape.MatchString(readFile(t, filepath.Join(root, g))) {
			t.Errorf("%s does not skip without GAS_TEST_REQUIRE_DOCKER and fail with it", g)
		}
	}
	files, err := filepath.Glob(filepath.Join(root, claims, "*", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		rel, _ := filepath.Rel(root, f)
		if importsTestcontainers.MatchString(readFile(t, f)) &&
			!strings.HasPrefix(filepath.Base(f), "requiredocker") && !guarded[filepath.Dir(rel)] {
			t.Errorf("%s uses testcontainers in a package with no requireDocker helper", rel)
		}
	}
}

// Docs: project/contributing.mdx#every-claim-has-a-test
// "The docs workflow, `.github/workflows/docs.yml`, runs `TestQuotes` and
// `pnpm build` on every pull request that touches `docs/`, and deploys the
// site to GitHub Pages from `main`."
func TestDocsWorkflow(t *testing.T) {
	t.Parallel()
	wf := readFile(t, filepath.Join(repoRoot(t), ".github", "workflows", "docs.yml"))
	on := block(t, wf, regexp.MustCompile(`^on:\s*$`))
	pr := block(t, on, regexp.MustCompile(`^  pull_request:\s*$`))
	if !strings.Contains(pr, "- 'docs/**'") {
		t.Errorf("docs.yml pull_request trigger does not cover docs/:\n%s", pr)
	}
	push := block(t, on, regexp.MustCompile(`^  push:\s*$`))
	if !regexp.MustCompile(`branches:\s*\n\s*- main\b`).MatchString(push) {
		t.Errorf("docs.yml push trigger is not main:\n%s", push)
	}
	build := job(t, wf, "build")
	for _, want := range []string{"run: go test ./claims -run TestQuotes", "working-directory: docs/snippets", "run: pnpm build"} {
		if !strings.Contains(build, want) {
			t.Errorf("docs.yml build job lacks %q", want)
		}
	}
	deploy := job(t, wf, "deploy")
	for _, want := range []string{"needs: build", "if: github.event_name != 'pull_request'", "actions/deploy-pages@", "pages: write"} {
		if !strings.Contains(deploy, want) {
			t.Errorf("docs.yml deploy job lacks %q", want)
		}
	}
	if !strings.Contains(build, "actions/upload-pages-artifact@") {
		t.Error("docs.yml build job does not upload the site for Pages")
	}
}
