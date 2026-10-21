package coreownership_test

import (
	"fmt"
	"net/http"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gasmod/gas"
)

// These tests stop Run by signaling the test process, so neither is
// parallel: a signal reaches every Run in flight.

// runApp runs an app with two owned routes in env until it serves, signals
// it to stop, and returns the log records it wrote.
func runApp(t *testing.T, env string) []map[string]any {
	t.Helper()
	requireSignals(t)
	port := freePort(t)
	sink, logger := newSink()
	app := gas.NewApp(
		gas.WithServiceInstance(mustConfig(t, map[string]any{
			"gasenv": env,
			"server": map[string]any{"host": "127.0.0.1", "port": port},
		})),
		gas.WithSingletonService[gas.Logger](logger),
	)
	app.Router().Get(owner("notes"), "/notes", ok)
	app.Router().Get(owner("health"), "/up", ok)

	done := make(chan error, 1)
	go func() { done <- app.Run() }()
	url := fmt.Sprintf("http://127.0.0.1:%d/up", port)
	up := false
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline) && !up; time.Sleep(10 * time.Millisecond) {
		select {
		case err := <-done:
			t.Fatalf("Run returned early: %v", err)
		default:
		}
		if resp, err := http.Get(url); err == nil {
			_ = resp.Body.Close()
			up = true
		}
	}
	if !up {
		t.Fatal("Run did not start serving")
	}
	// Serve starts before Run installs its signal handler; give it time.
	time.Sleep(200 * time.Millisecond)
	if err := sendSignal(syscall.SIGINT); err != nil {
		t.Fatalf("signal: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after SIGINT")
	}
	return sink.records(t)
}

// Docs: core/ownership.mdx#what-a-service-owns
// "`app.Run()`
// logs the same information at startup: as one table in the development
// environment, and as one `route registered` line per route otherwise."
func TestRunLogsRouteMap(t *testing.T) {
	dev := runApp(t, "development")
	tables := containingMsg(dev, "/notes")
	if len(tables) != 1 {
		t.Fatalf("development: %d records mention /notes, want one table", len(tables))
	}
	table, _ := tables[0]["msg"].(string)
	for _, want := range []string{"[notes]", "/notes", "[health]", "/up"} {
		if !strings.Contains(table, want) {
			t.Errorf("development route table lacks %q:\n%s", want, table)
		}
	}
	if n := len(withMsg(dev, "route registered")); n != 0 {
		t.Errorf("development: %d route registered lines, want 0", n)
	}

	prod := runApp(t, "production")
	got := map[string]bool{}
	for _, r := range withMsg(prod, "route registered") {
		got[fmt.Sprint(r["service"], " ", r["method"], " ", r["path"])] = true
	}
	if len(withMsg(prod, "route registered")) != 2 || !got["notes GET /notes"] || !got["health GET /up"] {
		t.Errorf("production route registered lines = %v, want one per route with its owner", got)
	}
}
