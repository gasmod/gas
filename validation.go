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
	v := validator.New(validator.WithTagNameFuncBlankOmit())
	v.RegisterTagNameFunc(func(field reflect.StructField) string {
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "" {
			name, _, _ = strings.Cut(field.Tag.Get("form"), ",")
		}

		// An untagged embedded struct is flattened by encoding/json, so it has
		// no segment of its own.
		if name == "" && field.Anonymous && embedsStruct(field.Type) {
			return ""
		}

		// Blank now means "omit", so the Go-name fallback has to be explicit.
		if name == "" || name == "-" {
			return field.Name
		}

		return name
	})

	return v
}

// embedsStruct reports whether t, the type of an embedded field, is a
// struct or a pointer to one: the embeddings encoding/json flattens into
// the outer object.
func embedsStruct(t reflect.Type) bool {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	return t.Kind() == reflect.Struct
}

// validationFieldErrors converts a validator.ValidationErrors from
// validating dest into the unified FieldError slice, each pointing at its
// field by its full path. It reports false when err is not a validation
// error.
func validationFieldErrors(err error, dest any) ([]FieldError, bool) {
	var ve validator.ValidationErrors

	if !errors.As(err, &ve) {
		return nil, false
	}

	root := rootName(dest)

	fields := make([]FieldError, 0, len(ve))
	for _, fe := range ve {
		fields = append(fields, FieldError{
			Message: validationMessage(fe.Tag(), fe.Param()),
			Pointer: fieldPointer(fe.Namespace(), root),
			Rule:    fe.Tag(),
		})
	}

	return fields, true
}

// rootName returns the name the validator starts dest's namespaces with:
// the name of its struct type, through one pointer as StructCtx
// dereferences it. An unnamed struct type has none, and its namespaces
// start at its fields.
func rootName(dest any) string {
	t := reflect.TypeOf(dest)
	if t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	if t == nil {
		return ""
	}

	return t.Name()
}

// fieldPointer returns the JSON pointer of the field at namespace, a
// validator namespace of client names such as `Req.items[0].name`, under
// root, the root struct's type name: below it, each name and each index or
// map key is a reference token. A namespace of the root alone, as an
// untagged embedded struct at the root has, is the whole body, `#`.
func fieldPointer(namespace, root string) string {
	path := strings.TrimPrefix(strings.TrimPrefix(namespace, root), ".")
	if path == "" {
		return "#"
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
