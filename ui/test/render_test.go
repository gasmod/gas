package ui_test

import (
	"bytes"
	"context"
	"errors"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"testing"
	texttemplate "text/template"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/ui"
)

// spyWriter is an http.ResponseWriter that records every status written
// and fails its writes with err, when set.
type spyWriter struct {
	// header is what Header returns.
	header http.Header
	// err is what Write returns, or nil.
	err error
	// statuses are the statuses WriteHeader got, in order.
	statuses []int
	// body is what Write got.
	body bytes.Buffer
}

// newSpy returns a spyWriter with an empty header.
func newSpy() *spyWriter { return &spyWriter{header: http.Header{}} }

// Header returns the header.
func (w *spyWriter) Header() http.Header { return w.header }

// WriteHeader records status.
func (w *spyWriter) WriteHeader(status int) { w.statuses = append(w.statuses, status) }

// Write records b, or fails with w.err.
func (w *spyWriter) Write(b []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}

	return w.body.Write(b)
}

// untouched reports whether nothing reached w.
func (w *spyWriter) untouched() bool {
	return len(w.header) == 0 && len(w.statuses) == 0 && w.body.Len() == 0
}

// Render and RenderFragment call WriteHeader only for WithStatus, so a
// status the handler wrote first stands and no superfluous WriteHeader is
// logged; the default 200 comes from the first write.
func TestRender_NoWriteHeaderWithoutStatus(t *testing.T) {
	t.Parallel()

	s := initialized(t, files{"p.html": `P`}, nil)

	for _, frag := range []bool{false, true} {
		w := newSpy()
		w.WriteHeader(http.StatusUnprocessableEntity)

		call := s.Render
		if frag {
			call = s.RenderFragment
		}

		require.NoError(t, call(t.Context(), w, "p", nil))
		assert.Equal(t, []int{http.StatusUnprocessableEntity}, w.statuses)
		assert.Equal(t, "P", w.body.String())

		w = newSpy()
		require.NoError(t, call(t.Context(), w, "p", nil, ui.WithStatus(http.StatusCreated)))
		assert.Equal(t, []int{http.StatusCreated}, w.statuses)
	}
}

// Render sets Content-Type to HTML unless the handler set one.
func TestRender_ContentType(t *testing.T) {
	t.Parallel()

	s := initialized(t, files{"p.html": `P`}, nil)

	w := newSpy()
	require.NoError(t, s.Render(t.Context(), w, "p", nil))
	assert.Equal(t, "text/html; charset=utf-8", w.header.Get("Content-Type"))

	w = newSpy()
	w.header.Set("Content-Type", "application/xhtml+xml")
	require.NoError(t, s.Render(t.Context(), w, "p", nil))
	assert.Equal(t, "application/xhtml+xml", w.header.Get("Content-Type"))
}

// WithStatus takes 200 to 599 but 204 and 304; any other status is
// ErrInvalidStatus, with nothing executed or written, where it used to
// panic in WriteHeader after the page ran.
func TestRender_WithStatusBounds(t *testing.T) {
	t.Parallel()

	s := initialized(t, files{"p.html": `P`}, nil)

	for _, status := range []int{0, 99, 100, 101, 199, 204, 304, 600, 1000, -1} {
		if status == 0 {
			continue
		}

		for _, frag := range []bool{false, true} {
			w := newSpy()

			call := s.Render
			op := "render"

			if frag {
				call, op = s.RenderFragment, "render fragment"
			}

			err := call(t.Context(), w, "p", nil, ui.WithStatus(status))
			require.ErrorIs(t, err, ui.ErrInvalidStatus, status)
			require.EqualError(
				t,
				err,
				`gas/ui: `+op+` "p": invalid status: must be 200 to 599, and not 204 or 304`,
			)
			assert.True(t, w.untouched(), status)
		}
	}

	for _, status := range []int{200, 201, 299, 400, 404, 500, 503, 599} {
		w := newSpy()
		require.NoError(t, s.Render(t.Context(), w, "p", nil, ui.WithStatus(status)))
		assert.Equal(t, []int{status}, w.statuses)
	}
}

