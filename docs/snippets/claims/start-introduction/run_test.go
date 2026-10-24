package startintroduction_test

import (
	"fmt"
	"io"
	"net/http"
	"syscall"
	"testing"
	"time"

	"github.com/gasmod/gas"
)

// Docs: start/introduction.mdx#what-it-looks-like
// "`app.Run()` starts everything, serves HTTP on
// `0.0.0.0:8080` by default, and shuts down on SIGINT or SIGTERM."
// The address is a free port here; the default is covered by
// TestDefaultAddress. Not parallel: it signals the whole test process.
func TestRunServesAndShutsDownOnSignal(t *testing.T) {
	requireSignals(t)
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			rec := &recorder{}
			port := freePort(t)
			app := gas.NewApp(
				gas.WithServiceInstance(serverConfig(t, port)),
				gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
				gas.WithSingletonService[*greeter](func(r *gas.Router, l gas.Logger) *greeter {
					return &greeter{router: r, logger: l, rec: rec}
				}),
			)
			done := make(chan error, 1)
			go func() { done <- app.Run() }()

			url := fmt.Sprintf("http://127.0.0.1:%d/hello/ada", port)
			var body string
			served := waitFor(10*time.Second, func() bool {
				resp, err := http.Get(url)
				if err != nil {
					return false
				}
				defer func() { _ = resp.Body.Close() }()
				b, _ := io.ReadAll(resp.Body)
				body = string(b)
				return true
			})
			if !served {
				t.Fatal("Run did not start serving")
			}
			if body != "Hello, ada" {
				t.Fatalf("GET /hello/ada = %q, want \"Hello, ada\"", body)
			}
			// Serve starts before Run installs its signal handler; give it time.
			time.Sleep(200 * time.Millisecond)
			if err := sendSignal(sig); err != nil {
				t.Fatalf("signal: %v", err)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("Run: %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatalf("Run did not return after %v", sig)
			}
			if got := rec.list(); len(got) != 2 || got[1] != "close greeter" {
				t.Fatalf("lifecycle = %v, want the greeter closed on shutdown", got)
			}
			if accepting(port) {
				t.Fatal("the port still accepts connections after Run returned")
			}
		})
	}
}
