package dotenv_test

import (
	"bytes"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"runtime/debug"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/config/internal/dotenv"
)

func parse(t *testing.T, in string) (map[string]string, error) {
	t.Helper()

	var p dotenv.Parser

	return p.Parse([]byte(in))
}

// parseCase is an input that must parse, and the map it must parse to.
type parseCase struct {
	want map[string]string
	name string
	in   string
}

func runParseCases(t *testing.T, tests []parseCase) {
	t.Helper()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := parse(t, tt.in)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParse_Syntax(t *testing.T) {
	t.Parallel()

	runParseCases(t, []parseCase{
		{name: "empty input", in: "", want: map[string]string{}},
		{name: "only newlines", in: "\n\n\n", want: map[string]string{}},
		{name: "only comment", in: "# c", want: map[string]string{}},
		{name: "single pair", in: "A=1", want: map[string]string{"A": "1"}},
		{name: "trailing newline", in: "A=1\n", want: map[string]string{"A": "1"}},
		{name: "lowercase and digits in key", in: "a_1=x", want: map[string]string{"a_1": "x"}},
		{name: "leading underscore key", in: "_A=x", want: map[string]string{"_A": "x"}},
		{name: "empty value at EOF", in: "A=", want: map[string]string{"A": ""}},
		{name: "empty value then line", in: "A=\nB=2", want: map[string]string{"A": "", "B": "2"}},
		{name: "spaces around equals", in: "A = 1", want: map[string]string{"A": "1"}},
		{name: "indented line", in: "  \tA=1", want: map[string]string{"A": "1"}},
		{
			name: "trailing spaces after value",
			in:   "A=1   \nB=2",
			want: map[string]string{"A": "1", "B": "2"},
		},
		{
			name: "inline comment after space",
			in:   "A=1 # c\nB=2",
			want: map[string]string{"A": "1", "B": "2"},
		},
		{name: "comment right after value", in: "A=1#c", want: map[string]string{"A": "1"}},
		{name: "utf8 value", in: "A=héllo✓", want: map[string]string{"A": "héllo✓"}},
		{name: "literal U+FFFD value", in: "A=�", want: map[string]string{"A": "�"}},
		{name: "equals in value", in: "A=b=c", want: map[string]string{"A": "b=c"}},
		{name: "keys keep their case", in: "Db_Host=x", want: map[string]string{"Db_Host": "x"}},
	})
}

func TestParse_Quoting(t *testing.T) {
	t.Parallel()

	runParseCases(t, []parseCase{
		{name: "double quoted", in: `A="x y"`, want: map[string]string{"A": "x y"}},
		{name: "single quoted", in: `A='x y'`, want: map[string]string{"A": "x y"}},
		{name: "empty double quoted", in: `A=""`, want: map[string]string{"A": ""}},
		{name: "hash inside quotes", in: `A="x # y"`, want: map[string]string{"A": "x # y"}},
		{name: "other quote inside quotes", in: `A="it's"`, want: map[string]string{"A": "it's"}},
		{
			name: "quoted then comment",
			in:   "A=\"x\" # c\nB=2",
			want: map[string]string{"A": "x", "B": "2"},
		},
		{
			name: "multiline quoted",
			in:   "A=\"x\ny\"\nB=2",
			want: map[string]string{"A": "x\ny", "B": "2"},
		},
	})
}

// CRLF line endings work everywhere LF does.
func TestParse_CRLF(t *testing.T) {
	t.Parallel()

	runParseCases(t, []parseCase{
		{name: "unquoted", in: "A=1\r\nB=2\r\n", want: map[string]string{"A": "1", "B": "2"}},
		{
			name: "after quoted value",
			in:   "A=\"x\"\r\nB=2",
			want: map[string]string{"A": "x", "B": "2"},
		},
		{
			name: "after trailing space",
			in:   "A=1 \r\nB=2",
			want: map[string]string{"A": "1", "B": "2"},
		},
		{
			name: "after inline comment",
			in:   "A=1 # c\r\nB=2",
			want: map[string]string{"A": "1", "B": "2"},
		},
		{name: "leading blank line", in: "\r\nA=1", want: map[string]string{"A": "1"}},
		{
			name: "blank lines between",
			in:   "A=1\r\n\r\nB=2",
			want: map[string]string{"A": "1", "B": "2"},
		},
		{name: "empty value", in: "A=\r\nB=2", want: map[string]string{"A": "", "B": "2"}},
	})
}

func TestParse_Interpolation(t *testing.T) {
	t.Parallel()

	runParseCases(t, []parseCase{
		{name: "interpolation", in: "A=1\nB=${A}", want: map[string]string{"A": "1", "B": "1"}},
		{
			name: "interpolation in double quotes",
			in:   "A=1\nB=\"x${A}y\"",
			want: map[string]string{"A": "1", "B": "x1y"},
		},
		{name: "forward reference", in: "B=${A}\nA=1", want: map[string]string{"A": "1", "B": "1"}},
		{
			name: "chain",
			in:   "A=${B}\nB=${C}\nC=x",
			want: map[string]string{"A": "x", "B": "x", "C": "x"},
		},
		{name: "dollar without brace is literal", in: "A=$B", want: map[string]string{"A": "$B"}},
		// A "$" must not consume the newline after it.
		{
			name: "dollar before newline",
			in:   "A=x$\nB=2",
			want: map[string]string{"A": "x$", "B": "2"},
		},
	})
}

// Single-quoted values are literal: "${...}" inside them is not expanded and
// needs no definition.
func TestParse_SingleQuotesDoNotExpand(t *testing.T) {
	t.Parallel()

	runParseCases(t, []parseCase{
		{name: "defined name", in: "B=1\nA='${B}'", want: map[string]string{"A": "${B}", "B": "1"}},
		{name: "undefined name", in: "A='${NOPE}'", want: map[string]string{"A": "${NOPE}"}},
		{name: "self reference", in: "A='${A}'", want: map[string]string{"A": "${A}"}},
		{
			name: "referenced from double quotes",
			in:   "A='${X}'\nB=\"${A}\"",
			want: map[string]string{"A": "${X}", "B": "${X}"},
		},
	})
}

func TestParse_DollarAtEOF(t *testing.T) {
	t.Parallel()

	got, err := parse(t, "A=$")
	require.NoError(t, err)
	assert.Equal(t, "$", got["A"])
}

// A "$" must not consume the closing quote after it.
func TestParse_DollarBeforeClosingQuote(t *testing.T) {
	t.Parallel()

	got, err := parse(t, `A="x$"`)
	require.NoError(t, err)
	assert.Equal(t, "x$", got["A"])
}

// A key may be defined once, and two keys may not differ only in ASCII case.
// The error is at the start of the second definition and names both keys.
func TestParse_DuplicateKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      string
		wantMsg string
	}{
		{name: "same key", in: "A=1\nA=2", wantMsg: `2:1: duplicate key: "A" and "A"`},
		{
			name:    "differs in case",
			in:      "Foo=1\nFOO=2",
			wantMsg: `2:1: duplicate key: "Foo" and "FOO"`,
		},
		{name: "indented", in: "A=1\n  a=2", wantMsg: `2:3: duplicate key: "A" and "a"`},
		{
			name:    "after multiline value",
			in:      "A=\"x\ny\"\nB=1\nA=2",
			wantMsg: `4:1: duplicate key: "A" and "A"`,
		},
		{
			name:    "not adjacent",
			in:      "A=1\nB=2\n# c\na=3",
			wantMsg: `4:1: duplicate key: "A" and "a"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := parse(t, tt.in)
			require.ErrorIs(t, err, dotenv.ErrDuplicateKey)
			assert.Nil(t, got)
			require.EqualError(t, err, tt.wantMsg)
		})
	}
}

// A syntax error in the second definition is reported as that, not as a
// duplicate: the key is checked once the "=" is read.
func TestParse_DuplicateKeyWithoutEquals(t *testing.T) {
	t.Parallel()

	_, err := parse(t, "A=1\nA")
	require.ErrorIs(t, err, dotenv.ErrUnexpectedEOF)
}

func TestParse_DuplicateKeyOmitsValues(t *testing.T) {
	t.Parallel()

	_, err := parse(t, "TOKEN=hunter2\nToken=swordfish")
	require.ErrorIs(t, err, dotenv.ErrDuplicateKey)
	assert.NotContains(t, err.Error(), "hunter2")
	assert.NotContains(t, err.Error(), "swordfish")
}

// References match keys exactly, so a reference that differs in case from
// the only key is undefined.
func TestParse_ReferenceMatchesCase(t *testing.T) {
	t.Parallel()

	_, err := parse(t, "DB_HOST=x\nA=${db_host}")
	require.ErrorIs(t, err, dotenv.ErrVarUndef)
}

// Text produced by one substitution is not scanned again: "${D}{C}" with
// D="$" is the literal "${C}", not a reference to C.
func TestParse_SubstitutedTextIsNotRescanned(t *testing.T) {
	t.Parallel()

	got, err := parse(t, "D=$\nC=x\nB=${D}{C}${C}")
	require.NoError(t, err)
	assert.Equal(t, "${C}x", got["B"])
}

// Interpolation must not grow a small input into an unbounded value. Each
// case expands to 64 MiB from under 64 KiB of input.
func TestParse_ExpansionIsCapped(t *testing.T) {
	t.Parallel()

	var doubling strings.Builder

	doubling.WriteString("V0=x\n")

	for i := 1; i <= 26; i++ {
		_, _ = fmt.Fprintf(&doubling, "V%d=${V%d}${V%d}\n", i, i-1, i-1)
	}

	fanOut := "A=" + strings.Repeat("x", 64<<10) + "\nB=" + strings.Repeat("${A}", 1<<10)

	for name, in := range map[string]string{"doubling chain": doubling.String(), "fan out": fanOut} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := parse(t, in)
			require.Error(t, err)
		})
	}
}

// The cap leaves ordinary expansion alone.
func TestParse_SmallExpansionIsNotCapped(t *testing.T) {
	t.Parallel()

	in := "A=" + strings.Repeat("x", 1<<10) + "\nB=" + strings.Repeat("${A}", 4)
	got, err := parse(t, in)
	require.NoError(t, err)
	assert.Len(t, got["B"], 4<<10)
}

func TestParse_Errors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		want error
		name string
		in   string
	}{
		{name: "invalid utf8 at top level", in: "\xff", want: dotenv.ErrInvalidUTF8Char},
		{name: "invalid utf8 in value", in: "A=\xff", want: dotenv.ErrInvalidUTF8Char},
		{name: "invalid utf8 in quoted value", in: "A=\"\xff\"", want: dotenv.ErrInvalidUTF8Char},
		{name: "invalid utf8 in comment", in: "# \xff", want: dotenv.ErrInvalidUTF8Char},
		{name: "line starts with digit", in: "1A=1", want: dotenv.ErrUnexpectedChar},
		{name: "line starts with equals", in: "=1", want: dotenv.ErrUnexpectedChar},
		{name: "space inside key", in: "A B=1", want: dotenv.ErrUnexpectedChar},
		{name: "unquoted value with space", in: "A=x y", want: dotenv.ErrUnexpectedChar},
		{name: "text after closing quote", in: `A="x"y`, want: dotenv.ErrUnexpectedChar},
		{name: "unclosed double quote", in: `A="x`, want: dotenv.ErrUnexpectedEOF},
		{name: "unclosed single quote", in: `A='x`, want: dotenv.ErrUnexpectedEOF},
		{name: "key at EOF", in: "A", want: dotenv.ErrUnexpectedEOF},
		{name: "key then newline", in: "A\n", want: dotenv.ErrUnexpectedChar},
		{name: "unclosed interpolation", in: "A=${B", want: dotenv.ErrUnexpectedEOF},
		{name: "bad char in interpolation", in: "A=${B-C}", want: dotenv.ErrVarName},
		{name: "empty interpolation name", in: "A=${}", want: dotenv.ErrVarName},
		{name: "undefined reference", in: "B=x${NOPE}y", want: dotenv.ErrVarUndef},
		{name: "undefined reference in quotes", in: "B=\"${NOPE}\"", want: dotenv.ErrVarUndef},
		{name: "self reference", in: "A=${A}", want: dotenv.ErrVarCycle},
		{name: "two-step cycle", in: "A=${B}\nB=${A}", want: dotenv.ErrVarCycle},
		{name: "three-step cycle", in: "A=${B}\nB=${C}\nC=${A}", want: dotenv.ErrVarCycle},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := parse(t, tt.in)
			require.ErrorIs(t, err, tt.want)
		})
	}
}

