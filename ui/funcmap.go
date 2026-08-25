package ui

import (
	"encoding/json"
	"html/template"
	"strings"
	"time"

	env "github.com/gasmod/gas/config/extensions/gasenv"

	"github.com/google/uuid"
)

var uiBuildID = uuid.NewString()

// DefaultFuncMap returns the template functions available in every template.
func DefaultFuncMap(e env.Environment) template.FuncMap {
	return template.FuncMap{
		// Markup safety — use when you trust the source.
		"safe":     func(s string) template.HTML { return template.HTML(s) },         //nolint:gosec // intentional
		"safeAttr": func(s string) template.HTMLAttr { return template.HTMLAttr(s) }, //nolint:gosec // intentional
		"safeURL":  func(s string) template.URL { return template.URL(s) },           //nolint:gosec // intentional

		// Strings.
		"upper":     strings.ToUpper,
		"lower":     strings.ToLower,
		"title":     strings.ToTitle,
		"trimSpace": strings.TrimSpace,
		"contains":  strings.Contains,
		"hasPrefix": strings.HasPrefix,
		"hasSuffix": strings.HasSuffix,
		"replace":   strings.ReplaceAll,
		"join":      strings.Join,
		"split":     strings.Split,

		"truncate": func(n int, s string) string {
			if len(s) <= n {
				return s
			}
			return s[:n] + "..."
		},

		// Time.
		"now": time.Now,
		"formatTime": func(layout string, t time.Time) string {
			return t.Format(layout)
		},
		"formatTimePtr": func(layout string, t *time.Time) string {
			if t == nil {
				return ""
			}
			return t.Format(layout)
		},

		// Arithmetic.
		"add": func(a, b int) int { return a + b },
		"sub": func(a, b int) int { return a - b },

		// Collections — extremely useful for passing data to partials.
		"dict": func(pairs ...any) map[string]any {
			m := make(map[string]any, len(pairs)/2)
			for i := 0; i+1 < len(pairs); i += 2 {
				if k, ok := pairs[i].(string); ok {
					m[k] = pairs[i+1]
				}
			}
			return m
		},
		"list": func(items ...any) []any { return items },

		// Serialization.
		"json": func(val any) json.RawMessage {
			data, err := json.Marshal(val)
			if err != nil {
				return nil
			}
			return data
		},

		"buildId": func() string {
			if e.IsDevelopmentLike() {
				return "dev-" + uuid.NewString()
			}
			return uiBuildID
		},
	}
}
