package template_test

import (
	"regexp"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/template"
	"github.com/gasmod/gas/template/memory"
)

// html is the default extension set.
var html = []string{template.DefaultExtension}

// CheckName accepts clean slash paths with an allowed extension and
// refuses everything else with ErrInvalidName and a reason that never
// quotes the name.
func TestCheckName(t *testing.T) {
	t.Parallel()

	for _, name := range []string{
		"a.html", "pages/home.html", ".html", "a b.html", "café.html", "a.b.html", "x/.hidden.html",
		"-.html",
	} {
		require.NoError(t, template.CheckName(name, html), name)
	}

	cases := []struct {
		name   string
		reason string
	}{
		{"", "empty"},
		{"bad\xff.html", "not valid UTF-8"},
		{"nul\x00.html", "holds a NUL byte"},
		{`a\b.html`, "holds a backslash"},
		{"/a.html", "not a clean slash path"},
		{"a.html/", "not a clean slash path"},
		{"./a.html", "not a clean slash path"},
		{"a/../b.html", "not a clean slash path"},
		{"a//b.html", "not a clean slash path"},
		{".", "extension not allowed"},
		{"..", "not a clean slash path"},
		{"a.HTML", "extension not allowed"},
		{"a.htm", "extension not allowed"},
		{"a.html.bak", "extension not allowed"},
		{"a", "extension not allowed"},
	}

	for _, c := range cases {
		err := template.CheckName(c.name, html)
		require.ErrorIs(t, err, template.ErrInvalidName, c.name)
		require.EqualError(t, err, "template: invalid name: "+c.reason, c.name)
	}
}

// HasExtension compares bytes, and an extension may hold dots.
func TestHasExtension(t *testing.T) {
	t.Parallel()

	assert.True(t, template.HasExtension("a.tmpl.html", []string{".tmpl.html"}))
	assert.False(t, template.HasExtension("a.html", []string{".tmpl.html"}))
	assert.False(t, template.HasExtension("a.HTML", html))
	assert.False(t, template.HasExtension("a.html", nil))
}

// CheckExtensions refuses an empty set and an extension that is not a dot
// and one or more characters free of slashes, backslashes and NUL, and
// names its position, not its text.
func TestCheckExtensions(t *testing.T) {
	t.Parallel()

	require.NoError(t, template.CheckExtensions(html))
	require.NoError(t, template.CheckExtensions([]string{".html", ".txt", ".tmpl.html"}))

	err := template.CheckExtensions(nil)
	require.ErrorIs(t, err, template.ErrInvalidExtension)
	require.EqualError(t, err, "template: invalid extension: none given")

	for _, ext := range []string{"", ".", "html", "./x", `.a\b`, ".a\x00", ".\xff"} {
		err := template.CheckExtensions([]string{".html", ext})
		require.ErrorIs(t, err, template.ErrInvalidExtension, ext)
		require.EqualError(t, err,
			"template: invalid extension: extension 2 must be a dot and one or more characters, "+
				"none a slash, backslash or NUL", ext)
	}
}

// nameError is the shape of every error CheckName returns: a fixed reason,
// never the name.
var nameError = regexp.MustCompile(
	`^template: invalid name: (empty|not valid UTF-8|holds a NUL byte|holds a backslash|` +
		`not a clean slash path|extension not allowed)$`)

// FuzzCheckName checks that CheckName never panics, gives the same answer
// twice, refuses with an error of the documented shape, and that every
// name it accepts round-trips through a memory store: Register, Get of
// the same content and List holding it.
func FuzzCheckName(f *testing.F) {
	for _, seed := range []string{
		"a.html", "pages/home.html", "", "/etc/passwd.html", "../a.html", "a\\b.html", "\xff.html",
		"a\x00.html", "a/./b.html", ".html", "a.HTML", "a//b.html", "{{.Secret}}.html",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, name string) {
		err := template.CheckName(name, html)
		again := template.CheckName(name, html)
		assert.Equal(t, err == nil, again == nil)

		if err != nil {
			require.ErrorIs(t, err, template.ErrInvalidName)
			assert.Regexp(t, nameError, err.Error())

			return
		}

		s, err := memory.NewStore()()
		require.NoError(t, err)
		require.NoError(t, s.Register(t.Context(), name, []byte(name)))

		b, err := s.Get(t.Context(), name)
		require.NoError(t, err)
		assert.Equal(t, name, string(b))

		names, err := s.List(t.Context())
		require.NoError(t, err)
		assert.True(t, slices.Equal([]string{name}, names))
	})
}
