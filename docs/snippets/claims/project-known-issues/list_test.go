package projectknownissues_test

import (
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// entry is one KNOWN_ISSUES.md entry.
type entry struct {
	id, title, status, area string
	detail, next            bool
}

var (
	fileHeading = regexp.MustCompile(`^## (KI-\d{3}): (.+)$`)
	fileField   = regexp.MustCompile(`^- \*\*(Status|Area|Detail|Next step)\*\*: ?(.*)$`)
	pageSection = regexp.MustCompile(`^## (.+)$`)
	pageIssue   = regexp.MustCompile(`^### (KI-\d{3}): (.+)$`)
	anyKI       = regexp.MustCompile(`^#+ (KI-[^:]*):`)
	backticked  = regexp.MustCompile("`([^`]+)`")
)

// knownIssues parses KNOWN_ISSUES.md at the repository root.
func knownIssues(t *testing.T) []entry {
	t.Helper()
	var out []entry
	for line := range strings.SplitSeq(readFile(t, filepath.Join(repoRoot(t), "KNOWN_ISSUES.md")), "\n") {
		if m := fileHeading.FindStringSubmatch(line); m != nil {
			out = append(out, entry{id: m[1], title: m[2]})
			continue
		}
		if m := anyKI.FindStringSubmatch(line); m != nil {
			t.Errorf("KNOWN_ISSUES.md heading %q has an ID not of the form KI-NNN", line)
			continue
		}
		if len(out) == 0 {
			continue
		}
		e := &out[len(out)-1]
		if m := fileField.FindStringSubmatch(line); m != nil {
			switch m[1] {
			case "Status":
				e.status = m[2]
			case "Area":
				e.area = m[2]
			case "Detail":
				e.detail = true
			case "Next step":
				e.next = true
			}
		}
	}
	if len(out) == 0 {
		t.Fatal("no entries parsed from KNOWN_ISSUES.md")
	}
	return out
}

// pageIssues returns, for each KI ID on the page, the module sections it is
// listed under.
func pageIssues(t *testing.T) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	section := ""
	page := readFile(t, filepath.Join(repoRoot(t), "docs", "src", "content", "docs", "project", "known-issues.mdx"))
	for line := range strings.SplitSeq(page, "\n") {
		if m := pageSection.FindStringSubmatch(line); m != nil {
			section = m[1]
			continue
		}
		if m := pageIssue.FindStringSubmatch(line); m != nil {
			out[m[1]] = append(out[m[1]], section)
		}
	}
	if len(out) == 0 {
		t.Fatal("no issues parsed from the page")
	}
	return out
}

// moduleSections maps the first path element of an Area to the page section
// that module is grouped under.
var moduleSections = map[string]string{
	"log": "Logging", "database": "Database", "auth": "Auth", "email": "Email",
	"queue": "Queue", "template": "Templates", "ui": "UI", "example": "Examples",
	"storage": "Storage", "cache": "Cache", "config": "Config", "migrate": "Migrations",
}

// sectionsFor returns the page sections an Area belongs under. An Area that
// names only tests goes under "Tests only"; "core" goes under "Core".
// Assumption: an entry whose Area names several modules is listed under each.
func sectionsFor(t *testing.T, e entry) []string {
	t.Helper()
	area := strings.TrimSuffix(strings.TrimSpace(e.area), ".")
	if strings.HasSuffix(area, " tests") {
		return []string{"Tests only"}
	}
	set := map[string]bool{}
	if strings.HasPrefix(area, "core") {
		set["Core"] = true
	}
	for _, m := range backticked.FindAllStringSubmatch(area, -1) {
		mod := strings.Split(m[1], "/")[0]
		if strings.HasSuffix(mod, ".go") {
			continue // a file in core, such as handler.go
		}
		s, ok := moduleSections[mod]
		if !ok {
			t.Errorf("%s: Area %q names module %q with no known page section", e.id, e.area, mod)
			continue
		}
		set[s] = true
	}
	if len(set) == 0 {
		t.Errorf("%s: Area %q names no module", e.id, e.area)
	}
	return slices.Sorted(maps.Keys(set))
}

// Docs: project/known-issues.mdx#_top
// "This page lists the defects that are open in the release these docs
// describe, grouped by module" and "If this page and that file disagree, the
// file is right."
// Checked as: every KNOWN_ISSUES.md entry is on the page, with its ID, under
// the section of the module its Area names, and the page lists no ID the file
// does not hold.
func TestPageMatchesKnownIssues(t *testing.T) {
	t.Parallel()
	page := pageIssues(t)
	file := map[string]bool{}
	for _, e := range knownIssues(t) {
		file[e.id] = true
		got, ok := page[e.id]
		if !ok {
			t.Errorf("%s (%s) is in KNOWN_ISSUES.md but not on the page", e.id, e.title)
			continue
		}
		slices.Sort(got)
		if want := sectionsFor(t, e); !slices.Equal(got, want) {
			t.Errorf("%s is listed under %v, want %v from its Area %q", e.id, got, want, e.area)
		}
	}
	for id := range page {
		if !file[id] {
			t.Errorf("%s is on the page but not in KNOWN_ISSUES.md", id)
		}
	}
}

// Docs: project/known-issues.mdx#_top
// "Every issue is open: a design decision or a fix is pending."
// The file's statuses are open, investigating, decision needed, and fix in
// progress; each of them leaves a decision or a fix pending.
func TestEveryIssueOpen(t *testing.T) {
	t.Parallel()
	for _, e := range knownIssues(t) {
		s := strings.TrimSuffix(strings.Trim(e.status, "` "), ".")
		switch {
		case s == "open", s == "investigating", s == "decision needed", strings.HasPrefix(s, "fix in progress"):
		default:
			t.Errorf("%s status %q, want one of the open statuses", e.id, e.status)
		}
	}
}

// Docs: project/known-issues.mdx#_top
// "Each entry there has a stable ID (`KI-NNN`, never reused), the evidence, and
// the next step."
// "Never reused" is checked within the file: no ID appears twice. History is
// not searched.
func TestEntriesHaveIDEvidenceNextStep(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for _, e := range knownIssues(t) {
		if seen[e.id] {
			t.Errorf("%s appears twice in KNOWN_ISSUES.md", e.id)
		}
		seen[e.id] = true
		if !e.detail || !e.next {
			t.Errorf("%s: Detail present %v, Next step present %v, want both", e.id, e.detail, e.next)
		}
	}
}
