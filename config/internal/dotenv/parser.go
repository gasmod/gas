package dotenv

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

var (
	// ErrInvalidUTF8Char indicates bytes that are not valid UTF-8, anywhere
	// in the input, comments included.
	ErrInvalidUTF8Char = errors.New("invalid UTF-8 character")
	// ErrUnexpectedChar indicates a character the format does not allow at
	// that point, such as a key starting with a digit, a key without `=`
	// before the line ends, or text after a value.
	ErrUnexpectedChar = errors.New("unexpected character")
	// ErrUnexpectedEOF indicates input that ends inside a definition: a key
	// without `=`, an unclosed quote, or an unclosed `${`.
	ErrUnexpectedEOF = errors.New("unexpected EOF")
	// ErrVarName indicates a `${...}` reference with an empty name or a
	// character outside `[A-Za-z0-9_]`.
	ErrVarName = errors.New("invalid character in variable name")
	// ErrDuplicateKey indicates a key defined twice, or two keys that differ
	// only in ASCII case, such as `Foo` and `FOO`.
	ErrDuplicateKey = errors.New("duplicate key")
	// ErrVarUndef indicates a `${NAME}` reference to a key the input does
	// not define. The process environment is never consulted.
	ErrVarUndef = errors.New("undefined variable")
	// ErrVarCycle indicates references that form a cycle, including a key
	// that refers to itself.
	ErrVarCycle = errors.New("circular variable reference")
	// ErrVarExpansion indicates references that together insert more than
	// 1 MiB (1,048,576 bytes) into the input's values.
	ErrVarExpansion = errors.New("variable expansion too large")
)

const (
	badRune = 1

	// maxExpansion caps the total bytes that interpolation may substitute
	// across all values, so a small input cannot expand without bound.
	maxExpansion = 1 << 20
)

// entry is one KEY=VALUE definition. key and value are substrings of the
// input, which is converted to a string once per Parse. refs reports whether
// value holds ${NAME} references; values are kept as written, so the
// references are found again by scanning value.
type entry struct {
	key   string
	value string
	refs  bool
}

// Parser parses `.env` input in the format described in the package
// documentation. The zero value is ready to use, and a Parser can be reused
// for any number of inputs; it keeps its buffers between calls. A Parser
// must not be used by more than one goroutine at a time.
type Parser struct {
	// seen maps each key, lowercased, to its index in entries.
	seen    map[string]int
	src     string
	entries []entry
	idx     int
	refs    bool
}

// Parse parses data and returns each key, as written, with its final value:
// the value with its `${NAME}` references expanded. Nothing
// from earlier calls carries over, and the result does not share memory
// with data. Input with no definitions yields an empty, non-nil map.
//
// On failure Parse returns a nil map and an error wrapping one of the
// package's sentinel errors. Syntax errors start with the 1-based line and
// byte column where parsing stopped, and name the key when the error is
// inside a value; duplicate key errors name both keys, and reference errors
// name the missing name or the key involved. No error includes a value from
// data.
//
//nolint:cyclop // can't be simplified further
func (p *Parser) Parse(data []byte) (map[string]string, error) {
	p.reset(data)

	for p.idx < len(p.src) {
		c := p.src[p.idx]
		switch {
		case c == '#':
			p.idx++

			fallthrough
		case p.atEOL():
			err := p.advanceLine()
			if err != nil {
				return nil, p.err(err)
			}
		case c == ' ' || c == '\t':
			p.idx++
			p.advanceEmpty()
		case isAlpha(c) || c == '_':
			err := p.parseLine()
			if err != nil {
				return nil, p.err(err)
			}
		default:
			return nil, p.err(p.charErr(ErrUnexpectedChar, ""))
		}
	}

	if !p.refs {
		return p.entriesMap(), nil
	}

	return p.resolveVariables()
}

// entriesMap returns entries as a map of name->value.
func (p *Parser) entriesMap() map[string]string {
	values := make(map[string]string, len(p.entries))
	for i := range p.entries {
		values[p.entries[i].key] = p.entries[i].value
	}

	return values
}