// A nil option, WithBlock on Render and WithLayout on RenderFragment are
// ErrInvalidOption; empty names keep the defaults; the last option of a
// kind wins.
func TestRender_Options(t *testing.T) {
	t.Parallel()

	s := initialized(t, files{
		"layouts/l.html": `{{define "base"}}B{{block "content" .}}{{end}}{{end}}{{define "alt"}}ALT{{end}}`,
		"p.html":         `{{define "content"}}C{{end}}{{define "x"}}X{{end}}`,
	}, nil)

	w := newSpy()
	err := s.Render(t.Context(), w, "p", nil, nil)
	require.ErrorIs(t, err, ui.ErrInvalidOption)
	require.EqualError(t, err, `gas/ui: render "p": invalid option: nil`)
	assert.True(t, w.untouched())

	err = s.Render(t.Context(), w, "p", nil, ui.WithBlock("x"))
	require.EqualError(
		t,
		err,
		`gas/ui: render "p": invalid option: WithBlock applies to RenderFragment only`,
	)

	err = s.RenderFragment(t.Context(), w, "p", nil, ui.WithLayout("alt"))
	require.EqualError(
		t,
		err,
		`gas/ui: render fragment "p": invalid option: WithLayout applies to Render only`,
	)
	assert.True(t, w.untouched())

	assert.Equal(t, "BC", body(t, s, "p", nil, ui.WithLayout("")))
	assert.Equal(t, "ALT", body(t, s, "p", nil, ui.WithLayout("alt")))
	assert.Equal(t, "BC", body(t, s, "p", nil, ui.WithLayout("alt"), ui.WithLayout("base")))
	assert.Equal(t, "C", fragmentBody(t, s, "p", nil, ui.WithBlock("")))
	assert.Equal(t, "X", fragmentBody(t, s, "p", nil, ui.WithBlock("x")))

	err = s.RenderFragment(t.Context(), w, "p", nil, ui.WithBlock("nope"))
	require.ErrorIs(t, err, ui.ErrBlockNotFound)

	_, err = render(t, s, "p", nil, ui.WithLayout("nope"))
	require.ErrorIs(t, err, ui.ErrLayoutNotFound)
}

// ApplyRenderOptions resolves options in order and skips nil ones.
func TestApplyRenderOptions(t *testing.T) {
	t.Parallel()

	assert.Equal(t, ui.RenderOptions{}, ui.ApplyRenderOptions())
	assert.Equal(t, ui.RenderOptions{Layout: "l", Block: "b", Status: 201}, ui.ApplyRenderOptions(
		ui.WithStatus(200), nil, ui.WithLayout("l"), ui.WithBlock("b"), ui.WithStatus(201),
	))
}

// With layout files, a page set without the LayoutName template is
// ErrLayoutNotFound rather than a page rendered without its layout.
func TestRender_LayoutsWithoutLayoutName(t *testing.T) {
	t.Parallel()

	s := initialized(t, files{
		"layouts/l.html": `{{define "main"}}M{{end}}`,
		"p.html":         `P`,
	}, nil)

	_, err := render(t, s, "p", nil)
	require.ErrorIs(t, err, ui.ErrLayoutNotFound)
	require.EqualError(t, err, `gas/ui: render "p": layout not found: "base"`)

	assert.Equal(t, "M", body(t, s, "p", nil, ui.WithLayout("main")))
}

// The LayoutName and FragmentBlock settings name the templates Render and
// RenderFragment execute.
func TestRender_LayoutAndBlockSettings(t *testing.T) {
	t.Parallel()

	s := initialized(t, files{
		"layouts/l.html": `{{define "shell"}}S{{block "main" .}}{{end}}{{end}}`,
		"p.html":         `top{{define "main"}}M{{end}}`,
	}, func(set *ui.Settings) {
		set.LayoutName = "shell"
		set.FragmentBlock = "main"
	})

	assert.Equal(t, "SM", body(t, s, "p", nil))
	assert.Equal(t, "M", fragmentBody(t, s, "p", nil))
}

// An execute error writes nothing and keeps html/template's error in the
// chain, so errors.As reaches it; the text names the file and the
// action.
func TestRender_ExecuteError(t *testing.T) {
	t.Parallel()

	fail := errSecretFail
	s := initialized(t, files{"p.html": `x{{fail}}`}, nil,
		ui.WithFuncs(template.FuncMap{"fail": func() (string, error) { return "", fail }}))

	w := newSpy()
	err := s.Render(t.Context(), w, "p", nil)
	require.ErrorIs(t, err, ui.ErrExecute)
	require.ErrorIs(t, err, fail)

	var execErr texttemplate.ExecError

	require.ErrorAs(t, err, &execErr)
	require.EqualError(
		t,
		err,
		`gas/ui: render "p": execute failed: template: p.html:1:3: executing "p.html" `+
			`at <fail>: error calling fail: db password=hunter2 failed`,
	)
	assert.True(t, w.untouched())
}

