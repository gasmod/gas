package corerouting_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gasmod/gas"
)

// These tests stop Run by signaling the test process, so none of them is
// parallel: a signal reaches every Run in flight.

// runApp runs app until it answers GET /up on port, signals it to stop, and
// returns the log records it wrote.
func runApp(t *testing.T, app *gas.App, port int, sink *logSink) []map[string]any {
	t.Helper()
	requireSignals(t)
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

// routeMapApp builds an app with three routes and two named middleware.
func routeMapApp(t *testing.T, env string, routes bool) (*gas.App, int, *logSink) {
	t.Helper()
	port := freePort(t)
	sink, logger := newSink()
	values := map[string]any{"server": map[string]any{"host": "127.0.0.1", "port": port}}
	if env != "" {
		values["gasenv"] = env
	}
	app := gas.NewApp(
		gas.WithServiceInstance(mustConfig(t, values)),
		gas.WithSingletonService[gas.Logger](logger),
	)
	r := app.Router()
	r.Register(owner("auth"), "require-auth", tagger("auth"))
	r.Register(nil, "trace", tagger("trace"))
	if routes {
		r.Get(owner("notes"), "/notes", ok, gas.MiddlewareByName("require-auth"))
		r.Post(owner("notes"), "/notes", ok)
	}
	// Without routes, readiness is probed through the not-found handler.
	if routes {
		r.Get(nil, "/up", ok)
	} else {
		r.NotFound(nil, ok)
	}
	return app, port, sink
}

func withMsg(recs []map[string]any, msg string) []map[string]any {
	var out []map[string]any
	for _, r := range recs {
		if m, _ := r["msg"].(string); m == msg {
			out = append(out, r)
		}
	}
	return out
}

func containingMsg(recs []map[string]any, sub string) []map[string]any {
	var out []map[string]any
	for _, r := range recs {
		if m, _ := r["msg"].(string); strings.Contains(m, sub) {
			out = append(out, r)
		}
	}
	return out
}

func indexOfMsg(recs []map[string]any, sub string) int {
	for i, r := range recs {
		if m, _ := r["msg"].(string); strings.Contains(m, sub) {
			return i
		}
	}
	return -1
}

// Docs: core/routing.mdx#ownership
// "`App.Run` logs both right after `Start`, when at least one route is
// registered. When the environment is `development`, it logs one table of routes
// and named middleware"
func TestRunLogsTableInDevelopment(t *testing.T) {
	app, port, sink := routeMapApp(t, "development", true)
	recs := runApp(t, app, port, sink)
	if env := app.Config().GasEnv; env != gas.EnvDevelopment {
		t.Fatalf("GasEnv = %q, want development; the test config did not set it", env)
	}

	tables := containingMsg(recs, "/notes")
	if len(tables) != 1 {
		t.Fatalf("records mentioning /notes = %d, want one table; records: %v", len(tables), recs)
	}
	table, _ := tables[0]["msg"].(string)
	for _, want := range []string{"GET", "POST", "/notes", "/up", "[notes]", "[gas/router]", "require-auth", "trace", "[auth]"} {
		if !strings.Contains(table, want) {
			t.Errorf("route table lacks %q:\n%s", want, table)
		}
	}
	if n := len(withMsg(recs, "route registered")) + len(withMsg(recs, "named middleware")); n != 0 {
		t.Errorf("per-line route records in development = %d, want 0", n)
	}
	if i, s := indexOfMsg(recs, "/notes"), indexOfMsg(recs, "server started"); i < 0 || s < 0 || i > s {
		t.Errorf("route table at record %d, server started at %d; want the table first", i, s)
	}
}

// Docs: core/routing.mdx#ownership
// "otherwise it
// logs one line per route and one per named middleware."
func TestRunLogsLinesOutsideDevelopment(t *testing.T) {
	app, port, sink := routeMapApp(t, "production", true)
	recs := runApp(t, app, port, sink)
	if env := app.Config().GasEnv; env != gas.EnvProduction {
		t.Fatalf("GasEnv = %q, want production; the test config did not set it", env)
	}
	assertPerLine(t, recs)
}

// Docs: core/routing.mdx#ownership
// "When nothing sets the
// environment, for example when no `ConfigProvider` is registered, it is empty and
// the per-line form is used."
func TestRunEmptyEnvironmentUsesLines(t *testing.T) {
	app, port, sink := routeMapApp(t, "", true)
	recs := runApp(t, app, port, sink)
	if env := app.Config().GasEnv; env != "" {
		t.Fatalf("GasEnv = %q, want empty", env)
	}
	assertPerLine(t, recs)

	bare := gas.NewApp()
	if err := bare.Start(t.Context()); err != nil {
		t.Fatalf("Start without a ConfigProvider: %v", err)
	}
	defer func() { _ = bare.Shutdown(context.Background()) }()
	if env := bare.Config().GasEnv; env != "" {
		t.Errorf("GasEnv without a ConfigProvider = %q, want empty", env)
	}
}

// Docs: core/routing.mdx#ownership
// "With no routes registered it logs nothing, not even the named
// middleware."
func TestRunLogsNothingWithoutRoutes(t *testing.T) {
	for _, env := range []string{"development", "production"} {
		t.Run(env, func(t *testing.T) {
			app, port, sink := routeMapApp(t, env, false)
			recs := runApp(t, app, port, sink)
			if n := len(withMsg(recs, "named middleware")) + len(withMsg(recs, "route registered")) +
				len(containingMsg(recs, "route map")); n != 0 {
				t.Errorf("route map records with no routes = %d, want 0", n)
			}
		})
	}
}

func assertPerLine(t *testing.T, recs []map[string]any) {
	t.Helper()
	got := map[string]bool{}
	for _, r := range withMsg(recs, "route registered") {
		got[fmt.Sprint(r["service"], " ", r["method"], " ", r["path"])] = true
	}
	want := []string{"notes GET /notes", "notes POST /notes", "gas/router GET /up"}
	if n := len(withMsg(recs, "route registered")); n != len(want) {
		t.Errorf("route registered records = %d, want %d", n, len(want))
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("no route registered record for %s; got %v", w, got)
		}
	}
	named := map[string]bool{}
	for _, r := range withMsg(recs, "named middleware") {
		named[fmt.Sprint(r["service"], " ", r["alias"])] = true
	}
	if len(withMsg(recs, "named middleware")) != 2 || !named["auth require-auth"] || !named["gas/router trace"] {
		t.Errorf("named middleware records = %v, want one each for require-auth and trace", named)
	}
	if n := len(containingMsg(recs, "route map")); n != 0 {
		t.Errorf("route table records = %d, want 0 in the per-line form", n)
	}
	if i, s := indexOfMsg(recs, "route registered"), indexOfMsg(recs, "server started"); i < 0 || s < 0 || i > s {
		t.Errorf("first route line at record %d, server started at %d; want the routes first", i, s)
	}
}