// advanceLine moves past the next "\n", or to EOF if there is none.
func (p *Parser) advanceLine() error {
	end := len(p.src)
	if i := strings.IndexByte(p.src[p.idx:], '\n'); i >= 0 {
		end = p.idx + i
	}

	err := p.skipText(end)
	if err != nil {
		return err
	}

	if p.idx < len(p.src) {
		p.idx++
	}

	return nil
}

// skipText moves p.idx to end, stopping with ErrInvalidUTF8Char at the
// first byte before end that is not valid UTF-8. end must be len(p.src) or
// the index of an ASCII byte, so no rune spans it.
func (p *Parser) skipText(end int) error {
	if utf8.ValidString(p.src[p.idx:end]) {
		p.idx = end

		return nil
	}

	for p.idx < end {
		r, rs := utf8.DecodeRuneInString(p.src[p.idx:])
		if r == utf8.RuneError && rs == badRune {
			return ErrInvalidUTF8Char
		}

		p.idx += rs
	}

	return nil
}

func (p *Parser) advanceEmpty() {
	for p.idx < len(p.src) && (p.src[p.idx] == ' ' || p.src[p.idx] == '\t') {
		p.idx++
	}
}

func (p *Parser) parseLine() error {
	kerr := p.parseKey()
	if kerr != nil {
		return kerr
	}

	verr := p.parseValue()
	if verr != nil {
		key := p.entries[len(p.entries)-1].key

		return fmt.Errorf("%w: key: %q", verr, key)
	}

	return nil
}

func (p *Parser) parseKey() error {
	start := p.idx
	for p.idx < len(p.src) && isIdent(p.src[p.idx]) {
		p.idx++
	}

	key := p.src[start:p.idx]

	if p.idx >= len(p.src) {
		return ErrUnexpectedEOF
	}

	if c := p.src[p.idx]; c == ' ' || c == '\t' {
		p.advanceEmpty()

		if p.idx >= len(p.src) {
			return ErrUnexpectedEOF
		}

		if p.src[p.idx] != '=' {
			return p.charErr(ErrUnexpectedChar, ", want '='")
		}
	} else if c != '=' {
		return p.charErr(ErrUnexpectedChar, "")
	}

	lower := strings.ToLower(key)
	if first, ok := p.seen[lower]; ok {
		p.idx = start

		return fmt.Errorf("%w: %q and %q", ErrDuplicateKey, p.entries[first].key, key)
	}

	p.idx++

	p.seen[lower] = len(p.entries)
	p.entries = append(p.entries, entry{key: key})

	return nil
}

func (p *Parser) parseValue() error {
	p.advanceEmpty()

	if p.idx >= len(p.src) {
		return nil
	}

	switch p.src[p.idx] {
	case '\n':
		return p.advanceLine()
	case '"', '\'':
		return p.parseQuotedValue()
	default:
		return p.parseUnquotedValue()
	}
}

func (p *Parser) parseQuotedValue() error {
	quote := p.src[p.idx]
	stops := "'"

	if quote == '"' {
		stops = `"$`
	}

	p.idx++

	start := p.idx

	for {
		i := strings.IndexAny(p.src[p.idx:], stops)
		if i < 0 {
			err := p.skipText(len(p.src))
			if err != nil {
				return err
			}

			return ErrUnexpectedEOF
		}

		err := p.skipText(p.idx + i)
		if err != nil {
			return err
		}

		if p.src[p.idx] == quote {
			p.entries[len(p.entries)-1].value = p.src[start:p.idx]
			p.idx++

			return p.endLine()
		}

		verr := p.parseVariable()
		if verr != nil {
			return verr
		}
	}
}