// A key character outside [A-Za-z0-9_] is an error, not the start of the
// value.
func TestParse_InvalidKeyCharIsRejected(t *testing.T) {
	t.Parallel()

	for _, in := range []string{"A-B=1", "A.B=1", "A:1", "Aé=1"} {
		got, err := parse(t, in)
		require.Error(t, err, "Parse(%q) = %q", in, got)
	}
}

// A key without "=" is an error whether or not a newline follows it.
func TestParse_KeyWithoutEqualsIsConsistent(t *testing.T) {
	t.Parallel()

	_, errEOF := parse(t, "A")
	_, errNL := parse(t, "A\n")
	require.Error(t, errEOF)
	require.Error(t, errNL)
}

// A "\r" that is not followed by "\n" is not a line ending. Rejecting it is
// fine; dropping it, or changing the bytes around it, is not.
func TestParse_LoneCRInUnquotedValue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "before text", in: "A=1\rx", want: "1\rx"},
		{name: "at EOF", in: "A=1\r", want: "1\r"},
		{name: "before CRLF", in: "A=x\r\r\nB=1", want: "x\r"},
		{name: "before interpolation", in: "A=1\r${B}\nB=2", want: "1\r2"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := parse(t, tt.in)
			if err != nil {
				return
			}

			assert.Equal(t, tt.want, got["A"])
		})
	}
}

