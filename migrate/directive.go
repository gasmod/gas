package migrate

import (
	"fmt"
	"strings"
)

// The directives a migration's SQL can open with, as described in the
// package documentation.
const (
	// directiveNoTransaction runs the SQL without a transaction.
	directiveNoTransaction = "no-transaction"
	// directiveNoSplit sends MySQL SQL as one statement.
	directiveNoSplit = "no-split"
	// directivePrefix starts a directive, after `--` and white space.
	directivePrefix = "gas:"
)

// directives are what the directives of one direction's SQL ask for.
type directives struct {
	// noTransaction runs the SQL without a transaction.
	noTransaction bool
	// noSplit sends MySQL SQL as one statement.
	noSplit bool
}

// parseDirectives returns the directives that sql opens with: the
// `-- gas:` comments, with white space after `--`, among its leading lines
// that are blank or start with `--`. An unknown directive is an error wrapping
// ErrUnknownDirective that names its line, from 1, and not its text.
func parseDirectives(sql string) (directives, error) {
	var d directives

	line := 0

	for l := range strings.Lines(sql) {
		line++

		text := strings.TrimSpace(l)
		if text == "" {
			continue
		}

		comment, ok := strings.CutPrefix(text, "--")
		if !ok {
			break
		}

		name, ok := directiveName(comment)
		if !ok {
			continue
		}

		switch name {
		case directiveNoTransaction:
			d.noTransaction = true
		case directiveNoSplit:
			d.noSplit = true
		default:
			return directives{}, fmt.Errorf("%w on line %d", ErrUnknownDirective, line)
		}
	}

	return d, nil
}

// directiveName returns the name of the directive in comment, the text of
// a line after its `--`, and reports whether it holds one. MySQL reads
// `--` as a comment only when white space follows it, so a directive
// needs it too: `--gas:` opens none.
func directiveName(comment string) (string, bool) {
	if comment == "" || (comment[0] != ' ' && comment[0] != '\t') {
		return "", false
	}

	return strings.CutPrefix(strings.TrimSpace(comment), directivePrefix)
}