func (p *Parser) parseUnquotedValue() error {
	start := p.idx

	for p.idx < len(p.src) {
		c := p.src[p.idx]
		switch {
		case c == '$':
			err := p.parseVariable()
			if err != nil {
				return err
			}
		case p.atUnquotedValueEOL():
			p.entries[len(p.entries)-1].value = p.src[start:p.idx]

			return p.endLine()
		case c < utf8.RuneSelf:
			p.idx++
		default:
			r, rs := utf8.DecodeRuneInString(p.src[p.idx:])
			if r == utf8.RuneError && rs == badRune {
				return ErrInvalidUTF8Char
			}

			p.idx += rs
		}
	}

	p.entries[len(p.entries)-1].value = p.src[start:]

	return nil
}

// parseVariable moves past the "$" at p.idx and, if a "{" follows, the rest
// of a ${NAME} reference. A "$" without "{" is literal.
func (p *Parser) parseVariable() error {
	p.idx++
	if p.idx >= len(p.src) || p.src[p.idx] != '{' {
		return nil
	}

	p.idx++

	start := p.idx
	for p.idx < len(p.src) && isIdent(p.src[p.idx]) {
		p.idx++
	}

	if p.idx >= len(p.src) {
		return ErrUnexpectedEOF
	}

	if p.src[p.idx] != '}' {
		return p.charErr(ErrVarName, "")
	}

	p.idx++

	if p.idx-1 == start {
		return fmt.Errorf("%w: empty variable name", ErrVarName)
	}

	p.entries[len(p.entries)-1].refs = true
	p.refs = true

	return nil
}

func (p *Parser) endLine() error {
	p.advanceEmpty()

	if p.idx >= len(p.src) {
		return nil
	}

	switch c := p.src[p.idx]; {
	case c == '#':
		p.idx++

		fallthrough
	case c == '\n' || (c == '\r' && p.atCRLF()):
		return p.advanceLine()
	default:
		return p.charErr(ErrUnexpectedChar, ", want newline")
	}
}

const (
	_ = iota
	visiting
	done
)

func (p *Parser) resolveVariables() (map[string]string, error) {
	// index maps each key to its definition.
	index := make(map[string]int, len(p.entries))
	for i := range p.entries {
		index[p.entries[i].key] = i
	}

	state := make([]uint8, len(p.entries))
	budget := maxExpansion

	for i := range p.entries {
		err := p.resolve(i, index, state, &budget)
		if err != nil {
			return nil, err
		}
	}

	values := make(map[string]string, len(index))
	for k, i := range index {
		values[k] = p.entries[i].value
	}

	return values, nil
}

// resolveFrame is an entry being resolved and the part of its value not yet
// scanned for references.
type resolveFrame struct {
	rest string
	i    int
}

// resolve expands the references in entry i, resolving the entries they name
// first, depth first. It keeps its own stack rather than recursing, so a long
// chain of references cannot overflow the goroutine stack.
func (p *Parser) resolve(i int, index map[string]int, state []uint8, budget *int) error {
	if state[i] == done {
		return nil
	}

	if !p.entries[i].refs {
		state[i] = done

		return nil
	}

	state[i] = visiting

	stack := []resolveFrame{{rest: p.entries[i].value, i: i}}

	for len(stack) > 0 {
		top := &stack[len(stack)-1]
		if name, rest, ok := nextRef(top.rest); ok {
			top.rest = rest

			j, push, err := p.enter(name, index, state)
			if err != nil {
				return err
			}

			if push {
				stack = append(stack, resolveFrame{rest: p.entries[j].value, i: j})
			}

			continue
		}

		e := &p.entries[top.i]

		v, err := substitute(e.value, func(name string) string {
			return p.entries[index[name]].value
		}, budget)
		if err != nil {
			return fmt.Errorf("%w: %q", err, e.key)
		}

		e.value = v
		state[top.i] = done
		stack = stack[:len(stack)-1]
	}

	return nil
}

