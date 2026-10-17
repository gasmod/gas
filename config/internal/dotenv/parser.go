package dotenv

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"
)

// Parse parses dotenv-style configuration and returns a map of key->value.
// It supports quoted values and multi-line continuations inside quotes.
// It also supports inline comments starting with #, which are ignored except when inside quotes.
func Parse(data []byte) (map[string]string, error) {
	env := make(map[string]string)
	scanner := bufio.NewScanner(bytes.NewReader(data))

	var (
		key          string
		valueBuilder strings.Builder
		inMultiline  bool
		quoteChar    rune
	)

	for scanner.Scan() {
		line := scanner.Text()

		if inMultiline {
			handleMultiline(line, &valueBuilder, key, quoteChar, &inMultiline, env)

			continue
		}

		k, val, multi, q := parseSingleLine(line)
		if k == "" {
			continue
		}

		if multi {
			inMultiline = true
			key = k
			quoteChar = q

			valueBuilder.WriteString(val)

			continue
		}

		env[k] = val
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scanning dotenv: %w", err)
	}

	return env, nil
}

// removeInlineComment removes inline comments starting with #, ignoring those inside quotes.
// It scans bytes, which is safe for UTF-8 because quotes and # are single-byte ASCII
// and never occur inside a multi-byte sequence; it returns the line up to the comment
// unchanged, so non-ASCII text is kept byte for byte.
func removeInlineComment(line string) string {
	inQuote := false
	quoteChar := byte(0)

	for i := range len(line) {
		c := line[i]
		switch {
		case c == '"' || c == '\'':
			if !inQuote {
				inQuote = true
				quoteChar = c
			} else if c == quoteChar {
				inQuote = false
			}

		case c == '#' && !inQuote:
			return line[:i]
		}
	}

	return line
}

// parseSingleLine processes a single line when not in multiline mode and extracts key-value pair.
// It returns key, value, whether it's a multiline start, and the quote character if multiline.
func parseSingleLine(line string) (key, value string, isMultiline bool, quote rune) {
	line = strings.TrimSpace(line)
	line = removeInlineComment(line)

	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false, 0
	}

	parts := strings.SplitN(line, "=", 2)
	if len(parts) != 2 {
		return "", "", false, 0
	}

	key = strings.TrimSpace(parts[0])
	value = strings.TrimSpace(parts[1])

	// Check for multiline start
	if (strings.HasPrefix(value, `"`) && !strings.HasSuffix(strings.TrimRight(value, " \t"), `"`)) ||
		(strings.HasPrefix(value, `'`) && !strings.HasSuffix(strings.TrimRight(value, " \t"), `'`)) {
		quote = rune(value[0])

		return key, strings.TrimPrefix(value, string(quote)), true, quote
	}

	// Trim quotes for single-line values
	value = trimQuotes(value)

	return key, value, false, 0
}

// trimQuotes removes quotes from the value if it starts and ends with matching quotes.
func trimQuotes(val string) string {
	if strings.HasPrefix(val, `"`) && strings.HasSuffix(val, `"`) {
		return strings.Trim(val, `"`)
	} else if strings.HasPrefix(val, `'`) && strings.HasSuffix(val, `'`) {
		return strings.Trim(val, `'`)
	}

	return val
}

// handleMultiline handles appending to a multiline value and checks for end of multiline.
func handleMultiline(
	line string,
	vb *strings.Builder,
	key string,
	quote rune,
	inMulti *bool,
	env map[string]string,
) {
	vb.WriteString("\n")
	vb.WriteString(line)

	if strings.HasSuffix(strings.TrimRight(line, " \t"), string(quote)) {
		value := strings.Trim(vb.String(), string(quote))
		env[key] = value
		*inMulti = false

		vb.Reset()
	}
}
