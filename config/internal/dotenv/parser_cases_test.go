package dotenv

import (
	"maps"
	"testing"
)

// TestParse_Cases pins the documented syntax, one rule per case.
func TestParse_Cases(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		want  map[string]string
		input string
	}{
		"plain":                 {map[string]string{"A": "1"}, "A=1"},
		"blank and comments":    {map[string]string{"A": "1"}, "\n# comment\n   \nA=1\n#B=2\n"},
		"inline comment":        {map[string]string{"A": "1"}, "A=1 # note"},
		"hash inside quotes":    {map[string]string{"A": "x # y"}, `A="x # y"`},
		"no equals ignored":     {map[string]string{"A": "1"}, "JUNK\nA=1"},
		"trimmed key and value": {map[string]string{"A": "1"}, "  A  =  1  "},
		"double quotes":         {map[string]string{"A": "a b"}, `A="a b"`},
		"single quotes":         {map[string]string{"A": "a b"}, `A='a b'`},
		"spaces inside quotes":  {map[string]string{"A": " a "}, `A=" a "`},
		"empty value":           {map[string]string{"A": ""}, "A="},
		"empty quoted value":    {map[string]string{"A": ""}, `A=""`},
		"equals in value":       {map[string]string{"A": "b=c"}, "A=b=c"},
		"backslash kept":        {map[string]string{"A": `a\nb`}, `A="a\nb"`},
		"export prefix kept":    {map[string]string{"export A": "1"}, "export A=1"},
		"last duplicate wins":   {map[string]string{"A": "2"}, "A=1\nA=2"},
		"multiline":             {map[string]string{"A": "x\ny", "B": "2"}, "A=\"x\ny\"\nB=2"},
		"single-quote multiline": {
			map[string]string{"A": "x\ny"}, "A='x\ny'",
		},
		"crlf": {map[string]string{"A": "1", "B": "2"}, "A=1\r\nB=2\r\n"},
		"utf-8": {
			map[string]string{"NAME": "café", "CITY": "Zürich", "É": "🙂"},
			"NAME=café\nCITY=\"Zürich\" # comment\nÉ=🙂",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := Parse([]byte(tc.input))
			if err != nil {
				t.Fatalf("Parse(%q) error: %v", tc.input, err)
			}
			if !maps.Equal(got, tc.want) {
				t.Errorf("Parse(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// A line longer than the scanner's 64 KB buffer is an error, not a
// truncated value.
func TestParse_LineTooLong(t *testing.T) {
	t.Parallel()

	long := make([]byte, 70*1024)
	for i := range long {
		long[i] = 'x'
	}
	if _, err := Parse(append([]byte("A="), long...)); err == nil {
		t.Error("Parse() of a 70 KB line returned no error")
	}
}