// Docs: core/routing.mdx#ownership
// "Calling `Start` and `Serve` yourself logs no route map."
func TestStartAndServeLogNoRouteMap(t *testing.T) {
	for _, env := range []string{"development", "production"} {
		t.Run(env, func(t *testing.T) {
			app, port, sink := routeMapApp(t, env, true)
			if err := app.Start(t.Context()); err != nil {
				t.Fatalf("Start: %v", err)
			}
			served := make(chan error, 1)
			go func() { served <- app.Serve() }()
			url := fmt.Sprintf("http://127.0.0.1:%d/up", port)
			up := false
			for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline) && !up; time.Sleep(10 * time.Millisecond) {
				if resp, err := http.Get(url); err == nil {
					_ = resp.Body.Close()
					up = true
				}
			}
			if err := app.Stop(context.Background()); err != nil {
				t.Errorf("Stop: %v", err)
			}
			if err := <-served; err != nil && !errors.Is(err, http.ErrServerClosed) {
				t.Errorf("Serve: %v", err)
			}
			if !up {
				t.Fatal("Serve did not start")
			}
			recs := sink.records(t)
			if n := len(containingMsg(recs, "/notes")) + len(withMsg(recs, "route registered")) +
				len(withMsg(recs, "named middleware")); n != 0 {
				t.Errorf("route map records = %d, want 0", n)
			}
		})
	}
}
