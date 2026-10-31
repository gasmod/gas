//go:build unix

package gas_test

import (
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
)

// freePort returns a TCP port free at the time of the call.
func freePort(t *testing.T) int {
	t.Helper()

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())

	return port
}

// App.Run starts, logs the route map in the form of the environment,
// serves, and stops on SIGINT, returning nil.
//
// Not parallel: the signal goes to the whole test process.
//
//nolint:funlen // one scenario or table, read in order
func TestApp_Run_ShutsDownOnSignal(t *testing.T) {
	// Keep SIGINT from ending the test binary before Run handles it.
	sink := make(chan os.Signal, 1)
	signal.Notify(sink, syscall.SIGINT)

	defer signal.Stop(sink)

	for _, env := range []gas.Environment{gas.EnvDevelopment, gas.EnvProduction} {
		buf := &lockedBuffer{}
		dep := &fakeService{name: "dep"}
		port := freePort(t)
		addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
		app := gas.NewApp(
			gas.WithServiceInstance[gas.Logger](newJSONLogger(buf)),
			gas.WithServiceInstance(dep),
			gas.WithServiceInstance[gas.ConfigProvider](&mapConfig{values: map[string]any{
				"gasenv": string(env),
				"server": map[string]any{"host": "127.0.0.1", "port": port},
			}}),
		)
		app.Router().Register(dep, "audit", func(next http.Handler) http.Handler { return next })
		app.Router().
			Get(dep, "/up", func(c gas.Context) error { return c.Text(http.StatusOK, "up") },
				gas.MiddlewareByName("audit"),
				gas.MiddlewareFuncWithName("inline", func(next http.Handler) http.Handler {
					return next
				}))

		done := make(chan error, 1)

		go func() { done <- app.Run() }()

		require.Eventually(t, func() bool {
			req, _ := http.NewRequestWithContext(
				t.Context(),
				http.MethodGet,
				"http://"+addr+"/up",
				nil,
			)

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return false
			}

			_ = resp.Body.Close()

			return resp.StatusCode == http.StatusOK
		}, 5*time.Second, 10*time.Millisecond)

		require.NoError(t, syscall.Kill(os.Getpid(), syscall.SIGINT))

		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(5 * time.Second):
			t.Fatal("Run did not return after SIGINT")
		}

		assert.Equal(t, int32(1), dep.closes.Load())

		var routeLines []string

		for _, line := range buf.lines() {
			msg, _ := line["msg"].(string)
			if strings.HasPrefix(msg, "route map") || msg == "route registered" ||
				msg == "named middleware" {
				routeLines = append(routeLines, msg)
			}
		}

		if env.IsDevelopment() {
			require.Len(t, routeLines, 1)
			assert.Contains(t, routeLines[0], "route map (1 routes across 1 services)")
		} else {
			assert.Equal(t, []string{"route registered", "named middleware"}, routeLines)
		}
	}
}

// App.Run returns a Start error without serving.
func TestApp_Run_StartError(t *testing.T) {
	t.Parallel()

	err := quietApp(gas.WithShutdownTimeout(0)).Run()
	require.ErrorIs(t, err, gas.ErrInvalidOption)
}

// App.Run returns a server error, joined with Stop's, and closes the
// services Start initialized.
func TestApp_Run_ServerError(t *testing.T) {
	t.Parallel()

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	t.Cleanup(func() { _ = ln.Close() })

	dep := &fakeService{name: "dep"}
	app := quietApp(
		gas.WithServiceInstance(dep),
		gas.WithServiceInstance[gas.ConfigProvider](&mapConfig{
			values: map[string]any{
				"server": map[string]any{
					"host": "127.0.0.1",
					"port": ln.Addr().(*net.TCPAddr).Port,
				},
			},
		}),
	)

	err = app.Run()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "gas: serve: ")
	assert.Equal(t, int32(1), dep.closes.Load())
}

// Worker.Run returns a Start error at once.
func TestWorker_Run_StartError(t *testing.T) {
	t.Parallel()

	err := gas.NewWorker(gas.WithShutdownTimeout(-1)).Run()
	require.ErrorIs(t, err, gas.ErrInvalidOption)
}
