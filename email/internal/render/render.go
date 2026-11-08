package render

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	htmltemplate "html/template"
	"maps"
	"slices"
	"strings"
	texttemplate "text/template"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/email"
)

// The fields a template renders, as errors name them.
const (
	fieldSubject = "subject"
	fieldText    = "text"
	fieldHTML    = "HTML"
)

// The stages a template fails at.
const (
	stageFetch   = "fetch failed"
	stageParse   = "parse failed"
	stageExecute = "execute failed"
)

// errRenderTooLarge is what a capped writer fails with once a template
// writes more than email.MaxMessageSize bytes.
var errRenderTooLarge = errors.New("rendered too much")

// cappedBuffer is a buffer that fails with errRenderTooLarge, keeping
// nothing more, once more than email.MaxMessageSize bytes are written to
// it, so that a template cannot render without bound.
type cappedBuffer struct {
	// b holds what was written.
	b bytes.Buffer
}

// Write appends p, or fails with errRenderTooLarge when it would take
// the buffer past email.MaxMessageSize bytes.
func (c *cappedBuffer) Write(p []byte) (int, error) {
	if c.b.Len()+len(p) > email.MaxMessageSize {
		return 0, errRenderTooLarge
	}

	return c.b.Write(p) //nolint:wrapcheck // bytes.Buffer's Write fails only by panicking.
}

// templateError is a failed template. Its text names the field, the
// template and the stage, and it wraps email.ErrTemplate and, for a
// failed fetch, the provider's error and ctx's error, whose texts it
// does not show.
type templateError struct {
	// msg is the error text.
	msg string
	// errs are the wrapped errors.
	errs []error
}

// Error returns the fixed text.
func (e *templateError) Error() string { return e.msg }

// Unwrap returns the wrapped errors.
func (e *templateError) Unwrap() []error { return e.errs }

// Render renders msg into a new gas.Email, as described in the package
// documentation.
func Render(
	ctx context.Context,
	templates gas.TemplateProvider,
	msg *gas.TemplatedEmail,
) (*gas.Email, error) {
	out := Copy(&msg.Email)

	var err error

	if msg.SubjectTemplate != "" {
		out.Subject, err = renderText(ctx, templates, fieldSubject, msg.SubjectTemplate, msg.Data)
		if err != nil {
			return nil, err
		}

		out.Subject = trimNewline(out.Subject)
	}

	if msg.TextTemplate != "" {
		out.TextBody, err = renderText(ctx, templates, fieldText, msg.TextTemplate, msg.Data)
		if err != nil {
			return nil, err
		}
	}

	if msg.HTMLTemplate != "" {
		out.HTMLBody, err = renderHTML(ctx, templates, msg.HTMLTemplate, msg.Data)
		if err != nil {
			return nil, err
		}
	}

	return out, nil
}

// trimNewline returns s without one trailing CRLF or LF, which a
// subject template file usually ends with.
func trimNewline(s string) string {
	if t, ok := strings.CutSuffix(s, "\r\n"); ok {
		return t
	}

	return strings.TrimSuffix(s, "\n")
}

// Copy returns a copy of msg that shares no slice or map with it.
func Copy(msg *gas.Email) *gas.Email {
	out := *msg

	out.To = slices.Clone(msg.To)
	out.Cc = slices.Clone(msg.Cc)
	out.Bcc = slices.Clone(msg.Bcc)
	out.Headers = maps.Clone(msg.Headers)

	return &out
}

// renderText renders the template name of field with text/template.
func renderText(
	ctx context.Context,
	templates gas.TemplateProvider,
	field, name string,
	data any,
) (string, error) {
	src, err := fetch(ctx, templates, field, name)
	if err != nil {
		return "", err
	}

	t, err := texttemplate.New(name).Parse(src)
	if err != nil {
		return "", failed(field, name, stageParse)
	}

	var b cappedBuffer

	err = t.Execute(&b, data)
	if err != nil {
		return "", executeError(field, name, err)
	}

	return b.b.String(), nil
}

// renderHTML renders the HTML template name with html/template.
func renderHTML(
	ctx context.Context,
	templates gas.TemplateProvider,
	name string,
	data any,
) (string, error) {
	src, err := fetch(ctx, templates, fieldHTML, name)
	if err != nil {
		return "", err
	}

	t, err := htmltemplate.New(name).Parse(src)
	if err != nil {
		return "", failed(fieldHTML, name, stageParse)
	}

	var b cappedBuffer

	err = t.Execute(&b, data)
	if err != nil {
		return "", executeError(fieldHTML, name, err)
	}

	return b.b.String(), nil
}

// executeError returns the failure of the template name of field to
// execute with err: email.ErrMessageTooLarge when it rendered more than
// email.MaxMessageSize bytes, and else a failure at stageExecute.
func executeError(field, name string, err error) error {
	if errors.Is(err, errRenderTooLarge) {
		return &templateError{
			msg: fmt.Sprintf("%s: %s %q: renders more than %d bytes",
				email.ErrMessageTooLarge, field, name, email.MaxMessageSize),
			errs: []error{email.ErrMessageTooLarge},
		}
	}

	return failed(field, name, stageExecute)
}

// fetch returns the text of the template name of field.
func fetch(
	ctx context.Context,
	templates gas.TemplateProvider,
	field, name string,
) (string, error) {
	src, err := templates.Get(ctx, name)
	if err != nil {
		e := failed(field, name, stageFetch)

		e.errs = append(e.errs, err)

		ctxErr := ctx.Err()
		if ctxErr != nil {
			e.errs = append(e.errs, ctxErr)
		}

		return "", e
	}

	return string(src), nil
}

// failed returns the failure of the template name of field at stage.
func failed(field, name, stage string) *templateError {
	return &templateError{
		msg:  fmt.Sprintf("%s: %s %q: %s", email.ErrTemplate, field, name, stage),
		errs: []error{email.ErrTemplate},
	}
}
