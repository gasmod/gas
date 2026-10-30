// Package dotenv parses `.env` files into a map of keys to string values.
// It is the parser behind the config module's `.env` provider, and it
// implements the format described below, the GAS Dotenv Spec.
//
// The format is deliberately small: one `KEY=VALUE` per line, comments,
// single and double quotes, and `${NAME}` references to other keys in the
// same file. Anything outside the spec is an error rather than a guess, so a
// file either means exactly one thing or fails to load.
//
// # Usage
//
// The zero value of [Parser] is ready to use. [Parser.Parse] takes the file's
// bytes and returns every key with its final value:
//
//	var p dotenv.Parser
//	values, err := p.Parse(data)
//	if err != nil {
//		return err
//	}
//
// A Parser can be reused, and each call starts from scratch. It keeps state
// between the start and end of a call, so one Parser must not be used by two
// goroutines at once. The returned map does not share memory with `data`.
// An empty input, or one with only comments and blank lines, returns an
// empty, non-nil map; any error returns a nil map.
//
// # GAS Dotenv Spec v0.1
//
// A file is a sequence of lines. Each line is blank, a comment, or one
// definition:
//
//	# Database settings
//	DB_HOST=localhost
//	DB_PORT = 5432              # spaces around "=" are allowed
//	DB_NAME="app db"
//	DB_URL=postgres://${DB_HOST}:${DB_PORT}/app
//	DB_PASSWORD='p@ss${not-a-ref}'
//
// # Encoding and line endings
//
// The file must be valid UTF-8 throughout, comments included; any invalid
// byte sequence is an error. A byte order mark is not skipped, so a file that
// starts with one fails on its first character. A literal U+FFFD in the file
// is ordinary text.
//
// A line ends with `\n` or `\r\n`. A `\r` that is not followed by `\n` is not
// a line ending: inside a value it is part of the value, and at the start of
// a line it is an error.
//
// # Blank lines and comments
//
// A line that is empty or holds only spaces and tabs is ignored. A comment
// starts with `#` and runs to the end of the line. It can fill a whole line,
// optionally indented, or follow a definition:
//
//	# a full-line comment
//	PORT=8080 # an inline comment
//	HOST=localhost# also a comment: "#" ends an unquoted value
//	NAME="x # y"  # inside quotes "#" is text; the value is "x # y"
//
// # Keys
//
// A key starts with an ASCII letter or `_` and continues with ASCII letters,
// digits and `_`, the pattern `[A-Za-z_][A-Za-z0-9_]*`. A line may be
// indented with spaces and tabs before its key, and spaces and tabs may
// surround the `=`.
//
// The key must be followed by `=`. A key with no `=`, a `:` in place of `=`,
// or any other character in the key (`-`, `.`, non-ASCII letters) is an
// error. There is no `export` prefix: `export A=1` is an error.
//
// Each key may be defined once, and two keys may not differ only in ASCII
// case: both `A=1` followed by `A=2` and `Foo=1` followed by `FOO=2` are
// errors at the second definition. The config module looks keys up without
// regard to case, so either pair would leave it to guess which value is
// meant. A key otherwise keeps its case: the result holds it as written,
// and a reference must match it exactly, so `${db_host}` does not refer to
// `DB_HOST`.
//
// # Values
//
// Spaces and tabs after the `=` are skipped. What follows decides the kind
// of value:
//
//   - nothing, a line ending, or a comment: the value is the empty string,
//     as in `A=` or `A= # unset`;
//   - `"`: a double-quoted value;
//   - `'`: a single-quoted value;
//   - anything else: an unquoted value.
//
// After the value, the line may hold only spaces, tabs and a comment. Any
// other text is an error, so `A=x y` and `A="x" y` both fail.
//
// There are no escape sequences in any kind of value. A backslash is an
// ordinary character: `A="a\nb"` is the four characters `a\nb`, and a
// double-quoted value cannot contain `"`.
//
// # Unquoted values
//
// An unquoted value runs up to the first space, tab, `#`, line ending or the
// end of the file. It cannot contain spaces or tabs and cannot span lines;
// quote the value for either. Quote characters after the first one are
// ordinary text, so `A=it's` is `it's`. `${NAME}` references are expanded.
//
// # Double-quoted values
//
// A double-quoted value is everything between the opening `"` and the next
// `"`, kept exactly: leading and trailing spaces, `#`, `'`, and line endings,
// so a value can span lines. `${NAME}` references are expanded. A missing
// closing quote is an error.
//
//	GREETING="Hello,
//	world"
//
// # Single-quoted values
//
// A single-quoted value is everything between the opening `'` and the next
// `'`, kept exactly, like a double-quoted value. Nothing in it is expanded:
// `'${HOME}'` is the seven characters `${HOME}`, and needs no `HOME` key. A
// missing closing quote is an error.
//
// # Variable references
//
// In unquoted and double-quoted values, `${NAME}` is replaced by the final
// value of the key NAME. NAME is one or more of `[A-Za-z0-9_]`; any other
// character between the braces, or an empty name, is an error. There are no
// default or alternative forms such as `${NAME:-x}`.
//
// A `$` that is not followed by `{` is literal, so `$NAME`, a `$` at the end
// of a value, and `$$` stay as written.
//
// References are resolved within the file only, never from the process
// environment. They may point forward to a key defined later in the file,
// and may chain through other references:
//
//	URL=${SCHEME}://${HOST}
//	SCHEME=https
//	HOST=${DOMAIN}
//	DOMAIN=example.com    # URL is "https://example.com"
//
// Resolution follows these rules:
//
//   - A reference to a key that is not defined in the file is an error.
//   - A cycle, including a key that refers to itself, is an error.
//   - Substitution is a single pass: text inserted by a reference is not
//     scanned again. With `D=$` and `C=x`, the value `${D}{C}` is the
//     literal `${C}`, and a single-quoted `'${X}'` inserted elsewhere stays
//     `${X}`.
//   - The bytes inserted by all references in the file together may not
//     exceed 1 MiB (1,048,576 bytes); past that, parsing fails. This keeps a
//     small file from expanding into a huge one through repeated or nested
//     references.
//
// # Errors
//
// Errors wrap one of the package's sentinel errors, to be tested with
// [errors.Is]:
//
//   - [ErrInvalidUTF8Char]: the file holds bytes that are not valid UTF-8.
//   - [ErrUnexpectedChar]: a character that the format does not allow at
//     that point, such as a key starting with a digit or text after a value.
//   - [ErrUnexpectedEOF]: the file ends inside a definition, such as a key
//     without `=`, an unclosed quote, or an unclosed `${`.
//   - [ErrDuplicateKey]: a key is defined twice, or two keys differ only in
//     ASCII case.
//   - [ErrVarName]: a `${...}` reference has an empty name or a character
//     outside `[A-Za-z0-9_]`.
//   - [ErrVarUndef]: a reference names a key that the file does not define.
//   - [ErrVarCycle]: references form a cycle.
//   - [ErrVarExpansion]: references insert more than 1 MiB in total.
//
// Syntax and duplicate key errors start with the 1-based line and byte
// column where parsing stopped, which for a duplicate is the start of its
// second definition. Errors inside a value name its key:
//
//	2:5: unexpected character, want newline: key: "B"
//	1:5: unexpected EOF: key: "A"
//	3:1: duplicate key: "Foo" and "FOO"
//
// Reference errors are found after the whole file is read and carry no
// position. An undefined reference names the missing name, not the key
// whose value holds it; a cycle names a key in the cycle; an oversized
// expansion names the key whose value grew too large:
//
//	undefined variable: "DB_HOST"
//	circular variable reference: "A"
//
// An error never includes a value or a character from the file, so a
// malformed secret does not leak into logs, and control characters from the
// file never reach a terminal raw. A syntax error gives the position of
// the character instead of the character.
package dotenv
