package dotenv

import (
	"maps"
	"strings"
	"testing"
)

// FuzzParse feeds arbitrary bytes to Parse. It must never panic, must be
// deterministic, and every key it returns must be non-empty and trimmed.
func FuzzParse(f *testing.F) {
	for _, seed := range []string{
		"", "A=1", "A=\"x\ny\"", "A='x", "# c\nA=1 # n", "export A=1",
		"A=\"x # y\"", "=1", "A==", "A=\"\"\"", "\"A\"=1", "A=1\r\n",
	} {
		f.Add([]byte(seed))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		got, err := Parse(data)
		again, againErr := Parse(data)
		if (err == nil) != (againErr == nil) || !maps.Equal(got, again) {
			t.Fatalf("Parse is not deterministic for %q", data)
		}
		if err != nil {
			return
		}
		for k := range got {
			if k == "" || k != strings.TrimSpace(k) {
				t.Errorf("Parse(%q) returned key %q", data, k)
			}
		}
	})
}

// FuzzParseRoundTrip writes one KEY="value" line from a valid key and a
// single-line value without double quotes, and expects the value back.
func FuzzParseRoundTrip(f *testing.F) {
	f.Add("KEY", "value")
	f.Add("A_1", "with # hash")
	f.Add("B", " spaced ")
	f.Add("C", "it's")
	f.Add("D", "")

	f.Fuzz(func(t *testing.T, key, value string) {
		if !validKey(key) || strings.ContainsAny(value, "\"\r\n") {
			t.Skip()
		}

		line := key + `="` + value + `"`
		got, err := Parse([]byte(line))
		if err != nil {
			t.Fatalf("Parse(%q) error: %v", line, err)
		}
		if got[key] != value {
			t.Errorf("Parse(%q)[%q] = %q, want %q", line, key, got[key], value)
		}
	})
}

// validKey reports whether key is a conventional environment variable
// name: a letter or underscore followed by letters, digits, or underscores.
func validKey(key string) bool {
	if key == "" {
		return false
	}
	for i, r := range key {
		switch {
		case r == '_', r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}