// An escaping error of html/template, found when a page first executes,
// is an execute error that errors.As reaches as *template.Error.
func TestRender_EscapeError(t *testing.T) {
	t.Parallel()

	s := initialized(t, files{"p.html": `<a href="{{.}}`}, nil)

	_, err := render(t, s, "p", "x")
	require.ErrorIs(t, err, ui.ErrExecute)

	var escErr *template.Error

	require.ErrorAs(t, err, &escErr)
}

// A failed write is ErrWrite, wrapping the writer's error.
func TestRender_WriteError(t *testing.T) {
	t.Parallel()

	s := initialized(t, files{"p.html": `P`}, nil)
	gone := errClientGone

	w := newSpy()

	w.err = gone

	err := s.Render(t.Context(), w, "p", nil)
	require.ErrorIs(t, err, ui.ErrWrite)
	require.ErrorIs(t, err, gone)
	require.EqualError(t, err, `gas/ui: render "p": write failed: client gone`)
}

// A render whose context has ended returns its error and writes nothing.
func TestRender_ContextEnded(t *testing.T) {
	t.Parallel()

	s := initialized(t, files{"p.html": `P`}, nil)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	w := newSpy()
	err := s.Render(ctx, w, "p", nil)
	require.ErrorIs(t, err, context.Canceled)
	require.EqualError(t, err, `gas/ui: render "p": context canceled`)
	assert.True(t, w.untouched())
}

// Data is escaped in every context html/template knows, and the default
// functions return no trusted type, so none of them breaks out.
func TestRender_Escaping(t *testing.T) {
	t.Parallel()

	s := initialized(t, files{
		"p.html": `<p>{{.}}</p><a href="{{.}}" title="{{.}}">x</a><script>var v = {{.}}; var j = {{json .}};</script>` +
			`<style>p{color:{{.}}}</style>{{upper .}}`,
	}, nil)

	out := body(t, s, "p", `</script><script>alert(1)</script>`)
	assert.NotContains(t, out, `<script>alert(1)</script>`)
	assert.Equal(t, 1, strings.Count(out, "<script>"))

	out = body(t, s, "p", `javascript:alert(1)`)
	assert.Contains(t, out, `href="#ZgotmplZ"`)
}

// A page larger than the pool's cap renders whole, and renders after it
// are not affected by the buffer it used.
func TestRender_LargePage(t *testing.T) {
	t.Parallel()

	s := initialized(t, files{"big.html": `{{range .}}0123456789{{end}}`, "small.html": `s`}, nil)

	out := body(t, s, "big", make([]struct{}, 200000))
	assert.Len(t, out, 2000000)
	assert.Equal(t, "s", body(t, s, "small", nil))
}

// Only templates with one of the extensions are read: a `.txt` template
// of a store shared with email is not a page and is never compiled.
func TestRender_Extensions(t *testing.T) {
	t.Parallel()

	st := newStoreExt(t, files{"a.html": `A`, "mail/welcome.txt": `{{.Broken`, "b.tmpl": `B`},
		".html", ".txt", ".tmpl")

	s, _ := initializedLog(t, st, nil)
	assert.Equal(t, "A", body(t, s, "a", nil))
	require.NoError(t, s.Reload(t.Context()))

	_, err := render(t, s, "mail/welcome", nil)
	require.ErrorIs(t, err, ui.ErrPageNotFound)

	s, _ = initializedLog(
		t,
		st,
		func(set *ui.Settings) { set.Extensions = []string{".tmpl", ".html"} },
	)
	assert.Equal(t, "B", body(t, s, "b", nil))
	assert.Equal(t, "A", body(t, s, "a", nil))
}

