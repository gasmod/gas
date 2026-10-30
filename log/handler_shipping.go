package log

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
)

// ShippingHandler is a slog.Handler that writes each record to a local
// handler and ships it to an HTTP endpoint, as described in the package
// documentation. The handlers from WithAttrs and WithGroup share its
// sender, so Flush, Close and Dropped on any of them act on all. It is
// safe for concurrent use.
type ShippingHandler struct {
	// sender queues and posts the records.
	sender *sender
	// local is the local handler, or nil.
	local slog.Handler
	// prefix is the group path, joined by `.`.
	prefix string
	// attrs are the attributes from WithAttrs, flattened.
	attrs []slog.Attr
}

var _ slog.Handler = (*ShippingHandler)(nil)

// NewShippingHandler returns a ShippingHandler that posts the records to
// endpoint, encoded by m, with opts applied, and starts its sender. The
// constructor checks, in this order: m is not nil (ErrNilMarshaler), the
// endpoint is an absolute http or https URL (ErrInvalidEndpoint), and each
// option (ErrInvalidOption). Close stops the sender.
func NewShippingHandler(
	endpoint string,
	m Marshaler,
	opts ...ShippingOption,
) (*ShippingHandler, error) {
	h, _, err := buildShippingHandler(endpoint, m, opts)
	if err != nil {
		return nil, err
	}

	// A new sender has no earlier run to wait for.
	h.sender.mu.Lock()
	h.sender.startLocked()
	h.sender.mu.Unlock()

	return h, nil
}

// buildShippingHandler checks the arguments and options, and returns a
// ShippingHandler whose sender is stopped, with its configuration.
func buildShippingHandler(
	endpoint string,
	m Marshaler,
	opts []ShippingOption,
) (*ShippingHandler, *shippingConfig, error) {
	if m == nil {
		return nil, nil, fmt.Errorf("%s: %w", errPrefix, ErrNilMarshaler)
	}

	template, shown, err := parseEndpoint(endpoint)
	if err != nil {
		return nil, nil, err
	}

	c, err := newShippingConfig(opts)
	if err != nil {
		return nil, nil, err
	}

	return &ShippingHandler{sender: newSender(template, shown, m, c), local: c.local}, c, nil
}

// Enabled reports whether the local handler enables level, or level is at
// least the shipping level.
func (h *ShippingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	if h.local != nil && h.local.Enabled(ctx, level) {
		return true
	}

	return level >= h.sender.level.Level()
}

// Handle writes r to the local handler when it enables r's level, then
// queues a Record of r for shipping when r's level is at least the
// shipping level. The Record is built here, on the caller's goroutine, as
// described in the package documentation. The local handler's error is
// reported and returned, wrapping ErrLocalHandler; r is shipped anyway.
func (h *ShippingHandler) Handle(ctx context.Context, r slog.Record) error {
	var localErr error

	if h.local != nil && h.local.Enabled(ctx, r.Level) {
		err := h.local.Handle(ctx, r.Clone())
		if err != nil {
			localErr = fmt.Errorf("%s: %w: %w", errPrefix, ErrLocalHandler, err)
			h.sender.reporter.report(ErrLocalHandler, localErr)
		}
	}

	if r.Level >= h.sender.level.Level() {
		h.sender.enqueue(h.record(r))
	}

	return localErr
}

// WithAttrs returns a handler that adds attrs to every record, after the
// attributes of h, in h's group.
func (h *ShippingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}

	out := *h

	out.attrs = slices.Clip(h.attrs)

	for _, a := range attrs {
		out.attrs = flatten(out.attrs, h.prefix, a, snapshotAny)
	}

	if h.local != nil {
		out.local = h.local.WithAttrs(attrs)
	}

	return &out
}

// WithGroup returns a handler that puts the attributes of every record
// after it in the group name. An empty name returns h.
func (h *ShippingHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}

	out := *h

	out.prefix = join(h.prefix, name)

	if h.local != nil {
		out.local = h.local.WithGroup(name)
	}

	return &out
}

// Flush posts the records queued when it is called, in batches, and waits
// for them. It returns nil when they were delivered, or when the sender is
// not running. Otherwise it returns an error wrapping ErrNotDelivered with
// the count; when ctx ended first, the error wraps ctx.Err() too, and the
// count is what was still queued or being posted.
func (h *ShippingHandler) Flush(ctx context.Context) error { return h.sender.flush(ctx) }

// Close stops accepting records and posts what is queued, in batches,
// within ctx, as described in the package documentation. It returns what
// Flush would for those records. Close on a closed handler returns nil.
func (h *ShippingHandler) Close(ctx context.Context) error { return h.sender.close(ctx) }

// Dropped returns how many records were not delivered since the handler
// was made, as described in the package documentation.
func (h *ShippingHandler) Dropped() uint64 { return h.sender.dropped.Load() }

// record returns the Record of r: the handler's attributes, then r's,
// flattened under the group path with every value captured.
func (h *ShippingHandler) record(r slog.Record) Record {
	rec := Record{
		Time:    r.Time,
		Message: r.Message,
		Attrs:   make([]slog.Attr, len(h.attrs), len(h.attrs)+r.NumAttrs()),
		Level:   r.Level,
	}
	copy(rec.Attrs, h.attrs)

	r.Attrs(func(a slog.Attr) bool {
		rec.Attrs = flatten(rec.Attrs, h.prefix, a, snapshotAny)

		return true
	})

	return rec
}
