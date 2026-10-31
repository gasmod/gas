// Package render renders a gas.TemplatedEmail into a gas.Email, for every
// sender of the module, as the email package describes under Templates.
//
// # Usage
//
//	msg, err := render.Render(ctx, templates, tmsg)
//
// msg is a copy of tmsg.Email with each field whose template tmsg names
// rendered: the subject and the text body with text/template, the HTML
// body with html/template, each template fetched by name from the
// gas.TemplateProvider and executed with tmsg.Data. A template name left
// empty keeps the field of tmsg.Email. One trailing CRLF or LF of a
// rendered subject is cut, as a template file usually ends with one. The
// copy shares no slice or map with tmsg, and tmsg is never changed.
//
// # Errors
//
// A template that cannot be fetched, parsed or executed fails the render
// with an error wrapping email.ErrTemplate that names the field and the
// template, never the data, the template text or the error of
// text/template or html/template, which can quote both. A failed fetch
// wraps the gas.TemplateProvider's error too, so that errors.Is reaches
// it; its text is not shown. When ctx has ended, the error wraps ctx's
// error too. The fields render in the order subject, text, HTML, and the
// first failure is returned. A template that renders more than
// email.MaxMessageSize bytes stops there and fails with an error wrapping
// email.ErrMessageTooLarge instead:
//
//	message too large: text "welcome.txt": renders more than 40000000 bytes
//	template failed: subject "welcome-subject": fetch failed
//	template failed: text "welcome.txt": parse failed
//	template failed: HTML "welcome.html": execute failed
package render
