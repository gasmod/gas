package gas

import (
	"errors"
	"reflect"
	"strings"

	"github.com/go-playground/validator/v10"
)

// newValidator builds the validator used for request binding. Field names in
// validation errors follow the json tag (falling back to the form tag) that
// the client actually sent, rather than the Go struct field name, so a client
// can map a FieldError back onto the payload it submitted.
func newValidator() *validator.Validate {
	v := validator.New()
	v.RegisterTagNameFunc(func(field reflect.StructField) string {
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "" {
			name, _, _ = strings.Cut(field.Tag.Get("form"), ",")
		}

		// Returning "" makes validator fall back to the Go field name, which is
		// what we want for a field carrying neither tag, or tagged "-".
		if name == "-" {
			return ""
		}

		return name
	})

	return v
}

// validationFieldErrors converts a validator.ValidationErrors into the unified
// FieldError slice, each pointing at its field by its full path. It
// reports false when err is not a validation error.
func validationFieldErrors(err error) ([]FieldError, bool) {
	var ve validator.ValidationErrors

	if !errors.As(err, &ve) {
		return nil, false
	}

	fields := make([]FieldError, 0, len(ve))
	for _, fe := range ve {
		fields = append(fields, FieldError{
			Message: validationMessage(fe.Tag(), fe.Param()),
			Pointer: fieldPointer(fe.Namespace()),
			Rule:    fe.Tag(),
		})
	}

	return fields, true
}

// fieldPointer returns the JSON pointer of the field at namespace, a
// validator namespace of client names such as `Req.items[0].name`: below
// the root struct, each name and each index or map key is a reference
// token.
func fieldPointer(namespace string) string {
	_, path, ok := strings.Cut(namespace, ".")
	if !ok {
		path = namespace
	}

	var b strings.Builder

	b.WriteString("#")

	for part := range strings.SplitSeq(path, ".") {
		name, keys, _ := strings.Cut(part, "[")

		b.WriteString("/" + escapePointer(name))

		for keys != "" {
			key, rest, _ := strings.Cut(keys, "]")

			b.WriteString("/" + escapePointer(key))

			keys = strings.TrimPrefix(rest, "[")
		}
	}

	return b.String()
}

// validationMessage renders a human-readable message for a failed validation
// tag. The table is intentionally internal; an application wanting different
// wording builds its own FieldError values.
func validationMessage(tag, param string) string {
	switch tag {
	case "required":
		return "is required"
	case "email":
		return "must be a valid email address"
	case "min":
		return "must be at least " + param
	case "max":
		return "must be at most " + param
	case "len":
		return "must be exactly " + param + " characters"
	case "oneof":
		return "must be one of: " + param
	case "url":
		return "must be a valid URL"
	case "uuid":
		return "must be a valid UUID"
	}

	msg := "failed the " + tag + " rule"
	if param != "" {
		msg += " (" + param + ")"
	}

	return msg
}
