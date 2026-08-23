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
		name := strings.SplitN(field.Tag.Get("json"), ",", 2)[0]
		if name == "" {
			name = strings.SplitN(field.Tag.Get("form"), ",", 2)[0]
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
// FieldError slice. It reports false when err is not a validation error.
func validationFieldErrors(err error) ([]FieldError, bool) {
	var ve validator.ValidationErrors
	if !errors.As(err, &ve) {
		return nil, false
	}

	fields := make([]FieldError, 0, len(ve))
	for _, fe := range ve {
		fields = append(fields, FieldError{
			Field:   fe.Field(),
			Rule:    fe.Tag(),
			Message: validationMessage(fe.Tag(), fe.Param()),
		})
	}
	return fields, true
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
