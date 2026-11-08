// Package jsonobj decodes a JSON object for the config module's JSON
// provider and its Secrets Manager provider, with errors that never quote
// the input.
//
// # Usage
//
// [Decode] takes the bytes of a JSON document and returns the object it
// holds:
//
//	values, err := jsonobj.Decode([]byte(`{"db": {"port": 5432}}`))
//	// values is {"db": {"port": int64(5432)}}
//
// The input is decoded with [encoding/json] into a map[string]any: an
// object is a map[string]any, a list a []any, a string a string, a boolean
// a bool, and null is nil. A number keeps its digits: written as an integer
// that fits an int64, such as `5432` or `-0`, it is an int64; an integer
// past that which fits a uint64 is a uint64; any other number, such as
// `1.5`, `1.0`, `1e3` or an integer past 2⁶⁴, is a float64, which keeps
// about 16 digits. Keys are kept as they are. The result does not share
// memory with the input.
//
// # Errors
//
// A document that is not valid JSON, or that holds anything but an object,
// is an error, and the result is nil. The error says what is wrong without
// quoting the input, which encoding/json's syntax errors do: they name the
// character where decoding stopped, and the input may be a secret.
//
//	value is not valid JSON: syntax error after byte 14
//	value is a JSON array, not an object
//	value is a JSON null, not an object
//	value holds a number out of range for float64
//
// The offset is that of [json.SyntaxError]: the number of bytes read
// before the error. Empty input, or input holding only white space, is a
// syntax error. The kinds are null, boolean, number, string and array. A
// number too large for a float64, such as `1e400`, is an error too, which
// wraps [ErrNumberRange], so a caller can tell it from a value that is not
// an object.
package jsonobj
