package ui

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"maps"
	"reflect"
	"slices"
	"strings"
	"time"
	"unicode"
)

// The errors of the default functions, which a page's execute error
// carries after `error calling <name>: `.
var (
	errNegativeTruncate = errors.New("truncate needs a length of 0 or more")
	errDictPairs        = errors.New("dict needs key and value pairs")
	errDictKey          = errors.New("dict key is not a string")
)

// ellipsis ends a string truncate cut.
const ellipsis = "..."

// defaultFuncs returns the default template functions of a build whose ID
// is buildID, as described in the package documentation.
func defaultFuncs(buildID string) template.FuncMap {
	return template.FuncMap{
		"upper":         strings.ToUpper,
		"lower":         strings.ToLower,
		"trimSpace":     strings.TrimSpace,
		"contains":      strings.Contains,
		"hasPrefix":     strings.HasPrefix,
		"hasSuffix":     strings.HasSuffix,
		"replace":       strings.ReplaceAll,
		"join":          strings.Join,
		"split":         strings.Split,
		"truncate":      truncate,
		"now":           time.Now,
		"formatTime":    formatTime,
		"formatTimePtr": formatTimePtr,
		"add":           func(a, b int) int { return a + b },
		"sub":           func(a, b int) int { return a - b },
		"dict":          dict,
		"list":          func(items ...any) []any { return items },
		"json":          marshalJSON,
		"buildId":       func() string { return buildID },
	}
}

// truncate returns s cut to its first n runes followed by `...`, or s
// when it has n runes or fewer.
func truncate(n int, s string) (string, error) {
	if n < 0 {
		return "", errNegativeTruncate
	}

	runes := 0

	for i := range s {
		if runes == n {
			return s[:i] + ellipsis, nil
		}

		runes++
	}

	return s, nil
}

// formatTime returns t formatted with layout.
func formatTime(layout string, t time.Time) string { return t.Format(layout) }

// formatTimePtr returns *t formatted with layout, or empty for a nil t.
func formatTimePtr(layout string, t *time.Time) string {
	if t == nil {
		return ""
	}

	return t.Format(layout)
}

// dict returns a map of the key and value pairs.
func dict(pairs ...any) (map[string]any, error) {
	if len(pairs)%2 != 0 {
		return nil, errDictPairs
	}

	m := make(map[string]any)

	for i := 0; i < len(pairs); i += 2 {
		k, ok := pairs[i].(string)
		if !ok {
			return nil, fmt.Errorf("%w: argument %d", errDictKey, i+1)
		}

		m[k] = pairs[i+1]
	}

	return m, nil
}

// marshalJSON returns v encoded as JSON, which html/template writes as
// JSON in a script and escapes elsewhere.
func marshalJSON(v any) (json.RawMessage, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err //nolint:wrapcheck // the execute error names the function.
	}

	return b, nil
}

// errorType is the type of error, the second result a function may have.
var errorType = reflect.TypeFor[error]()

// checkFuncs returns nil when html/template can take every function of
// funcs, and otherwise an error wrapping ErrInvalidFunc that names the
// first, in name order, it cannot. These are the checks of
// text/template's Funcs, which panics where this returns an error, and a
// nil function, which it would take and fail to call.
func checkFuncs(funcs template.FuncMap) error {
	for _, name := range slices.Sorted(maps.Keys(funcs)) {
		err := checkFunc(name, funcs[name])
		if err != nil {
			return fmt.Errorf("%w: %q: %w", ErrInvalidFunc, name, err)
		}
	}

	return nil
}

// builtinFuncs are the built-in functions of text/template a function
// may replace, with a warning: html and urlquery are refused instead.
var builtinFuncs = []string{
	"and", "call", "eq", "ge", "gt", "index", "js", "le", "len", "lt", "ne", "not", "or",
	"print", "printf", "println", "slice",
}

// reservedPrefix starts the names of the functions html/template adds to
// escape data.
const reservedPrefix = "_html_template_"

// The reasons checkFunc gives.
var (
	errFuncName     = errors.New("not a Go identifier")
	errFuncReserved = errors.New("reserved by html/template")
	errFuncNil      = errors.New("nil")
	errFuncKind     = errors.New("not a function")
	errFuncResult   = errors.New("must return one value, or a value and an error")
)

// checkFunc returns the reason fn, named name, is not a template
// function, or nil.
func checkFunc(name string, fn any) error {
	err := checkName(name)
	if err != nil {
		return err
	}

	v := reflect.ValueOf(fn)

	switch {
	case !v.IsValid():
		return errFuncNil
	case v.Kind() != reflect.Func:
		return errFuncKind
	case v.IsNil():
		return errFuncNil
	}

	typ := v.Type()
	if typ.NumOut() == 1 || typ.NumOut() == 2 && typ.Out(1) == errorType {
		return nil
	}

	return errFuncResult
}

// checkName returns the reason name cannot name a function, or nil: it
// is not a Go identifier, or html/template escapes through it.
func checkName(name string) error {
	switch {
	case !goodName(name):
		return errFuncName
	case name == "html", name == "urlquery", strings.HasPrefix(name, reservedPrefix):
		return errFuncReserved
	default:
		return nil
	}
}

// goodName reports whether name is a Go identifier, as text/template's
// check of a function name does.
func goodName(name string) bool {
	if name == "" {
		return false
	}

	for i, r := range name {
		switch {
		case r == '_':
		case i == 0 && !unicode.IsLetter(r):
			return false
		case !unicode.IsLetter(r) && !unicode.IsDigit(r):
			return false
		}
	}

	return true
}
