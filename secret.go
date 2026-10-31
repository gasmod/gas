package gas

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
)

// redacted is what a Secret prints, marshals and logs as.
const redacted = "[redacted]"

// errSecretGob is what Secret's GobEncode returns.
var errSecretGob = errors.New("gas: a Secret is never gob-encoded")

// Secret is a string that is never printed, marshaled or logged, such as
// a password, a key or a DSN in a module's settings: String returns
// `[redacted]`, and Format writes it for every verb, so `%v`, `%+v` and
// `%#v` of a settings struct hide it; MarshalText returns it, so
// encoding/json and YAML encoders that use encoding.TextMarshaler write
// it; and LogValue returns it, so log/slog logs it. GobEncode returns an
// error, so encoding/gob fails rather than encode the value.
// Configuration binds into it as into a string, and string(s) is the
// value.
//
// fmt calls no method on a value it reaches through an unexported struct
// field, so a Secret in a settings struct held in an unexported field
// prints as the raw string. Never print a struct that holds settings.
type Secret string

var (
	_ fmt.Formatter  = Secret("")
	_ fmt.Stringer   = Secret("")
	_ slog.LogValuer = Secret("")
)

// String returns `[redacted]`, never the value.
func (Secret) String() string { return redacted }

// Format writes `[redacted]`, never the value, whatever the verb.
func (Secret) Format(f fmt.State, _ rune) {
	// fmt's State writes to fmt's own buffer, which does not fail.
	_, _ = io.WriteString(f, redacted)
}

// MarshalText returns `[redacted]`, never the value.
func (Secret) MarshalText() ([]byte, error) { return []byte(redacted), nil }

// LogValue returns `[redacted]`, never the value.
func (Secret) LogValue() slog.Value { return slog.StringValue(redacted) }

// GobEncode returns an error, never the value, so encoding/gob fails
// rather than encode it.
func (Secret) GobEncode() ([]byte, error) { return nil, errSecretGob }
