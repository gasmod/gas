package gas_test

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
)

// listenLocal returns a listener on a free port of 127.0.0.1.
func listenLocal(t *testing.T) net.Listener {
	t.Helper()

	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	return l
}

// listening returns a channel that receives the payload of each
// SystemServerListening of app.
func listening(app *gas.App) <-chan gas.SystemServerListeningPayload {
	ch := make(chan gas.SystemServerListeningPayload, 1)

	app.EventBus().Subscribe[gas.SystemServerListening](func(p gas.SystemServerListeningPayload) {
		ch <- p
	})

	return ch
}

// receive returns the next value of ch, failing the test after 5s.
func receive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()

	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("timed out")

		var zero T

		return zero
	}
}

// get sends a GET of url with client and returns the response's protocol
// major version and body.
func get(t *testing.T, client *http.Client, url string) (int, string) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	require.NoError(t, err)

	resp, err := client.Do(req)
	require.NoError(t, err)

	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	return resp.ProtoMajor, string(body)
}

// upApp returns an App with a NopLogger and a route `/` that answers
// `up`, not started.
func upApp() *gas.App {
	app := quietApp()
	app.Router().Get(nil, "/", func(c gas.Context) error { return c.Text(http.StatusOK, "up") })

	return app
}

// ServeListener serves on the caller's listener, emits
// SystemServerListening with its address once it serves, and returns nil
// once Stop shuts the server down, having closed the listener.
func TestApp_ServeListener(t *testing.T) {
	t.Parallel()

	app := upApp()
	events := listening(app)

	require.NoError(t, app.Start(t.Context()))

	l := listenLocal(t)
	served := make(chan error, 1)

	go func() { served <- app.ServeListener(l) }()

	ev := receive(t, events)
	assert.Equal(t, l.Addr().String(), ev.Addr.String())
	assert.False(t, ev.TLS)

	_, body := get(t, http.DefaultClient, "http://"+ev.Addr.String()+"/")
	assert.Equal(t, "up", body)

	require.NoError(t, app.Stop(t.Context()))
	require.NoError(t, receive(t, served))

	_, err := l.Accept()
	require.ErrorIs(t, err, net.ErrClosed)
}

// ServeListener before Start returns ErrNotStarted and after Stop
// ErrShutDown, without serving, and closes the listener either way.
func TestApp_ServeListenerNotServing(t *testing.T) {
	t.Parallel()

	app := upApp()
	l := listenLocal(t)

	require.ErrorIs(t, app.ServeListener(l), gas.ErrNotStarted)

	_, err := l.Accept()
	require.ErrorIs(t, err, net.ErrClosed)

	require.NoError(t, app.Start(t.Context()))
	require.NoError(t, app.Stop(t.Context()))

	l = listenLocal(t)

	require.ErrorIs(t, app.ServeListener(l), gas.ErrShutDown)

	_, err = l.Accept()
	require.ErrorIs(t, err, net.ErrClosed)
}

// Serve on port 0 listens on a port the system chose, which
// SystemServerListening carries.
func TestApp_ServePortZero(t *testing.T) {
	t.Parallel()

	app := upApp()
	events := listening(app)

	require.NoError(t, app.Start(t.Context()))

	app.Server().Addr = "127.0.0.1:0"

	served := make(chan error, 1)

	go func() { served <- app.Serve() }()

	ev := receive(t, events)
	port := ev.Addr.(*net.TCPAddr).Port
	assert.NotZero(t, port)

	_, body := get(t, http.DefaultClient, "http://"+ev.Addr.String()+"/")
	assert.Equal(t, "up", body)

	require.NoError(t, app.Stop(t.Context()))
	require.NoError(t, receive(t, served))
}

// A server whose TLSConfig holds a certificate serves TLS, with HTTP/2.
func TestApp_ServeListenerTLS(t *testing.T) {
	t.Parallel()

	// httptest's server carries a certificate for 127.0.0.1, and a client
	// that trusts it.
	ts := httptest.NewUnstartedServer(nil)

	ts.EnableHTTP2 = true
	ts.StartTLS()

	cert, client := ts.TLS.Certificates[0], ts.Client()

	ts.Close()

	app := upApp()
	events := listening(app)

	require.NoError(t, app.Start(t.Context()))

	app.Server().TLSConfig = &tls.Config{
		Certificates: []tls.Certificate{cert},
	}

	l := listenLocal(t)
	served := make(chan error, 1)

	go func() { served <- app.ServeListener(l) }()

	ev := receive(t, events)
	assert.True(t, ev.TLS)

	proto, body := get(t, client, "https://"+ev.Addr.String()+"/")
	assert.Equal(t, "up", body)
	assert.Equal(t, 2, proto)

	req, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		"https://"+ev.Addr.String()+"/",
		nil,
	)
	require.NoError(t, err)

	resp, err := http.DefaultClient.Do(req)
	if resp != nil {
		_ = resp.Body.Close()
	}

	var certErr *tls.CertificateVerificationError

	require.ErrorAs(t, err, &certErr)

	require.NoError(t, app.Stop(context.WithoutCancel(t.Context())))
	require.NoError(t, receive(t, served))
}
