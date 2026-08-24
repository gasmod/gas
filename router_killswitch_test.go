package gas_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gasmod/gas"
)

// Kill-switch contract: killing service A replaces EVERY route and EVERY
// middleware registered by A with a static Service Unavailable response,
// regardless of where they are referenced. A middleware owned by A that
// another service's route pulls in short-circuits to 503, so that route stops
// serving too — the middleware is the unit of teardown, not the route.
//
// This is what Worker.CloseService drives at runtime via
// onServiceClose -> Router.RemoveByService.

func ksHandler(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }

// ksRouter builds a router where "auth" owns a named middleware and one route,
// and "billing" owns routes that reference auth's middleware in several ways.
func ksStatus(t *testing.T, h http.Handler, path string) int {
	t.Helper()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
	return rr.Code
}

func ksPassthrough(next http.Handler) http.Handler { return next }

// TestKillSwitchDisablesMiddlewareScopedInSubRouter covers a middleware pulled
// in by sub.Use() inside a Route() block by a service that was NOT killed.
func TestKillSwitchDisablesMiddlewareScopedInSubRouter(t *testing.T) {
	router := gas.NewRouter()
	router.Register("auth", "require-auth", ksPassthrough)

	router.Route("/api", func(sub *gas.Router) {
		sub.Use(gas.MiddlewareByName("require-auth"))
		sub.Handle("billing", http.MethodGet, "/invoices", ksHandler)
	})
	router.Handle("auth", http.MethodGet, "/login", ksHandler)
	router.Handle("billing", http.MethodGet, "/plans", ksHandler)
	router.Seal()

	if got := ksStatus(t, router, "/api/invoices"); got != http.StatusOK {
		t.Fatalf("before kill-switch: /api/invoices = %d, want 200", got)
	}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("RemoveByService panicked: %v", r)
		}
	}()
	router.RemoveByService("auth")

	if got := ksStatus(t, router, "/api/invoices"); got != http.StatusServiceUnavailable {
		t.Errorf("/api/invoices = %d, want 503 (guarded by killed service's middleware)", got)
	}
	if got := ksStatus(t, router, "/login"); got != http.StatusServiceUnavailable {
		t.Errorf("/login = %d, want 503 (killed service's own route)", got)
	}
	if got := ksStatus(t, router, "/plans"); got != http.StatusOK {
		t.Errorf("/plans = %d, want 200 (billing route, no reference to auth)", got)
	}
}

// TestKillSwitchDisablesMiddlewareOnSubRouterRoute covers a per-route
// MiddlewareByName on a sub-router's Handle.
func TestKillSwitchDisablesMiddlewareOnSubRouterRoute(t *testing.T) {
	router := gas.NewRouter()
	router.Register("auth", "require-auth", ksPassthrough)
	router.Group(func(sub *gas.Router) {
		sub.Handle("billing", http.MethodGet, "/invoices", ksHandler, gas.MiddlewareByName("require-auth"))
	})
	router.Seal()

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("RemoveByService panicked: %v", r)
		}
	}()
	router.RemoveByService("auth")

	if got := ksStatus(t, router, "/invoices"); got != http.StatusServiceUnavailable {
		t.Errorf("/invoices = %d, want 503", got)
	}
}

// TestKillSwitchDisablesMiddlewareOnTopLevelRoute covers a per-route
// MiddlewareByName on a top-level Handle. This is the case that silently kept
// running the dead service's middleware: Handle resolved the name once at
// registration and its op closure captured the func, so RemoveByService could
// never reach it.
func TestKillSwitchDisablesMiddlewareOnTopLevelRoute(t *testing.T) {
	ran := 0
	router := gas.NewRouter()
	router.Register("auth", "require-auth", func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ran++
			next.ServeHTTP(w, r)
		})
	})
	router.Handle("billing", http.MethodGet, "/invoices", ksHandler, gas.MiddlewareByName("require-auth"))
	router.Seal()

	if got := ksStatus(t, router, "/invoices"); got != http.StatusOK || ran != 1 {
		t.Fatalf("before kill-switch: status=%d runs=%d, want 200/1", got, ran)
	}

	router.RemoveByService("auth")

	if got := ksStatus(t, router, "/invoices"); got != http.StatusServiceUnavailable {
		t.Errorf("/invoices = %d, want 503", got)
	}
	if ran != 1 {
		t.Errorf("killed middleware ran %d times, want 1 (must not execute after teardown)", ran)
	}
}

