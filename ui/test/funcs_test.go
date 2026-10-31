package ui_test

import (
	"html/template"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/ui"
)

// The default functions behave as the package documentation lists them.
func TestFuncs_Defaults(t *testing.T) {
	t.Parallel()

	when := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	cases := map[string]string{
		`{{upper "aé"}}`:                      "AÉ",
		`{{lower "AÉ"}}`:                      "aé",
		`{{trimSpace " a "}}`:                 "a",
		`{{contains "abc" "b"}}`:              "true",
		`{{hasPrefix "abc" "a"}}`:             "true",
		`{{hasSuffix "abc" "c"}}`:             "true",
		`{{replace "aXbX" "X" "-"}}`:          "a-b-",
		`{{join (split "a,b" ",") "-"}}`:      "a-b",
		`{{truncate 2 "héllo"}}`:              "hé...",
		`{{truncate 5 "héllo"}}`:              "héllo",
		`{{truncate 0 "abc"}}`:                "...",
		`{{truncate 3 ""}}`:                   "",
		`{{formatTime "2006-01-02" .}}`:       "2026-01-02",
		`{{add 2 3}} {{sub 2 3}}`:             "5 -1",
		`{{(dict "a" 1 "b" "x").b}}`:          "x",
		`{{len (dict)}}`:                      "0",
		`{{index (list 1 "x") 1}}`:            "x",
		`{{len (list)}}`:                      "0",
		`<script>var v = {{json .}}</script>`: `<script>var v = "2026-01-02T03:04:05Z"</script>`,
	}

	for src, want := range cases {
		s := initialized(t, files{"p.html": src}, nil)
		assert.Equal(t, want, body(t, s, "p", when), src)
	}

	s := initialized(t, files{"p.html": `{{formatTimePtr "2006" .}}|{{now.Year}}`}, nil)
	assert.Equal(t, "|", body(t, s, "p", (*time.Time)(nil))[:1])
	assert.Equal(t, "2026|", body(t, s, "p", &when)[:5])
}

// title is not a default function: it was strings.ToTitle, which upper
// cases every letter, and a page that calls it fails to compile.
func TestFuncs_NoTitle(t *testing.T) {
	t.Parallel()

	s := initialized(t, files{"p.html": `{{title "a b"}}`}, nil)

	_, err := render(t, s, "p", nil)
	require.ErrorIs(t, err, ui.ErrCompile)
	require.EqualError(
		t,
		err,
		`gas/ui: render "p": compile failed: template: p.html:1: function "title" not defined`,
	)
}

// truncate counts runes, so it never cuts one in half, and a negative
// length is an execute error rather than a panic; dict with an odd count
// or a key that is not a string, and json of a value it cannot encode,
// are execute errors rather than dropped pairs or `null`.
func TestFuncs_Errors(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		`{{truncate -1 "abc"}}`: `gas/ui: render "p": execute failed: template: p.html:1:2: executing "p.html" at ` +
			`<truncate -1 "abc">: error calling truncate: truncate needs a length of 0 or more`,
		`{{dict "a" 1 "b"}}`: `gas/ui: render "p": execute failed: template: p.html:1:2: executing "p.html" at ` +
			`<dict "a" 1 "b">: error calling dict: dict needs key and value pairs`,
		`{{dict "a" 1 2 3}}`: `gas/ui: render "p": execute failed: template: p.html:1:2: executing "p.html" at ` +
			`<dict "a" 1 2 3>: error calling dict: dict key is not a string: argument 3`,
		`{{json .}}`: `gas/ui: render "p": execute failed: template: p.html:1:2: executing "p.html" at <json .>: ` +
			`error calling json: json: unsupported type: chan int`, //nolint:dupword // json's error after the function's name.
	}

	for src, want := range cases {
		s := initialized(t, files{"p.html": src}, nil)

		_, err := render(t, s, "p", make(chan int))
		require.ErrorIs(t, err, ui.ErrExecute, src)
		require.EqualError(t, err, want, src)
	}
}

// buildId is the same for every call within a build and changes with
// each build.
func TestFuncs_BuildID(t *testing.T) {
	t.Parallel()

	s := initialized(t, files{"p.html": `{{buildId}}|{{buildId}}`}, nil)

	first := body(t, s, "p", nil)
	assert.Len(t, first, 73)
	assert.Equal(t, first[:36], first[37:])
	assert.Equal(t, first, body(t, s, "p", nil))

	require.NoError(t, s.Reload(t.Context()))
	assert.NotEqual(t, first, body(t, s, "p", nil))

	r := initialized(
		t,
		files{"p.html": `{{buildId}}`},
		func(set *ui.Settings) { set.Reload = true },
	)
	one, two := body(t, r, "p", nil), body(t, r, "p", nil)
	assert.NotEqual(t, one, two)
}

