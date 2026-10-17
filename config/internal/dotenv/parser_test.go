package dotenv

import (
	"strings"
	"testing"
)

// A quoted value whose quote is never closed must be an error, not a
// silent loss of every line after it.
func TestParse_UnclosedQuote(t *testing.T) {
	t.Parallel()

	for name, input := range map[string]string{
		"double at end":    "A=1\nB=\"unterminated\nC=3\n",
		"single at end":    "A=1\nB='unterminated\nC=3\n",
		"last line":        "A=1\nB=\"unterminated",
		"secret not shown": "TOKEN=\"s3cr3t-value\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			env, err := Parse([]byte(input))
			if err == nil {
				t.Fatalf("Parse() = %v, nil; want an error for the unclosed quote", env)
			}
			msg := err.Error()
			if strings.Contains(msg, "s3cr3t") || strings.Contains(msg, "unterminated") {
				t.Errorf("Parse() error %q contains the value", err)
			}
		})
	}
}

func TestParse_UnclosedQuoteNamesKeyAndLine(t *testing.T) {
	t.Parallel()

	_, err := Parse([]byte("A=1\nB=\"unterminated\nC=3\n"))
	if err == nil {
		t.Fatal("Parse() error = nil, want an error")
	}
	if !strings.Contains(err.Error(), `"B"`) || !strings.Contains(err.Error(), "line 2") {
		t.Errorf("Parse() error %q does not name key B and line 2", err)
	}
}

// A closed multi-line value still parses.
func TestParse_ClosedMultiline(t *testing.T) {
	t.Parallel()

	env, err := Parse([]byte("A=\"first\nsecond\"\nB=2\n"))
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	if env["A"] != "first\nsecond" || env["B"] != "2" {
		t.Errorf("Parse() = %q, want A=%q B=%q", env, "first\nsecond", "2")
	}
}
