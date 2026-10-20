package gas_test

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/gasmod/gas"
)

// A second Start while the server is serving must not write the live
// server's fields, which net/http reads for every connection: under -race
// that write is a data race. Start, Serve, Start is one way to get there.
func TestServer_SecondStartLeavesALiveServerAlone(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}

	app := gas.NewApp()
	app.Config().Server.Host = "127.0.0.1"
	app.Config().Server.Port = port
	if err := app.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- app.Serve() }()
	t.Cleanup(func() {
		_ = app.Stop(context.Background())
		if sErr := <-served; sErr != nil {
			t.Errorf("Serve() = %v", sErr)
		}
	})

	url := "http://127.0.0.1:" + strconv.Itoa(port) + "/"
	get := func() {
		resp, gErr := http.Get(url) //nolint:noctx,gosec // a local test server
		if gErr == nil {
			_ = resp.Body.Close()
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, gErr := http.Get(url) //nolint:noctx,gosec // a local test server
		if gErr == nil {
			_ = resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server never answered: %v", gErr)
		}
		time.Sleep(10 * time.Millisecond)
	}

	app.Config().Server.ReadTimeout = 9 * time.Second
	_ = app.Start(context.Background())
	get()
	if got := app.Server().ReadTimeout; got != 5*time.Second {
		t.Errorf("Server().ReadTimeout of the live server = %v, want it left at 5s", got)
	}
}