// Two store names that make the same page name fail to compile together;
// the longest extension a name ends in is removed.
func TestRender_PageNameConflict(t *testing.T) {
	t.Parallel()

	st := newStoreExt(
		t,
		files{"a.htm": `1`, "a.html": `2`, "b.x.html": `3`},
		".html",
		".htm",
		".x.html",
	)

	s, _ := initializedLog(
		t,
		st,
		func(set *ui.Settings) { set.Extensions = []string{".html", ".htm", ".x.html"} },
	)

	_, err := render(t, s, "a", nil)
	require.ErrorIs(t, err, ui.ErrCompile)
	require.EqualError(
		t,
		err,
		`gas/ui: render "a": compile failed: "a.htm" and "a.html" are both page "a"`,
	)
	assert.Equal(t, "3", body(t, s, "b", nil))
}

// A store name that leaves no page name fails to compile and is reported
// by Reload.
func TestRender_NamelessPage(t *testing.T) {
	t.Parallel()

	s := initialized(t, files{".html": `x`, "a.html": `A`}, nil)

	err := s.Reload(t.Context())
	require.ErrorIs(t, err, ui.ErrCompile)
	require.EqualError(t, err, `gas/ui: reload: compile failed: ".html" leaves no page name`)
	assert.Equal(t, "A", body(t, s, "a", nil))
}

// With PagesPrefix set, only templates under it are pages, named without
// it; layouts and partials stay where they are.
func TestRender_PagesPrefix(t *testing.T) {
	t.Parallel()

	s := initialized(t, files{
		"layouts/l.html":   `{{define "base"}}[{{block "content" .}}{{end}}]{{end}}`,
		"pages/home.html":  `{{define "content"}}H{{end}}`,
		"mail/notice.html": `{{.Broken`,
	}, func(set *ui.Settings) { set.PagesPrefix = "pages/" })

	assert.Equal(t, "[H]", body(t, s, "home", nil))
	require.NoError(t, s.Reload(t.Context()))

	for _, name := range []string{"pages/home", "mail/notice"} {
		_, err := render(t, s, name, nil)
		require.ErrorIs(t, err, ui.ErrPageNotFound)
	}
}

// Every name a page cannot have is ErrInvalidName, with the reason and
// never the name.
func TestRender_InvalidNames(t *testing.T) {
	t.Parallel()

	s := initialized(t, files{"a.html": `A`}, nil)

	cases := map[string]string{
		"":        "empty",
		"a\xffb":  "not valid UTF-8",
		"a\x00b":  "holds a NUL byte",
		`a\b`:     "holds a backslash",
		"/a":      "not a clean slash path",
		"a/":      "not a clean slash path",
		"a/../a":  "not a clean slash path",
		"./a":     "not a clean slash path",
		"a//b":    "not a clean slash path",
		"..":      "not a clean slash path",
		"x/y/../": "not a clean slash path",
	}

	for name, reason := range cases {
		w := newSpy()
		err := s.Render(t.Context(), w, name, nil)
		require.ErrorIs(t, err, ui.ErrInvalidName, name)
		require.EqualError(t, err, "gas/ui: render: invalid name: "+reason, name)
		assert.True(t, w.untouched())
	}
}

// FuzzPageName checks, for any name, that a render never panics, returns
// nil only for the store's page, ErrInvalidName or ErrPageNotFound
// otherwise with nothing written, and quotes a name in an error only when
// it is valid, so a name from a request is never echoed raw.
func FuzzPageName(f *testing.F) {
	for _, seed := range []string{
		"home", "", "home.html", "../home", "/home", "a\x00b", "a\xff", "layouts/base", "a//b", "%2e%2e",
		"partials/nav", "home\n", "\"home\"", "a\\b",
	} {
		f.Add(seed)
	}

	s, _ := initializedLog(f, newStore(f, files{
		"home.html":         `H`,
		"layouts/base.html": `{{define "base"}}{{template "content" .}}{{end}}`,
	}), nil)

	f.Fuzz(func(t *testing.T, name string) {
		w := newSpy()
		err := s.RenderFragment(t.Context(), w, name, nil)

		switch {
		case name == "home":
			require.NoError(t, err)
			assert.Equal(t, "H", w.body.String())
		case errors.Is(err, ui.ErrInvalidName):
			assert.True(t, w.untouched())
			assert.NotContains(t, err.Error(), `"`)
			assert.True(t, utf8.ValidString(err.Error()))
		default:
			require.ErrorIs(t, err, ui.ErrPageNotFound)
			assert.True(t, w.untouched())
			assert.Equal(
				t,
				"gas/ui: render fragment "+strconv.Quote(name)+": page not found",
				err.Error(),
			)
		}
	})
}
