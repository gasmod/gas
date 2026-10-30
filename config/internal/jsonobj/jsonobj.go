package jsonobj

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
)

var (
	errInvalidJSON = errors.New("value is not valid JSON")
	errNotObject   = errors.New("not an object")

	// ErrNumberRange indicates a JSON object that is valid but holds a
	// number too large for a float64, such as 1e400.
	ErrNumberRange = errors.New("number out of range for float64")
)

// Decode decodes data as a JSON object, as described in the package
// documentation.
func Decode(data []byte) (map[string]any, error) {
	// Unmarshal checks the whole input before it decodes anything, so a
	// syntax error comes back here with its offset, whatever the target.
	var raw json.RawMessage

	err := json.Unmarshal(data, &raw)
	if err != nil {
		if syntaxErr, ok := errors.AsType[*json.SyntaxError](err); ok {
			return nil, fmt.Errorf(
				"%w: syntax error after byte %d",
				errInvalidJSON,
				syntaxErr.Offset,
			)
		}

		return nil, fmt.Errorf("%w: %w", errInvalidJSON, err)
	}

	// The input is valid, so the decoder reads exactly one value. UseNumber
	// keeps each number's text, so an integer keeps all its digits.
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()

	var v any

	err = dec.Decode(&v)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errInvalidJSON, err)
	}

	obj, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("value is a JSON %s, %w", kind(v), errNotObject)
	}

	err = numbers(obj)
	if err != nil {
		return nil, err
	}

	return obj, nil
}

// numbers replaces each json.Number in the map or list v, at every depth,
// with the number it holds.
func numbers(v any) error {
	switch v := v.(type) {
	case map[string]any:
		for key, elem := range v {
			n, err := convert(elem)
			if err != nil {
				return err
			}

			v[key] = n
		}
	case []any:
		for i, elem := range v {
			n, err := convert(elem)
			if err != nil {
				return err
			}

			v[i] = n
		}
	}

	return nil
}

// convert returns the number a json.Number holds, or v itself, with the
// numbers inside it converted, when it is not a json.Number.
func convert(v any) (any, error) {
	n, ok := v.(json.Number)
	if !ok {
		return v, numbers(v)
	}

	return number(n)
}

// number returns n as an int64 when it is an integer that fits one, as a
// uint64 when it fits that instead, and as a float64 otherwise.
func number(n json.Number) (any, error) {
	s := string(n)

	i, err := strconv.ParseInt(s, 10, 64)
	if err == nil {
		return i, nil
	}

	u, err := strconv.ParseUint(s, 10, 64)
	if err == nil {
		return u, nil
	}

	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		// The text is valid JSON, so the only failure is a magnitude past
		// float64's, such as 1e400. The error does not quote it.
		return nil, fmt.Errorf("value holds a %w", ErrNumberRange)
	}

	return f, nil
}

// kind names the kind of a value that encoding/json decoded into an any
// and that is not an object.
func kind(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case json.Number:
		return "number"
	case string:
		return "string"
	default: // []any
		return "array"
	}
}