// RegisterFuncs checks every function before it adds any: a name that is
// not a Go identifier, a nil value, a value that is not a function, or a
// function with results html/template cannot call is ErrInvalidFunc, so
// that html/template's Funcs never panics.
func TestRegisterFuncs_Invalid(t *testing.T) {
	t.Parallel()

	s := initialized(t, files{"p.html": `{{ok}}`}, nil)

	var nilFunc func() string

	cases := []struct {
		funcs template.FuncMap
		want  string
	}{
		{template.FuncMap{"a-b": func() string { return "" }}, `"a-b": not a Go identifier`},
		{template.FuncMap{"": func() string { return "" }}, `"": not a Go identifier`},
		{template.FuncMap{"1a": func() string { return "" }}, `"1a": not a Go identifier`},
		{template.FuncMap{"x": nil}, `"x": nil`},
		{template.FuncMap{"x": nilFunc}, `"x": nil`},
		{template.FuncMap{"x": "str"}, `"x": not a function`},
		{template.FuncMap{"x": func() {}}, `"x": must return one value, or a value and an error`},
		{
			template.FuncMap{"x": func() (string, string) { return "", "" }},
			`"x": must return one value, or a value and an error`,
		},
		{
			template.FuncMap{
				"ok":  func() string { return "ok" },
				"a-b": func() string { return "" },
			},
			`"a-b": not a Go identifier`,
		},
	}

	for _, c := range cases {
		err := s.RegisterFuncs(c.funcs)
		require.ErrorIs(t, err, ui.ErrInvalidFunc, c.want)
		require.EqualError(t, err, "gas/ui: register funcs: invalid func: "+c.want)
	}

	_, err := render(t, s, "p", nil)
	require.ErrorIs(t, err, ui.ErrCompile, "no function of a refused map is added")

	require.NoError(t, s.RegisterFuncs(nil))
	require.NoError(
		t,
		s.RegisterFuncs(template.FuncMap{"ok": func() (string, error) { return "ok", nil }}),
	)
	assert.Equal(t, "ok", body(t, s, "p", nil))
}

// A registered function replaces a default or an earlier one of the same
// name, and a warn-level log names it; the next render compiles again.
func TestRegisterFuncs_Replace(t *testing.T) {
	t.Parallel()

	s, log := initializedLog(t, newStore(t, files{"p.html": `{{upper "a"}}{{x}}`}), nil,
		ui.WithFuncs(template.FuncMap{"x": func() string { return "1" }}))

	assert.Equal(t, "A1", body(t, s, "p", nil))

	require.NoError(
		t,
		s.RegisterFuncs(template.FuncMap{"upper": func(string) string { return "u" }}),
	)
	assert.Equal(t, "u1", body(t, s, "p", nil))

	require.NoError(t, s.RegisterFuncs(template.FuncMap{"x": func() string { return "2" }}))
	require.NoError(
		t,
		s.RegisterFuncs(
			template.FuncMap{"x": func() string { return "3" }, "y": func() string { return "" }},
		),
	)
	assert.Equal(t, "u3", body(t, s, "p", nil))

	var replaced []any

	for _, e := range log.entries() {
		if e.msg == "template func replaced" {
			assert.Equal(t, "warn", e.level)

			replaced = append(replaced, e.fields["func"])
		}
	}

	assert.Equal(t, []any{"upper", "x", "x"}, replaced)
}

// WithFuncs adds functions over the defaults; Init refuses a map
// RegisterFuncs would refuse.
func TestWithFuncs(t *testing.T) {
	t.Parallel()

	s := initialized(t, files{"p.html": `{{lower (shout "A")}}`}, nil,
		ui.WithFuncs(template.FuncMap{"shout": func(v string) string { return v + "!" }}))
	assert.Equal(t, "a!", body(t, s, "p", nil))

	b, _ := build(
		t,
		newStore(t, nil),
		ui.WithConfig(cfgWith(nil)),
		ui.WithFuncs(template.FuncMap{"bad": 1}),
	)
	err := b.Init(t.Context())
	require.ErrorIs(t, err, ui.ErrInvalidFunc)
	require.EqualError(t, err, `gas/ui: init: invalid func: "bad": not a function`)
}

// A function named `html`, `urlquery` or with html/template's prefix was
// once taken and replaced the escaper html/template runs data through,
// so `{{. | html}}` in an attribute wrote data unescaped. Both WithFuncs
// and RegisterFuncs now refuse those names, and a function named as
// another built-in, such as `len`, replaces it with a warn-level log.
func TestFuncs_ReservedAndBuiltinNames(t *testing.T) {
	t.Parallel()

	id := func(s string) string { return s }

	for _, name := range []string{"html", "urlquery", "_html_template_attrescaper"} {
		r := initialized(t, files{"p.html": `{{ok}}`}, nil)

		err := r.RegisterFuncs(template.FuncMap{"ok": func() string { return "ok" }, name: id})
		require.ErrorIs(t, err, ui.ErrInvalidFunc, name)
		require.EqualError(
			t,
			err,
			`gas/ui: register funcs: invalid func: "`+name+`": reserved by html/template`,
		)

		_, err = render(t, r, "p", nil)
		require.ErrorIs(t, err, ui.ErrCompile, "no function of a refused map is added")

		b, _ := build(
			t,
			newStore(t, nil),
			ui.WithConfig(cfgWith(nil)),
			ui.WithFuncs(template.FuncMap{name: id}),
		)

		err = b.Init(t.Context())
		require.ErrorIs(t, err, ui.ErrInvalidFunc, name)
		require.EqualError(
			t,
			err,
			`gas/ui: init: invalid func: "`+name+`": reserved by html/template`,
		)
	}

	s, log := initializedLog(
		t,
		newStore(t, files{"p.html": `<b title="{{. | html}}">{{len .}}{{printf "%s" .}}</b>`}),
		nil,
		ui.WithFuncs(template.FuncMap{"len": func(string) int { return 7 }}),
	)

	require.ErrorIs(t, s.RegisterFuncs(template.FuncMap{"html": id}), ui.ErrInvalidFunc)
	require.NoError(
		t,
		s.RegisterFuncs(template.FuncMap{"printf": func(string, string) string { return "P" }}),
	)
	assert.Equal(t, `<b title="&lt;x&gt;">7P</b>`, body(t, s, "p", "<x>"))

	var warned []any

	for _, e := range log.entries() {
		if e.msg == "template func replaces a builtin" {
			assert.Equal(t, "warn", e.level)

			warned = append(warned, e.fields["func"])
		}
	}

	assert.Equal(t, []any{"len", "printf"}, warned)
}