// enter looks up the entry a reference to name points at. It reports true
// when that entry still has references to expand, after marking it visiting;
// an entry without references is marked done instead.
func (p *Parser) enter(name string, index map[string]int, state []uint8) (int, bool, error) {
	j, ok := index[name]
	if !ok {
		return 0, false, fmt.Errorf("%w: %q", ErrVarUndef, name)
	}

	switch {
	case state[j] == done:
		return j, false, nil
	case state[j] == visiting:
		return 0, false, fmt.Errorf("%w: %q", ErrVarCycle, name)
	case !p.entries[j].refs:
		state[j] = done

		return j, false, nil
	}

	state[j] = visiting

	return j, true, nil
}

// nextRef finds the first ${NAME} in s and returns NAME and the text after
// the reference. Like substitute, it relies on every "${" in a value with
// references starting a complete reference.
func nextRef(s string) (name, rest string, ok bool) {
	i := strings.Index(s, "${")
	if i < 0 {
		return "", "", false
	}

	j := i + strings.IndexByte(s[i:], '}')

	return s[i+2 : j], s[j+1:], true
}

// substitute replaces each ${NAME} in s with lookup(NAME) in a single pass,
// so substituted text is never scanned again. It is only called for values
// with references, for which parseVariable guarantees that every "${" starts
// a complete reference; single-quoted values have none. Each substitution is
// charged to budget, and substitute fails once budget runs out.
func substitute(s string, lookup func(string) string, budget *int) (string, error) {
	var b strings.Builder

	for {
		i := strings.Index(s, "${")
		if i < 0 {
			b.WriteString(s)

			return b.String(), nil
		}

		j := i + strings.IndexByte(s[i:], '}')
		v := lookup(s[i+2 : j])

		*budget -= len(v)

		if *budget < 0 {
			return "", ErrVarExpansion
		}

		b.WriteString(s[:i])
		b.WriteString(v)

		s = s[j+1:]
	}
}

// atCRLF reports whether p.src[p.idx:] starts with "\r\n".
func (p *Parser) atCRLF() bool {
	return p.idx+1 < len(p.src) && p.src[p.idx] == '\r' && p.src[p.idx+1] == '\n'
}

// atEOL reports whether p.src[p.idx] is LF or CRLF line endings.
func (p *Parser) atEOL() bool {
	return p.src[p.idx] == '\n' || (p.src[p.idx] == '\r' && p.atCRLF())
}

// atUnquotedValueEOL reports whether p.src[p.idx] is a terminator of an
// unquoted value line.
func (p *Parser) atUnquotedValueEOL() bool {
	c := p.src[p.idx]

	return c == ' ' || c == '\t' || c == '#' || p.atEOL()
}

// charErr returns ErrInvalidUTF8Char if the character at p.idx is not valid
// UTF-8, and otherwise sentinel followed by suffix. It does not name the
// character: one after a value is often part of what was meant as the
// value, such as the `h` of `PASSWORD=correct horse`, and the position
// already locates it.
func (p *Parser) charErr(sentinel error, suffix string) error {
	r, rs := utf8.DecodeRuneInString(p.src[p.idx:])
	if r == utf8.RuneError && rs == badRune {
		return ErrInvalidUTF8Char
	}

	return fmt.Errorf("%w%s", sentinel, suffix)
}

// pos returns the 1-based line and byte column of p.idx. Every "\n" before
// p.idx is a line ending, since none can appear in a key or reference.
func (p *Parser) pos() (ln, col int) {
	before := p.src[:p.idx]

	return strings.Count(before, "\n") + 1, p.idx - strings.LastIndexByte(before, '\n')
}

func (p *Parser) err(err error) error {
	ln, col := p.pos()

	return fmt.Errorf("%d:%d: %w", ln, col, err)
}

func (p *Parser) reset(data []byte) {
	p.src = string(data)
	p.idx = 0
	p.refs = false
	clear(p.entries)

	p.entries = p.entries[:0]
	if p.seen == nil {
		p.seen = make(map[string]int)
	}

	clear(p.seen)
}

func isAlpha(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isAlphaNum(c byte) bool {
	return isAlpha(c) || (c >= '0' && c <= '9')
}

func isIdent(c byte) bool {
	return isAlphaNum(c) || c == '_'
}
