package ses

import (
	"context"
	"fmt"
	"maps"
	"mime"
	"slices"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/email"
	"github.com/gasmod/gas/email/internal/lifecycle"
	"github.com/gasmod/gas/email/internal/render"
)

// charset is the character set the sender sends every part with.
const charset = "UTF-8"

// errNilOutput reports a client that returned neither an output nor an
// error, which a send fails with as an invalid reply.
var errNilOutput = fmt.Errorf("%w: no output", errInvalidReply)

// Send checks msg as email.CheckEmail does and sends a normalized copy
// of it with SendEmail, as described in the package documentation. It
// never changes msg.
func (s *Service) Send(ctx context.Context, msg *gas.Email) error {
	if msg == nil {
		return opError(opSend, email.ErrNilMessage)
	}

	sess, err := s.lc.Begin(ctx)
	if err != nil {
		return opError(opSend, err)
	}
	defer sess.End()

	return s.send(ctx, sess, opSend, msg)
}

// SendFromTemplate renders msg into a copy, as the email package
// describes under Templates, and sends the copy as Send does. It never
// changes msg.
func (s *Service) SendFromTemplate(ctx context.Context, msg *gas.TemplatedEmail) error {
	if msg == nil {
		return opError(opSendTemplate, email.ErrNilMessage)
	}

	sess, err := s.lc.Begin(ctx)
	if err != nil {
		return opError(opSendTemplate, err)
	}
	defer sess.End()

	rctx, cancel := context.WithCancel(ctx)
	defer cancel()

	//nolint:contextcheck // the session's context is one Init's, which Close cancels.
	stop := context.AfterFunc(sess.Context(), cancel)
	defer stop()

	rendered, err := render.Render(rctx, s.templates, msg)

	switch {
	case err == nil:
	case ctx.Err() != nil:
		return opError(opSendTemplate, ctx.Err())
	case sess.Context().Err() != nil:
		return opError(opSendTemplate, email.ErrClosed)
	default:
		return opError(opSendTemplate, err)
	}

	return s.send(ctx, sess, opSendTemplate, rendered)
}

// send checks msg and sends it within ctx, the session's OpTimeout and
// its context, and returns its error as op's: the context's error when
// ctx ended, email.ErrClosed when a Close canceled the session,
// context.DeadlineExceeded for a timeout while ctx's deadline was the one
// in force, and else the error requestError makes.
func (s *Service) send(
	ctx context.Context,
	sess *lifecycle.Session[*state],
	op string,
	msg *gas.Email,
) error {
	st := sess.Value

	out, err := email.CheckEmail(msg, st.from)
	if err != nil {
		return opError(op, err)
	}

	// A Close whose context ended canceled the send; one that did not
	// wait on its context, such as a TemplateProvider that ignores it,
	// sends nothing.
	if sess.Context().Err() != nil {
		return opError(op, email.ErrClosed)
	}

	opDeadline := time.Now().Add(st.opTimeout)

	opCtx, cancel := context.WithDeadline(ctx, opDeadline)
	defer cancel()

	//nolint:contextcheck // the session's context is one Init's, which Close cancels.
	stop := context.AfterFunc(sess.Context(), cancel)
	defer stop()

	reply, err := st.client.SendEmail(opCtx, input(out))
	if err == nil && reply == nil {
		err = errNilOutput
	}

	if err == nil {
		return nil
	}

	return failure(ctx, sess, op, opDeadline, err)
}

// failure returns the error err of a send of op whose operation deadline
// was opDeadline, as send describes.
func failure(
	ctx context.Context,
	sess *lifecycle.Session[*state],
	op string,
	opDeadline time.Time,
	err error,
) error {
	ctxErr := ctx.Err()
	if ctxErr != nil {
		return opError(op, ctxErr)
	}

	if sess.Context().Err() != nil {
		return opError(op, email.ErrClosed)
	}

	callerDeadline, ok := ctx.Deadline()
	if ok && !callerDeadline.After(opDeadline) && timedOut(err) {
		return opError(op, context.DeadlineExceeded)
	}

	return requestError(op, err)
}

// input returns the SendEmail input of msg, a message email.CheckEmail
// returned: Simple content with a UTF-8 subject, as RFC 2047 encoded
// words when it is not ASCII, each body that is not
// empty, and the headers in the order of their names; no list that is
// empty, and no reply-to address when there is none.
func input(msg *gas.Email) *sesv2.SendEmailInput {
	body := &types.Body{}

	if msg.TextBody != "" {
		body.Text = content(msg.TextBody)
	}

	if msg.HTMLBody != "" {
		body.Html = content(msg.HTMLBody)
	}

	simple := &types.Message{
		Subject: content(mime.BEncoding.Encode(charset, msg.Subject)),
		Body:    body,
	}

	for _, name := range slices.Sorted(maps.Keys(msg.Headers)) {
		simple.Headers = append(simple.Headers, types.MessageHeader{
			Name:  new(name),
			Value: new(msg.Headers[name]),
		})
	}

	in := &sesv2.SendEmailInput{
		FromEmailAddress: new(msg.From),
		Destination: &types.Destination{
			ToAddresses:  msg.To,
			CcAddresses:  msg.Cc,
			BccAddresses: msg.Bcc,
		},
		Content: &types.EmailContent{Simple: simple},
	}

	if msg.ReplyTo != "" {
		in.ReplyToAddresses = []string{msg.ReplyTo}
	}

	return in
}

// content returns the UTF-8 content of data.
func content(data string) *types.Content {
	return &types.Content{Data: new(data), Charset: new(charset)}
}
