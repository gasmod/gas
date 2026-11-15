package gas_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
)

// hijackApp serves an App on a free local port with a route, /ws, that
// hijacks its connection, sends it on conns and then calls hold with the
// request. It returns the App, its address, and Serve's result.
func hijackApp(
	t *testing.T,
	cfg *gas.Config,
	logger gas.Logger,
	conns chan<- net.Conn,
	hold func(*http.Request, net.Conn),
) (*gas.App, string, <-chan error) {
	t.Helper()

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())

	cfg.Server.Host = "127.0.0.1"
	cfg.Server.Port = port

	app := gas.NewApp(
		gas.WithServiceInstance(logger),
		gas.WithBaseConfig(*cfg),
	)
	app.Router().Get(nil, "/ws", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _, hErr := http.NewResponseController(w).Hijack()
		if hErr != nil {
			http.Error(w, hErr.Error(), http.StatusInternalServerError)

			return
		}

		conns <- conn

		hold(r, conn)
	}))
	require.NoError(t, app.Start(t.Context()))

	served := make(chan error, 1)

	go func() { served <- app.Serve() }()

	return app, app.Server().Addr, served
}

// dialHijacked dials addr until the server answers, asks for /ws and
// returns the client's end and the server's end of the hijacked
// connection.
func dialHijacked(t *testing.T, addr string, conns <-chan net.Conn) (net.Conn, net.Conn) {
	t.Helper()

	var client net.Conn

	require.Eventually(t, func() bool {
		c, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", addr)
		if err != nil {
			return false
		}

		client = c

		return true
	}, 5*time.Second, 10*time.Millisecond)
	t.Cleanup(func() { _ = client.Close() })

	_, err := io.WriteString(client, "GET /ws HTTP/1.1\r\nHost: test\r\n\r\n")
	require.NoError(t, err)

	select {
	case server := <-conns:
		return client, server
	case <-time.After(5 * time.Second):
		t.Fatal("the handler never hijacked the connection")

		return nil, nil
	}
}

// Stop does not close a hijacked connection, nor do the server's read,
// write and idle timeouts apply to it. It cancels the request's context once the
// server has shut down, well before ShutdownTimeout, and, while the
// handler still runs, waits out both ShutdownGracePeriods and logs the
// warning.
func TestApp_StopLeavesHijackedConnectionsOpen(t *testing.T) {
	t.Parallel()

	buf := &lockedBuffer{}
	cfg := gas.DefaultConfig()

	cfg.Server.ShutdownTimeout = time.Minute
	cfg.Server.ReadTimeout = 50 * time.Millisecond
	cfg.Server.WriteTimeout = 50 * time.Millisecond
	cfg.Server.IdleTimeout = 50 * time.Millisecond

	conns := make(chan net.Conn, 1)
	canceled := make(chan struct{})
	release := make(chan struct{})

	t.Cleanup(func() { close(release) })

	hold := func(r *http.Request, _ net.Conn) {
		<-r.Context().Done()
		close(canceled)
		<-release
	}
	app, addr, served := hijackApp(t, cfg, newJSONLogger(buf), conns, hold)
	client, server := dialHijacked(t, addr, conns)

	t.Cleanup(func() { _ = server.Close() })

	start := time.Now()
	stopped := make(chan error, 1)

	go func() { stopped <- app.Stop(context.WithoutCancel(t.Context())) }()

	select {
	case <-canceled:
	case err := <-stopped:
		t.Fatalf("Stop returned (%v) before the request's context was canceled", err)
	}

	require.NoError(t, <-stopped)
	require.NoError(t, <-served)

	elapsed := time.Since(start)
	assert.GreaterOrEqual(t, elapsed, 2*gas.ShutdownGracePeriod)
	assert.Less(t, elapsed, cfg.Server.ShutdownTimeout)

	var warned bool

	for _, line := range buf.lines() {
		warned = warned || line["msg"] == "requests still running after the shutdown grace period"
	}

	assert.True(t, warned)

	// The connection has outlived both timeouts many times over and Stop
	// has returned; it still carries bytes both ways.
	require.NoError(t, client.SetDeadline(time.Now().Add(5*time.Second)))
	require.NoError(t, server.SetDeadline(time.Now().Add(5*time.Second)))

	exchange(t, server, client, "ping")
	exchange(t, client, server, "pong")
}

// exchange writes msg to from and reads it back from to.
func exchange(t *testing.T, from, to net.Conn, msg string) {
	t.Helper()

	_, err := io.WriteString(from, msg)
	require.NoError(t, err)

	got := make([]byte, len(msg))

	_, err = io.ReadFull(to, got)
	require.NoError(t, err)
	assert.Equal(t, msg, string(got))
}

// A SystemServerShuttingDown subscriber that closes the hijacked
// connections ends their handlers, so Stop returns without waiting out
// ShutdownGracePeriod or logging the warning.
func TestApp_StopWithSubscriberClosingHijackedConnections(t *testing.T) {
	t.Parallel()

	buf := &lockedBuffer{}
	cfg := gas.DefaultConfig()
	conns := make(chan net.Conn, 1)

	// Read until the connection is closed.
	hold := func(_ *http.Request, conn net.Conn) { _, _ = io.Copy(io.Discard, conn) }
	app, addr, served := hijackApp(t, cfg, newJSONLogger(buf), conns, hold)
	client, server := dialHijacked(t, addr, conns)

	app.EventBus().Subscribe[gas.SystemServerShuttingDown](
		func(gas.SystemServerShuttingDownPayload) { _ = server.Close() },
	)

	start := time.Now()

	require.NoError(t, app.Stop(context.WithoutCancel(t.Context())))
	require.NoError(t, <-served)
	assert.Less(t, time.Since(start), gas.ShutdownGracePeriod)

	for _, line := range buf.lines() {
		assert.NotEqual(t, "requests still running after the shutdown grace period", line["msg"])
	}

	require.NoError(t, client.SetReadDeadline(time.Now().Add(5*time.Second)))

	_, err := client.Read(make([]byte, 1))
	assert.ErrorIs(t, err, io.EOF)
}
