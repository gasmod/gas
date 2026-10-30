package log

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
)

// flatten appends a to dst with its value resolved: a group's attributes
// are appended in its place, under its key joined to prefix by `.`, an
// empty group and an empty attribute are dropped, and a slog.KindAny value
// is replaced by encode's. A group with an empty key is inlined.
func flatten(dst []slog.Attr, prefix string, a slog.Attr, encode func(any) slog.Value) []slog.Attr {
	v := a.Value.Resolve()

	//nolint:exhaustive // only groups and KindAny values are changed.
	switch v.Kind() {
	case slog.KindGroup:
		group := v.Group()
		if len(group) == 0 {
			return dst
		}

		inner := prefix
		if a.Key != "" {
			inner = join(prefix, a.Key)
		}

		for _, g := range group {
			dst = flatten(dst, inner, g, encode)
		}

		return dst
	case slog.KindAny:
		if a.Key == "" && v.Any() == nil {
			return dst
		}

		v = encode(v.Any())
	default:
	}

	return append(dst, slog.Attr{Key: join(prefix, a.Key), Value: v})
}

// join returns key under the group path prefix.
func join(prefix, key string) string {
	if prefix == "" {
		return key
	}

	return prefix + "." + key
}

// unencodable starts the text that stands for a value encoding/json
// cannot encode; the name of the value's type follows.
const unencodable = "!unencodable "

// encodeAny returns the value a Record holds for v, as described in the
// package documentation under Shipping: nil stays nil, a []byte is copied,
// an error becomes its Error text, a fmt.Stringer its String, a value
// encoding/json can encode its JSON text, and anything else unencodable
// followed by the name of its type. It does not recover a panic of the
// methods it calls.
func encodeAny(v any) slog.Value {
	switch x := v.(type) {
	case nil:
		return slog.AnyValue(nil)
	case []byte:
		return slog.AnyValue(bytes.Clone(x))
	case error:
		return slog.StringValue(x.Error())
	case fmt.Stringer:
		return slog.StringValue(x.String())
	}

	text, err := json.Marshal(v)
	if err == nil {
		return slog.StringValue(string(text))
	}

	// A value encoding/json cannot encode, such as a channel or a cycle,
	// is named by its type only: formatting it, as fmt does, recurses
	// without end on a cycle, which no recover stops.
	return slog.StringValue(unencodable + reflect.TypeOf(v).String())
}

// snapshotAny is encodeAny with slog's handling of a panic: a nil pointer
// whose method panicked becomes `<nil>`, and any other panic `!PANIC: `
// followed by the panic value, as slog's built-in handlers write them.
func snapshotAny(v any) (out slog.Value) {
	defer func() {
		r := recover()
		if r == nil {
			return
		}

		if rv := reflect.ValueOf(v); rv.Kind() == reflect.Pointer && rv.IsNil() {
			out = slog.StringValue("<nil>")

			return
		}

		out = slog.StringValue(fmt.Sprintf("!PANIC: %v", r))
	}()

	return encodeAny(v)
}