// A lone "\r" after a separator must get the same treatment as anywhere else
// that separator is allowed: "A=1\r x" must not parse when "A=1 x" does not.
func TestParse_LoneCRBeforeSeparator(t *testing.T) {
	t.Parallel()

	for _, in := range []string{"A=1\r x", "A=1\r#c"} {
		got, err := parse(t, in)
		if err == nil {
			assert.Contains(t, got["A"], "\r", "Parse(%q) = %q", in, got)
		}
	}
}

// "\n\r" is not a line ending: a "\r" starting a line is an error at the
// start of the file and must be one after a "\n" too.
func TestParse_LFCRIsNotALineEnding(t *testing.T) {
	t.Parallel()

	_, errStart := parse(t, "\rB=2")
	_, errAfterLF := parse(t, "A=1\n\rB=2")
	assert.Equal(
		t,
		errStart == nil,
		errAfterLF == nil,
		"\\rB=2: %v, A=1\\n\\rB=2: %v",
		errStart,
		errAfterLF,
	)
}

// Errors start with the line and column of the offending character.
func TestParse_ErrorPosition(t *testing.T) {
	t.Parallel()

	tests := []struct {
		want    error
		name    string
		in      string
		wantPos string
	}{
		{name: "third line", in: "A=1\nB=2\n  1", want: dotenv.ErrUnexpectedChar, wantPos: "3:3:"},
		// Newlines inside a quoted value advance the line count.
		{
			name:    "after multiline value",
			in:      "A=\"x\ny\nz\"\n1",
			want:    dotenv.ErrUnexpectedChar,
			wantPos: "4:1:",
		},
		// Invalid UTF-8 at the start of a line is reported at that line.
		{
			name:    "invalid utf8 at line start",
			in:      "A=1\n\xff",
			want:    dotenv.ErrInvalidUTF8Char,
			wantPos: "2:1:",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := parse(t, tt.in)
			require.ErrorIs(t, err, tt.want)
			assert.True(t, strings.HasPrefix(err.Error(), tt.wantPos), "got %q", err)
		})
	}
}

