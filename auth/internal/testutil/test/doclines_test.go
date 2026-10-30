package testutil_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/gasmod/gas/auth/internal/testutil"
)

// docSource is a doc comment with two code blocks.
const docSource = `// Package p does things.
//
//	p.Do()
//
// Its errors:
//
//	p: one
//	p: two
//
// And more:
//
//	p: three
package p
`

// writeDoc writes docSource to a new file and returns its path.
func writeDoc(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "doc.go")

	err := os.WriteFile(path, []byte(docSource), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	return path
}

// DocLines returns the code lines that start with the prefix, from every
// block, without the comment marker and the tab.
func TestDocLines(t *testing.T) {
	t.Parallel()

	path := writeDoc(t)

	assert.Equal(t, []string{"p: one", "p: two", "p: three"}, testutil.DocLines(t, path, "p: "))
	assert.Equal(t, []string{"p.Do()"}, testutil.DocLines(t, path, "p."))
	assert.Empty(t, testutil.DocLines(t, path, "q: "))
}

// DocBlock returns the first code block after the marker, and only that
// block.
func TestDocBlock(t *testing.T) {
	t.Parallel()

	path := writeDoc(t)

	assert.Equal(t, []string{"p: one", "p: two"}, testutil.DocBlock(t, path, "Its errors:"))
	assert.Equal(t, []string{"p: three"}, testutil.DocBlock(t, path, "And more:"))
	assert.Equal(t, []string{"p.Do()"}, testutil.DocBlock(t, path, "Package p"))
}
