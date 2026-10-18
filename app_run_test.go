package gas_test

import (
	"context"
	"net"
	"sync/atomic"
	"testing"

	"github.com/gasmod/gas"
)

// runCloser is a Service that records whether it was closed.
type runCloser struct{ closed atomic.Bool }

func (*runCloser) Name() string                  { return "run-closer" }
func (*runCloser) Init(context.Context) error    { return nil }
func (c *runCloser) Close(context.Context) error { c.closed.Store(true); return nil }

// When the listener fails, Run returns the error after stopping the App, so
// the services it started are closed.
func TestApp_Run_ServeErrorStopsApp(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	svc := &runCloser{}
	app := gas.NewApp(gas.WithServiceInstance[*runCloser](svc))
	app.Config().Server.Host = "127.0.0.1"
	app.Config().Server.Port = ln.Addr().(*net.TCPAddr).Port

	if err := app.Run(); err == nil {
		t.Fatal("Run() = nil, want the listen error")
	}
	if !svc.closed.Load() {
		t.Error("Run() returned without closing the started service")
	}
}