// Invalid UTF-8 at the start of a line is not blamed on the key above it.
func TestParse_InvalidUTF8AtLineStartNamesNoKey(t *testing.T) {
	t.Parallel()

	_, err := parse(t, "A=1\n\xff")
	require.ErrorIs(t, err, dotenv.ErrInvalidUTF8Char)
	assert.NotContains(t, err.Error(), `key: "A"`)
}

func TestParse_ErrorNamesKeyNotValue(t *testing.T) {
	t.Parallel()

	_, err := parse(t, `SECRET="hunter2`)
	require.ErrorIs(t, err, dotenv.ErrUnexpectedEOF)
	assert.Contains(t, err.Error(), `"SECRET"`)
	assert.NotContains(t, err.Error(), "hunter2")
}

// A character after a value is often part of what was meant as the value,
// so an error gives its position and never the character itself.
func TestParse_ErrorOmitsCharacter(t *testing.T) {
	t.Parallel()

	//nolint:gosec // G101: test input, not a credential
	for in, want := range map[string]string{
		"PASSWORD=correct horse":   `1:18: unexpected character, want newline: key: "PASSWORD"`,
		`PASSWORD="abc"def`:        `1:15: unexpected character, want newline: key: "PASSWORD"`,
		"PASSWORD=abc\nkey secret": "2:5: unexpected character, want '='",
		"A=${pass-word}":           `1:9: invalid character in variable name: key: "A"`,
	} {
		_, err := parse(t, in)
		require.EqualError(t, err, want, "Parse(%q)", in)
	}
}

// No character of the file reaches the error, so a control character cannot
// reach a terminal or log line raw.
func TestParse_ErrorEscapesControlChars(t *testing.T) {
	t.Parallel()

	for _, in := range []string{"\rB=2", "\x00", "\x1b[2J", "A=1 \r", "A=\"x\"\x1b[2J", "A=x\x07y z"} {
		_, err := parse(t, in)
		require.Error(t, err, "Parse(%q)", in)

		for _, r := range err.Error() {
			if unicode.IsControl(r) {
				t.Errorf("Parse(%q) error %q contains control char %U", in, err, r)

				break
			}
		}
	}
}

