package claims_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

// pagesDir holds the docs pages, relative to this package's directory.
const pagesDir = "../../src/content/docs"

const docsTag = "// Docs:"

// claim is one Docs tag in a claims test and the quote that follows it.
type claim struct {
	pos    string // file:line of the tag
	ref    string // the tag as written, page plus optional #anchor
	page   string
	anchor string
	quote  string
	err    string // set when the tag or its quote cannot be parsed
}

// page is a docs page reduced to what the claims are checked against.
type page struct {
	text     string          // normalized body
	anchors  map[string]bool // heading ids, as Starlight renders them
	problems []string        // snippets or headings the page cannot be rendered from
}

// TestQuotes checks every Docs tag under claims/ against its page: the page
// exists, every fragment of the quote appears on it in order, and the anchor,
// when given, is the id of one of its headings. Each failure names the tag's
// file and line, so after a page is removed the output lists its orphans.
func TestQuotes(t *testing.T) {
	t.Parallel()
	claims := collectClaims(t)
	if len(claims) == 0 {
		t.Fatal("no Docs tags found under claims/")
	}
	pages := map[string]*page{}
	for _, c := range claims {
		if c.err != "" {
			t.Errorf("%s: %s: %s", c.pos, c.ref, c.err)
			continue
		}
		p, seen := pages[c.page]
		if !seen {
			p = loadPage(t, c.page)
			pages[c.page] = p
			if p != nil {
				for _, msg := range p.problems {
					t.Errorf("%s: %s", c.page, msg)
				}
			}
		}
		if p == nil {
			t.Errorf("%s: %s: page %s does not exist", c.pos, c.ref, filepath.Join(pagesDir, c.page))
			continue
		}
		if c.anchor != "" && !p.anchors[c.anchor] {
			t.Errorf("%s: %s: no heading on %s has id %q", c.pos, c.ref, c.page, c.anchor)
		}
		if frag, ok := matchQuote(p.text, c.quote); !ok {
			t.Errorf("%s: %s: not on %s: %q", c.pos, c.ref, c.page, frag)
		}
	}
	t.Logf("checked %d claims against %d pages", len(claims), len(pages))
}

