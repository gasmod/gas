package ui

import (
	"encoding/json"
	"html/template"
	"maps"
	"strings"
	"time"
	"uuid"

	"github.com/gasmod/gas/config/extensions/gasenv"
)

var uiBuildID = uuid.New().String()

func merge(funcMaps ...template.FuncMap) template.FuncMap {
	res := make(template.FuncMap)
	for _, m := range funcMaps {
		maps.Copy(res, m)
	}
	return res
}

func strFns() template.FuncMap {
	return template.FuncMap{
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
	}
}

func timeFns() template.FuncMap {
	return template.FuncMap{
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
	}
}

func mathFns() template.FuncMap {
	return template.FuncMap{
		"add": func(a, b int) int { return a + b },
		"sub": func(a, b int) int { return a - b },
	}
}

func collFns() template.FuncMap {
	return template.FuncMap{
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
	}
}

func jsonFns() template.FuncMap {
	return template.FuncMap{
		"json": func(val any) json.RawMessage {
			data, err := json.Marshal(val)
			if err != nil {
				return nil
			}
			return data
		},
	}
}

func buildFns(env gasenv.Environment) template.FuncMap {
	return template.FuncMap{
		"env": func() gasenv.Environment {
			return env
		},
		"buildId": func() string {
			if env.IsDevelopmentLike() {
				return "dev-" + uuid.New().String()
			}
			return uiBuildID
		},
	}
}

// DefaultFuncMap returns the template functions available in every template.
func DefaultFuncMap(env gasenv.Environment) template.FuncMap {
	return merge(
		strFns(),
		timeFns(),
		mathFns(),
		collFns(),
		jsonFns(),
		buildFns(env),
	)
}