// A reused Parser starts from scratch on each call.
func TestParser_Reuse(t *testing.T) {
	t.Parallel()

	var p dotenv.Parser

	_, err := p.Parse([]byte("A=1"))
	require.NoError(t, err)

	got, err := p.Parse([]byte("B=2"))
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"B": "2"}, got)
}

// FuzzParse checks that Parse is deterministic, returns only valid keys, no
// two of which differ only in case, and never invents U+FFFD.
func FuzzParse(f *testing.F) {
	for _, seed := range []string{
		"", "A=1", "A=\"x\ny\"", "A='x", "# c\nA=1 # n", "A=$", "A=${",
		"A=${B}\nB=${A}", "A=\"$\"", "A=1\r\n", "A-B=1", "\xff", "A=x$\nB",
		"A=1\r", "A=\"x\"\r\n", "A=1\n\rB=2",
	} {
		f.Add([]byte(seed))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		var p1, p2 dotenv.Parser

		got, err := p1.Parse(data)
		again, againErr := p2.Parse(data)

		if (err == nil) != (againErr == nil) || !maps.Equal(got, again) {
			t.Fatalf("Parse is not deterministic for %q", data)
		}

		if err != nil {
			return
		}

		lower := make(map[string]string, len(got))
		for k, v := range got {
			if other, ok := lower[strings.ToLower(k)]; ok {
				t.Errorf("Parse(%q) returned keys %q and %q", data, other, k)
			}

			lower[strings.ToLower(k)] = k
			if !validKey(k) {
				t.Errorf("Parse(%q) returned key %q", data, k)
			}

			if strings.ContainsRune(v, '�') && !bytes.ContainsRune(data, '�') {
				t.Errorf("Parse(%q)[%q] = %q invents U+FFFD", data, k, v)
			}
		}
	})
}

// FuzzParseRoundTrip writes KEY='value' for a value with no quote or "$" and
// expects the value back.
func FuzzParseRoundTrip(f *testing.F) {
	f.Add("KEY", "value")
	f.Add("A_1", "with # hash")
	f.Add("B", " spaced ")
	f.Add("C", "multi\nline")
	f.Add("D", "")

	f.Fuzz(func(t *testing.T, key, value string) {
		if !validKey(key) || !utf8.ValidString(value) || strings.ContainsAny(value, "'$") {
			t.Skip()
		}

		line := key + "='" + value + "'"

		var p dotenv.Parser

		got, err := p.Parse([]byte(line))
		if err != nil {
			t.Fatalf("Parse(%q) error: %v", line, err)
		}

		if got[key] != value {
			t.Errorf("Parse(%q)[%q] = %q, want %q", line, key, got[key], value)
		}
	})
}

// validKey reports whether k matches [A-Za-z_][A-Za-z0-9_]*.
func validKey(k string) bool {
	if k == "" || (k[0] >= '0' && k[0] <= '9') {
		return false
	}

	for _, r := range k {
		if !isIdent(r) {
			return false
		}
	}

	return true
}

func isAlpha(c rune) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isAlphaNum(c rune) bool {
	return isAlpha(c) || (c >= '0' && c <= '9')
}

func isIdent(c rune) bool {
	return isAlphaNum(c) || c == '_'
}

// Resolving a chain of references must not need stack in proportion to the
// chain's length. Parse caps neither input size nor chain depth, and a stack
// overflow is a fatal error that recover cannot catch, so the chain runs in
// a child process with a 16 MiB stack limit, where a recursive resolver dies.
func TestParse_LongReferenceChainDoesNotOverflowStack(t *testing.T) {
	t.Parallel()

	const child = "DOTENV_LONG_CHAIN_CHILD"

	if os.Getenv(child) == "1" {
		debug.SetMaxStack(16 << 20)

		const n = 200_000

		var b strings.Builder

		for i := range n {
			_, _ = fmt.Fprintf(&b, "K%d=${K%d}\n", i, i+1)
		}

		_, _ = fmt.Fprintf(&b, "K%d=x\n", n)

		got, err := parse(t, b.String())
		require.NoError(t, err)
		assert.Equal(t, "x", got["K0"])

		return
	}

	//nolint:gosec // reruns this test binary
	cmd := exec.CommandContext(
		t.Context(),
		os.Args[0],
		"-test.run=^TestParse_LongReferenceChainDoesNotOverflowStack$",
	)

	cmd.Env = append(os.Environ(), child+"=1")

	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "child process failed:\n%s", firstLines(string(out), 5))
}

func firstLines(s string, n int) string {
	lines := strings.SplitN(s, "\n", n+1)

	return strings.Join(lines[:min(n, len(lines))], "\n")
}
