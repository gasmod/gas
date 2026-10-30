package testutil

import (
	"bufio"
	"os"
	"strings"
	"testing"
)

// codePrefix starts a line of a code block in a Go doc comment.
const codePrefix = "//\t"

// DocLines returns the lines of the code blocks in the doc comments of the
// Go file at path that start with prefix, in order, without the comment
// marker and the tab, as described in the package documentation.
func DocLines(tb testing.TB, path, prefix string) []string {
	tb.Helper()

	var out []string

	for _, line := range readLines(tb, path) {
		code, ok := strings.CutPrefix(line, codePrefix)
		if ok && strings.HasPrefix(code, prefix) {
			out = append(out, code)
		}
	}

	return out
}

// DocBlock returns the lines of the first code block in the doc comments
// of the Go file at path that follows a comment line containing marker,
// without the comment marker and the tab, as described in the package
// documentation. It fails the test when there is no such block.
func DocBlock(tb testing.TB, path, marker string) []string {
	tb.Helper()

	var (
		out    []string
		marked bool
	)

	for _, line := range readLines(tb, path) {
		if !marked {
			marked = !strings.HasPrefix(line, codePrefix) && strings.Contains(line, marker)

			continue
		}

		code, ok := strings.CutPrefix(line, codePrefix)

		switch {
		case ok:
			out = append(out, code)
		case len(out) > 0:
			return out
		}
	}

	if len(out) == 0 {
		tb.Fatalf("no code block after %q in %s", marker, path)
	}

	return out
}

// readLines returns the lines of the file at path.
func readLines(tb testing.TB, path string) []string {
	tb.Helper()

	f, err := os.Open(path) //nolint:gosec // G304: the tests name their own doc.go.
	if err != nil {
		tb.Fatalf("failed to open %s: %v", path, err)
	}

	defer func() { _ = f.Close() }()

	var lines []string

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}

	err = sc.Err()
	if err != nil {
		tb.Fatalf("failed to read %s: %v", path, err)
	}

	return lines
}