// TestKillSwitchDisablesGlobalMiddleware covers a middleware applied globally
// via top-level Use(). Killing its owner takes down every route under it.
func TestKillSwitchDisablesGlobalMiddleware(t *testing.T) {
	router := gas.NewRouter()
	router.Register("auth", "require-auth", ksPassthrough)
	router.Use(gas.MiddlewareByName("require-auth"))
	router.Handle("billing", http.MethodGet, "/invoices", ksHandler)
	router.Seal()

	if got := ksStatus(t, router, "/invoices"); got != http.StatusOK {
		t.Fatalf("before kill-switch: /invoices = %d, want 200", got)
	}

	router.RemoveByService("auth")

	if got := ksStatus(t, router, "/invoices"); got != http.StatusServiceUnavailable {
		t.Errorf("/invoices = %d, want 503 (global middleware owned by killed service)", got)
	}
}

// TestKillSwitchLeavesRouterUsable pins that the teardown does not poison the
// router: later rebuilds (the RestartService -> Init -> Handle path) must keep
// working.
func TestKillSwitchLeavesRouterUsable(t *testing.T) {
	router := gas.NewRouter()
	router.Register("auth", "require-auth", ksPassthrough)
	router.Route("/api", func(sub *gas.Router) {
		sub.Use(gas.MiddlewareByName("require-auth"))
		sub.Handle("billing", http.MethodGet, "/invoices", ksHandler)
	})
	router.Seal()
	router.RemoveByService("auth")

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("router poisoned: registering a route after the kill-switch: %v", r)
		}
	}()
	router.Handle("reports", http.MethodGet, "/reports", ksHandler)

	if got := ksStatus(t, router, "/reports"); got != http.StatusOK {
		t.Errorf("/reports = %d, want 200", got)
	}
}

// TestRestartServiceRestoresMiddlewareAndRoutes pins the inverse: bringing the
// service back re-arms its middleware everywhere it is referenced, and its own
// routes come back with it.
func TestRestartServiceRestoresMiddlewareAndRoutes(t *testing.T) {
	router := gas.NewRouter()
	router.Register("auth", "require-auth", ksPassthrough)
	router.Route("/api", func(sub *gas.Router) {
		sub.Use(gas.MiddlewareByName("require-auth"))
		sub.Handle("billing", http.MethodGet, "/invoices", ksHandler)
	})
	router.Handle("auth", http.MethodGet, "/login", ksHandler)
	router.Seal()
	router.RemoveByService("auth")

	if got := ksStatus(t, router, "/api/invoices"); got != http.StatusServiceUnavailable {
		t.Fatalf("after kill-switch: /api/invoices = %d, want 503", got)
	}

	// RestartService -> svc.Init() re-registers the middleware, then the routes.
	router.Register("auth", "require-auth", ksPassthrough)

	if got := ksStatus(t, router, "/api/invoices"); got != http.StatusOK {
		t.Errorf("after restart: /api/invoices = %d, want 200 (middleware re-armed)", got)
	}

	router.Handle("auth", http.MethodGet, "/login", ksHandler)
	if got := ksStatus(t, router, "/login"); got != http.StatusOK {
		t.Errorf("after restart: /login = %d, want 200", got)
	}
}

// TestKillSwitchLeavesAnonymousMiddlewareAlone pins the boundary: ownership is
// only tracked for names passed to Register, so an inline MiddlewareFunc is
// not swept up by an unrelated service's teardown.
func TestKillSwitchLeavesAnonymousMiddlewareAlone(t *testing.T) {
	ran := 0
	router := gas.NewRouter()
	router.Register("auth", "require-auth", ksPassthrough)
	router.Use(gas.MiddlewareFunc(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ran++
			next.ServeHTTP(w, r)
		})
	}))
	router.Handle("billing", http.MethodGet, "/plans", ksHandler)
	router.Seal()
	router.RemoveByService("auth")

	if got := ksStatus(t, router, "/plans"); got != http.StatusOK {
		t.Errorf("/plans = %d, want 200", got)
	}
	if ran != 1 {
		t.Errorf("inline middleware ran %d times, want 1", ran)
	}
}
