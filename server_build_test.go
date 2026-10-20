package gas_test

import (
	"context"
	"testing"
	"time"

	"github.com/gasmod/gas"
)

// Handler gives the wrapped handler without building the server, so a call
// to it before Start, from a test or a service's Init, does not pin the
// server to the config as it stood then.
func TestHandler_DoesNotBuildTheServer(t *testing.T) {
	t.Parallel()
	app := gas.NewApp()
	if app.Handler() == nil {
		t.Fatal("Handler() = nil")
	}
	app.Config().Server.ReadTimeout = 7 * time.Second
	if err := app.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })
	if got := app.Server().ReadTimeout; got != 7*time.Second {
		t.Errorf("Server().ReadTimeout after an early Handler() = %v, want 7s", got)
	}
	if app.Server().Handler == nil {
		t.Error("Server().Handler = nil")
	}
}
