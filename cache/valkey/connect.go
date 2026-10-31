package valkey

import (
	"context"
	"crypto/tls"
	"net"
	"sync"
	"time"

	"github.com/valkey-io/valkey-go"
)

// backoffFactor is what each wait between connect retries multiplies the
// one before by.
const backoffFactor = 2

// connect connects to the server the settings describe, retrying failed
// attempts as the settings say, and returns the client.
func (s *Service) connect(ctx context.Context, set *Settings) (valkey.Client, error) {
	opt := s.clientOption(set)
	wait := set.ConnRetryInterval

	for attempt := 1; ; attempt++ {
		client, err := attemptConnect(ctx, set.DialTimeout, opt)
		if err == nil {
			return client, nil
		}

		if attempt > set.ConnRetries || ctx.Err() != nil {
			return nil, connectError(ctx, err)
		}

		s.logger.Warn("valkey connection failed, retrying").
			Int("attempt", attempt).
			Int("retries", set.ConnRetries).
			Duration("wait", wait).
			Str("reason", reason(err)).
			Send()

		timer := time.NewTimer(wait)

		select {
		case <-ctx.Done():
			timer.Stop()

			return nil, connectError(ctx, err)
		case <-timer.C:
		}

		wait = min(backoffFactor*wait, MaxConnRetryInterval)
	}
}

// clientOption returns the valkey-go options for the settings, as the
// package documentation describes them, without DialCtxFn, which each
// attempt sets.
func (s *Service) clientOption(set *Settings) valkey.ClientOption {
	opt := valkey.ClientOption{
		InitAddress: []string{set.Addr},
		Username:    set.Username,
		Password:    string(set.Password),
		SelectDB:    set.DB,
		Dialer:      net.Dialer{Timeout: set.DialTimeout},
		// A connection whose reply is late by OpTimeout is closed, so a
		// stalled server never holds Close for longer.
		ConnWriteTimeout: set.OpTimeout,
		// One standalone server: no cluster probe on connect.
		ForceSingleClient: true,
		// The store never uses client-side caching, so connections do not
		// send CLIENT TRACKING, which an ACL user may not be allowed.
		DisableCache: true,
	}

	switch {
	case s.tlsConfig != nil:
		opt.TLSConfig = s.tlsConfig.Clone()
	case set.TLS:
		opt.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, ServerName: set.TLSServerName}
	}

	return opt
}

// attemptConnect makes one connect attempt within ctx and timeout: it
// builds a client, which dials and handshakes, and sends PING. valkey-go
// dials without a context while it builds a client, so the attempt's
// context reaches the dial through a dialGuard. On failure it closes
// what it built and returns the error, the context's when the attempt's
// context ended.
func attemptConnect(
	ctx context.Context,
	timeout time.Duration,
	opt valkey.ClientOption,
) (valkey.Client, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	g := &dialGuard{ctx: ctx}

	opt.DialCtxFn = g.dial

	client, err := valkey.NewClient(opt)
	if err == nil {
		err = client.Do(ctx, client.B().Ping().Build()).Error()
	}

	fired := g.release()

	if err == nil && fired {
		err = ctx.Err()
	}

	if err == nil {
		return client, nil
	}

	// With ForceSingleClient, NewClient returns a client beside the error
	// of its first dial.
	if client != nil {
		client.Close()
	}

	ctxErr := ctx.Err()
	if ctxErr != nil {
		return nil, ctxErr //nolint:wrapcheck // connect classifies the error and drops it.
	}

	return nil, err //nolint:wrapcheck // connect classifies the error and drops it.
}

// dialGuard dials valkey-go's connections, and ties those dialed during a
// connect attempt to the attempt's context: the dial is canceled, and the
// connection closed, when that context ends before the attempt releases
// the guard. Connections dialed after the release, when valkey-go
// reconnects, are dialed with the context valkey-go gives.
type dialGuard struct {
	// ctx is the attempt's context, or nil once released.
	ctx context.Context //nolint:containedctx // the attempt's context, held until release.
	// stops stop the closing of the connections dialed so far.
	stops []func() bool
	// mu guards ctx and stops; dial runs on valkey-go's goroutines.
	mu sync.Mutex
}

// dial is a valkey.ClientOption DialCtxFn. It dials dst as valkey-go
// does, with TLS when tc is set, within ctx and, before the release, the
// attempt's context.
func (g *dialGuard) dial(
	ctx context.Context,
	dst string,
	d *net.Dialer,
	tc *tls.Config,
) (net.Conn, error) {
	g.mu.Lock()

	bound := g.ctx
	g.mu.Unlock()

	if bound != nil {
		var cancel context.CancelFunc

		ctx, cancel = context.WithCancel(ctx)
		defer cancel()

		//nolint:contextcheck // the dial ends with valkey-go's context or the attempt's.
		stop := context.AfterFunc(bound, cancel)
		defer stop()
	}

	conn, err := dialConn(ctx, dst, d, tc)
	if err != nil || bound == nil {
		return conn, err
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if g.ctx != nil {
		//nolint:contextcheck // the connection is tied to the attempt's context.
		g.stops = append(g.stops, context.AfterFunc(bound, func() {
			// The attempt has ended; closing makes its handshake or PING
			// fail at once. The error of closing tells nothing more.
			_ = conn.Close()
		}))
	}

	return conn, nil
}

// release unties later dials from the attempt's context, keeps the
// connections dialed so far open, and reports whether the context ended,
// and closed one of them, first.
func (g *dialGuard) release() bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.ctx = nil

	fired := false

	for _, stop := range g.stops {
		if !stop() {
			fired = true
		}
	}

	g.stops = nil

	return fired
}

// dialConn dials dst as valkey-go's own dial does: over TCP with d, and
// with TLS when tc is set.
func dialConn(ctx context.Context, dst string, d *net.Dialer, tc *tls.Config) (net.Conn, error) {
	if tc != nil {
		td := tls.Dialer{NetDialer: d, Config: tc}

		//nolint:wrapcheck // connect classifies the error and drops it.
		return td.DialContext(ctx, "tcp", dst)
	}

	//nolint:wrapcheck // connect classifies the error and drops it.
	return d.DialContext(ctx, "tcp", dst)
}
