package mysqlsplit

import "strings"

// Mode is what of a session's SQL mode changes how the text is read, as
// described in the package documentation.
type Mode struct {
	// NoBackslashEscapes reads a backslash in a string as an ordinary
	// character, as the mode NO_BACKSLASH_ESCAPES does.
	NoBackslashEscapes bool
	// ANSIQuotes reads `"` as quoting an identifier, as the mode
	// ANSI_QUOTES does.
	ANSIQuotes bool
}

// ParseMode returns the Mode of sqlMode, a value of MySQL's @@sql_mode: a
// comma-separated list of mode names, which MySQL reports in upper case
// and with combination modes such as ANSI expanded.
func ParseMode(sqlMode string) Mode {
	var m Mode

	for name := range strings.SplitSeq(sqlMode, ",") {
		switch strings.ToUpper(strings.TrimSpace(name)) {
		case "NO_BACKSLASH_ESCAPES":
			m.NoBackslashEscapes = true
		case "ANSI_QUOTES":
			m.ANSIQuotes = true
		}
	}

	return m
}

// space is the white space trimmed from a statement.
const space = " \t\n\r\f\v"

// The delimiters of a block comment.
const (
	// blockOpen starts a block comment.
	blockOpen = "/*"
	// blockClose ends a block comment.
	blockClose = "*/"
)

// Split returns the statements of sql, read in mode, as described in the
// package documentation.
//
//nolint:cyclop // one case per lexer state; splitting it hides them.
func Split(sql string, mode Mode) []string {
	var (
		out     []string
		start   int
		hasCode bool
	)

	for i := 0; i < len(sql); {
		c := sql[i]

		switch {
		case c == ';':
			out = appendStatement(out, sql[start:i], hasCode)
			start, hasCode = i+1, false
			i++
		case c == '\'' || (c == '"' && !mode.ANSIQuotes):
			hasCode = true
			i = skipQuoted(sql, i, !mode.NoBackslashEscapes)
		case c == '`' || c == '"':
			hasCode = true
			i = skipQuoted(sql, i, false)
		case c == '#' || isLineComment(sql, i):
			i = skipLine(sql, i)
		case c == '/' && i+1 < len(sql) && sql[i+1] == '*':
			if i+2 < len(sql) && sql[i+2] == '!' {
				hasCode = true
			}

			i = skipBlock(sql, i)
		default:
			if !strings.ContainsRune(space, rune(c)) {
				hasCode = true
			}

			i++
		}
	}

	return appendStatement(out, sql[start:], hasCode)
}

// appendStatement appends stmt, trimmed, to out when hasCode reports that
// it holds more than white space and comments.
func appendStatement(out []string, stmt string, hasCode bool) []string {
	if !hasCode {
		return out
	}

	return append(out, strings.Trim(stmt, space))
}

// isLineComment reports whether the `--` comment starts at i: two dashes
// followed by white space, a control character or the end of the text.
func isLineComment(sql string, i int) bool {
	if !strings.HasPrefix(sql[i:], "--") {
		return false
	}

	return i+2 == len(sql) || sql[i+2] <= ' '
}

// skipQuoted returns the index after the string or quoted identifier that
// starts with the quote at i, or the length of sql when it is not closed.
// The quote doubled stands for itself, and with escapes a backslash
// escapes the next character.
func skipQuoted(sql string, i int, escapes bool) int {
	quote := sql[i]

	for i++; i < len(sql); i++ {
		switch sql[i] {
		case '\\':
			if escapes {
				i++
			}
		case quote:
			if i+1 < len(sql) && sql[i+1] == quote {
				i++

				continue
			}

			return i + 1
		}
	}

	return len(sql)
}

// skipLine returns the index of the newline that ends the comment at i,
// or the length of sql when no newline follows. The newline itself is
// white space and is left for the caller.
func skipLine(sql string, i int) int {
	n := strings.IndexByte(sql[i:], '\n')
	if n < 0 {
		return len(sql)
	}

	return i + n
}

// skipBlock returns the index after the `*/` that ends the comment
// starting at i, or the length of sql when it is not closed.
func skipBlock(sql string, i int) int {
	start := i + len(blockOpen)

	n := strings.Index(sql[start:], blockClose)
	if n < 0 {
		return len(sql)
	}

	return start + n + len(blockClose)
}