// collectClaims reads every Docs tag in the Go files under the claims tree. A
// tag is a line comment "Docs: <section>/<slug>.mdx[#anchor]", and the comment
// lines right after it hold the quote: it opens with a double quote and ends
// at the first line that ends with one. Comment lines after that are notes.
func collectClaims(t *testing.T) []claim {
	t.Helper()
	var claims []claim
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		lines := strings.Split(string(src), "\n")
		for i, line := range lines {
			if rest, ok := strings.CutPrefix(strings.TrimSpace(line), docsTag); ok {
				c := parseRef(strings.TrimSpace(rest))
				c.pos = path + ":" + strconv.Itoa(i+1)
				if c.err == "" {
					c.quote, c.err = readQuote(lines[i+1:])
				}
				claims = append(claims, c)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return claims
}

func parseRef(ref string) claim {
	c := claim{ref: ref}
	c.page, c.anchor, _ = strings.Cut(ref, "#")
	switch {
	case strings.ContainsAny(ref, " \t"):
		c.err = "tag must be a single page reference"
	case !strings.HasSuffix(c.page, ".mdx"):
		c.err = "tag must name an .mdx page"
	case !filepath.IsLocal(c.page):
		c.err = "page must be relative to " + pagesDir
	case strings.HasSuffix(ref, "#"):
		c.err = "empty anchor"
	}
	return c
}

// readQuote returns the quote opening on the first of lines, without its
// outer double quotes, or a reason there is none.
func readQuote(lines []string) (string, string) {
	var parts []string
	for _, line := range lines {
		text, ok := commentText(line)
		if !ok || strings.HasPrefix(text, "Docs:") {
			break
		}
		if len(parts) == 0 && !strings.HasPrefix(text, `"`) {
			break
		}
		parts = append(parts, text)
		if (len(text) > 1 || len(parts) > 1) && strings.HasSuffix(text, `"`) {
			q := strings.Join(parts, " ")
			return q[1 : len(q)-1], ""
		}
	}
	if len(parts) == 0 {
		return "", `no quoted claim follows the tag; quote the page text on the next comment line`
	}
	return "", "quote is not closed with a double quote"
}

func commentText(line string) (string, bool) {
	s, ok := strings.CutPrefix(strings.TrimSpace(line), "//")
	return strings.TrimSpace(s), ok
}

// matchQuote reports whether quote appears in text. A tag may carry several
// quotes, written "a" and "b" or "a", "b", and each must appear somewhere.
// Within one, "..." marks a gap, and the fragments around it must appear in
// order. Otherwise it returns the first fragment it could not find.
func matchQuote(text, quote string) (string, bool) {
	for _, q := range quoteSep.Split(quote, -1) {
		at := 0
		for frag := range strings.SplitSeq(elision.Replace(q), "...") {
			re := fragmentPattern(frag)
			if re == nil {
				continue
			}
			loc := re.FindStringIndex(text[at:])
			if loc == nil {
				return strings.TrimSpace(frag), false
			}
			at += loc[1]
		}
	}
	return "", true
}

// fragmentPattern matches one normalized fragment. A " / " in a quote joins
// lines or table cells that are apart on the page, so it matches a space as
// well as itself.
func fragmentPattern(frag string) *regexp.Regexp {
	var parts []string
	for part := range strings.SplitSeq(frag, " / ") {
		if n := normalize(part); n != "" {
			parts = append(parts, regexp.QuoteMeta(n))
		}
	}
	if len(parts) == 0 {
		return nil
	}
	return regexp.MustCompile(strings.Join(parts, "(?: / | )"))
}

var (
	quoteSep = regexp.MustCompile(`" and "|", "`)
	elision  = strings.NewReplacer("[...]", " ... ", "\u2026", " ... ")
	mdLink   = regexp.MustCompile(`\]\([^)\s]*\)`)
	// markup a quote may keep or drop: code spans, bold, table pipes, and the
	// brackets of a link whose target it leaves out.
	markup = strings.NewReplacer("`", "", "**", "", "|", " ", "[", "", "]", "")
)

// normalize reduces page text and quotes to the same form: links lose their
// targets, markup, list markers, and the comment markers that continue a code
// comment onto the next line are dropped, and runs of whitespace become one
// space.
func normalize(s string) string {
	words := strings.Fields(markup.Replace(mdLink.ReplaceAllString(s, "")))
	words = slices.DeleteFunc(words, func(w string) bool { return w == "-" || w == "*" || w == "+" || w == "//" })
	return strings.Join(words, " ")
}

var (
	rawImport = regexp.MustCompile(`^import (\w+) from '([^']+)\?raw';$`)
	codeTag   = regexp.MustCompile(`<Code code=\{(?:region\((\w+), '([^']+)'\)|(\w+))\}[^>]*/>`)
)

// renderText is the page source with each <Code> block that shows an imported
// file, or a region of one, replaced by that code. Imports and tags inside
// fenced code blocks are examples and left as they are.
func renderText(name, src string, problems *[]string) string {
	files := map[string]string{}
	lines := strings.Split(src, "\n")
	inFence := fenceTracker()
	for i, line := range lines {
		if inFence(line) {
			continue
		}
		if m := rawImport.FindStringSubmatch(line); m != nil {
			raw, err := os.ReadFile(filepath.Join(pagesDir, filepath.Dir(name), m[2]))
			if err != nil {
				*problems = append(*problems, "import "+m[1]+": "+err.Error())
				continue
			}
			files[m[1]] = string(raw)
		}
		lines[i] = codeTag.ReplaceAllStringFunc(line, func(tag string) string {
			m := codeTag.FindStringSubmatch(tag)
			if m[3] != "" {
				return files[m[3]]
			}
			body, err := snippetRegion(files[m[1]], m[2])
			if err != "" {
				*problems = append(*problems, tag+": "+err)
			}
			return body
		})
	}
	return strings.Join(lines, "\n")
}

// fenceTracker returns a function to call on each line in turn. It reports
// whether the line opens, closes, or sits inside a fenced code block.
func fenceTracker() func(string) bool {
	fence := ""
	return func(line string) bool {
		trimmed := strings.TrimSpace(line)
		switch {
		case fence != "":
			if strings.HasPrefix(trimmed, fence) {
				fence = ""
			}
			return true
		case strings.HasPrefix(trimmed, "```"), strings.HasPrefix(trimmed, "~~~"):
			fence = trimmed[:3]
			return true
		}
		return false
	}
}

// snippetRegion ports region() from docs/src/lib/snippet.ts, without the
// dedent and blank-edge trimming that normalization makes moot.
func snippetRegion(source, name string) (string, string) {
	lines := strings.Split(source, "\n")
	start := slices.IndexFunc(lines, func(l string) bool { return strings.TrimSpace(l) == "// #region "+name })
	if start < 0 {
		return "", "region not found"
	}
	var body []string
	for _, l := range lines[start+1:] {
		l = strings.TrimSpace(l)
		// Only the region's own end marker closes it, so a nested or
		// overlapping region ending first does not cut it short.
		if l == "// #endregion "+name {
			return strings.Join(body, "\n"), ""
		}
		if !strings.HasPrefix(l, "// #region") && !strings.HasPrefix(l, "// #endregion") {
			body = append(body, l)
		}
	}
	return "", "region is not closed"
}

var heading = regexp.MustCompile(`^(#{1,6})[ \t]+(.*?)(?:[ \t]+#+)?[ \t]*$`)

// loadPage reads and indexes a page, or returns nil when it does not exist.
func loadPage(t *testing.T, name string) *page {
	t.Helper()
	src, err := os.ReadFile(filepath.Join(pagesDir, name))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	// Starlight renders the page title as an h1 with this id.
	p := &page{anchors: map[string]bool{"_top": true}}
	p.text = normalize(renderText(name, string(src), &p.problems))
	s := slugger{}
	inFence := fenceTracker()
	for line := range strings.SplitSeq(string(src), "\n") {
		if inFence(line) {
			continue
		}
		m := heading.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		text := headingText(m[2])
		if strings.ContainsAny(text, "*_<>{}&\\") {
			p.problems = append(p.problems, "heading "+strconv.Quote(m[2])+
				" has markup the slug port does not render; extend headingText")
		}
		p.anchors[s.slug(text)] = true
	}
	return p
}

// headingText is the text content Astro slugs for a heading: link text, code
// span contents, and bold contents.
func headingText(s string) string {
	s = mdLink.ReplaceAllString(s, "")
	return strings.NewReplacer("`", "", "**", "", "[", "", "]", "").Replace(s)
}

// slugger ports the BananaSlug class of github-slugger 2.0.0, which Astro uses
// for heading ids. Repeats on one page get -1, -2, and so on.
type slugger map[string]int

func (s slugger) slug(value string) string {
	base := githubSlug(value)
	result := base
	for {
		if _, taken := s[result]; !taken {
			break
		}
		s[base]++
		result = base + "-" + strconv.Itoa(s[base])
	}
	s[result] = 0
	return result
}

// githubSlug ports github-slugger's slug function: lowercase, delete the
// characters in its regex, and turn each space into a hyphen. With U+0130
// expanded as JavaScript does, strings.ToLower agrees with toLowerCase on
// every single code point; the context-sensitive final sigma is not ported.
func githubSlug(value string) string {
	value = strings.ReplaceAll(value, "\u0130", "i\u0307")
	var b strings.Builder
	for _, r := range strings.ToLower(value) {
		switch {
		case unicode.Is(sluggerStrip, r):
		case r == ' ':
			b.WriteByte('-')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// TestSlugger pins the port to slugs github-slugger 2.0.0 produced in node.
func TestSlugger(t *testing.T) {
	t.Parallel()
	s := slugger{}
	for _, c := range []struct{ in, want string }{
		{"Why transient services cannot implement Service", "why-transient-services-cannot-implement-service"},
		{"Cache-Control", "cache-control"},
		{"a", "a"},
		{"a", "a-1"},
		{"a-1", "a-1-1"},
		{"a", "a-2"},
		{"  Two  spaces ", "--two--spaces-"},
		{"\u0130stanbul", "i\u0307stanbul"},
		{"\u00dcn\u00efc\u00f6d\u00e9 H\u00e9ading", "\u00fcn\u00efc\u00f6d\u00e9-h\u00e9ading"},
		{"emoji \U0001f389 here", "emoji--here"},
		{"C++ & Go: why?", "c--go-why"},
		{"tab\there", "tabhere"},
	} {
		if got := s.slug(c.in); got != c.want {
			t.Errorf("slug(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestSnippetRegion pins the port to region() in docs/src/lib/snippet.ts.
func TestSnippetRegion(t *testing.T) {
	t.Parallel()
	src := strings.Join([]string{
		"// #region outer",
		"a",
		"// #region inner",
		"b",
		"// #endregion inner",
		"c",
		"// #region overlap",
		"d",
		"// #endregion outer",
		"e",
		"// #endregion overlap",
		"// #region open",
		"f",
		"// #endregion other",
	}, "\n")
	cases := []struct{ name, body, err string }{
		{"outer", "a\nb\nc\nd", ""},
		{"inner", "b", ""},
		{"overlap", "d\ne", ""},
		{"open", "", "region is not closed"},
		{"missing", "", "region not found"},
	}
	for _, c := range cases {
		body, err := snippetRegion(src, c.name)
		if body != c.body || err != c.err {
			t.Errorf("snippetRegion(%q) = %q, %q; want %q, %q", c.name, body, err, c.body, c.err)
		}
	}
}
