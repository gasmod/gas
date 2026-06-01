package gas_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/gasmod/gas"
)

// TestRouterServeHTTPRacesKillSwitch demonstrates a data race between
// Router.ServeHTTP and the runtime kill-switch (RemoveByModule).
//
// Router.mu (an RWMutex) guards only the router's own bookkeeping maps
// (routes/registry). ServeHTTP (router.go) calls r.mux.ServeHTTP WITHOUT
// taking r.mu, while RemoveByModule mutates the very same chi radix tree via
// r.mux.Method(...) — chi does no internal locking of its own. So concurrent
// request serving and a kill-switch invocation read and write chi's tree at
// the same time with zero synchronization between them.
//
// This is not theoretical: Worker.CloseService and Worker.RestartService call
// this exact path at RUNTIME, while the server is live and handling traffic
// (CloseService -> onServiceClose -> Router.RemoveByModule;
// RestartService -> svc.Init() -> Router.Handle). A data race on the routing
// tree is undefined behavior: torn reads, a "concurrent map writes" fatal
// panic that takes down the whole process (DoS), or a request dispatched to a
// half-mutated node.
//
// Run with the race detector to observe it:
//
//	go test -race -run TestRouterServeHTTPRacesKillSwitch ./...
//
// Under -race this test fails with a DATA RACE report (read in chi
// node.findRoute from ServeHTTP vs write in chi node.setEndpoint from
// RemoveByModule). Once ServeHTTP and the mutators are properly synchronized,
// the test passes. Without -race the bug is silent (which is exactly why it
// shipped), so the race detector is the point of this test.
func TestRouterServeHTTPRacesKillSwitch(t *testing.T) {
	const (
		nRoutes = 200
		readers = 8
		rounds  = 5
	)

	handler := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }

	paths := make([]string, nRoutes)
	for i := range paths {
		paths[i] = fmt.Sprintf("/r%d", i)
	}

	for range rounds {
		router := gas.NewRouter()
		for _, p := range paths {
			router.Handle("victim", http.MethodGet, p, handler)
		}
		router.Seal()

		stop := make(chan struct{})
		var ready sync.WaitGroup // each reader signals once it is actively serving
		ready.Add(readers)
		var done sync.WaitGroup
		done.Add(readers)

		for r := range readers {
			go func(seed int) {
				defer done.Done()
				i := seed
				first := true
				for {
					select {
					case <-stop:
						return
					default:
					}
					req := httptest.NewRequest(http.MethodGet, paths[i%nRoutes], nil)
					// ServeHTTP reads chi's tree under no lock.
					router.ServeHTTP(httptest.NewRecorder(), req)
					if first {
						ready.Done()
						first = false
					}
					i++
				}
			}(r)
		}

		// Make sure every reader is genuinely in its serve loop before the
		// kill-switch mutates the tree, guaranteeing read/write overlap.
		ready.Wait()

		// The runtime kill-switch: rip out the service's routes (one chi tree
		// write per route) while requests are in flight. This is exactly what
		// Worker.CloseService does to a live router.
		router.RemoveByModule("victim")

		close(stop)
		done.Wait()
	}
}

// TestRouterServeHTTPRacesRuntimeHandle is the symmetric counterpart to the
// kill-switch race: it exercises ServeHTTP concurrently with Handle, which is
// the path RestartService drives at runtime (RestartService -> svc.Init() ->
// Router.Handle on a sealed, live router). Adding a route rebuilds the tree
// and swaps it in; serving must never observe a half-mutated tree.
//
// Run with the race detector to exercise the synchronization:
//
//	go test -race -run TestRouterServeHTTPRacesRuntimeHandle ./...
func TestRouterServeHTTPRacesRuntimeHandle(t *testing.T) {
	const (
		nRoutes = 200
		readers = 8
	)

	handler := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }

	router := gas.NewRouter()
	router.Handle("base", http.MethodGet, "/warm", handler)
	router.Seal()

	stop := make(chan struct{})
	var ready sync.WaitGroup
	ready.Add(readers)
	var done sync.WaitGroup
	done.Add(readers)

	for r := range readers {
		go func(seed int) {
			defer done.Done()
			first := true
			for {
				select {
				case <-stop:
					return
				default:
				}
				req := httptest.NewRequest(http.MethodGet, "/warm", nil)
				router.ServeHTTP(httptest.NewRecorder(), req)
				if first {
					ready.Done()
					first = false
				}
				_ = seed
			}
		}(r)
	}

	ready.Wait()

	// Register routes at runtime while requests are in flight. Each Handle on a
	// sealed router rebuilds and atomically swaps the tree.
	for i := range nRoutes {
		router.Handle("late", http.MethodGet, fmt.Sprintf("/late%d", i), handler)
	}

	close(stop)
	done.Wait()
}

// TestRouterRemoveThenReRegisterServes verifies the copy-on-write rebuild's
// teardown/restore cycle: RemoveByModule overlays a service's routes with 503,
// and re-registering the service (as RestartService does via Init) brings the
// routes back to a live 200. This guards the removed-overlay bookkeeping that
// the rebuild applies on every swap.
func TestRouterRemoveThenReRegisterServes(t *testing.T) {
	ok := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }

	router := gas.NewRouter()
	router.Handle("victim", http.MethodGet, "/x", ok)
	router.Seal()

	serve := func() int {
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/x", nil))
		return rr.Code
	}

	if got := serve(); got != http.StatusOK {
		t.Fatalf("before removal: expected 200, got %d", got)
	}

	router.RemoveByModule("victim")
	if got := serve(); got != http.StatusServiceUnavailable {
		t.Fatalf("after removal: expected 503, got %d", got)
	}

	// Re-register the same service/path, mirroring RestartService -> Init.
	router.Handle("victim", http.MethodGet, "/x", ok)
	if got := serve(); got != http.StatusOK {
		t.Fatalf("after re-registration: expected 200, got %d", got)
	}
}

// TestRouterPostSealGroupedRouteTracked covers a service registering grouped
// routes after Seal (the RestartService -> Init path for services that use
// Route/Group). The rebuild must serve the route, track it in Routes() so it
// owns the path, and still be able to tear it down via RemoveByModule.
func TestRouterPostSealGroupedRouteTracked(t *testing.T) {
	ok := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }

	router := gas.NewRouter()
	router.Handle("base", http.MethodGet, "/base", ok)
	router.Seal()

	router.Route("/api", func(sub *gas.Router) {
		sub.Handle("svc", http.MethodGet, "/thing", ok)
	})

	serve := func() int {
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/thing", nil))
		return rr.Code
	}

	if got := serve(); got != http.StatusOK {
		t.Fatalf("serve /api/thing: expected 200, got %d", got)
	}
	if got := router.Routes()["svc"]; len(got) == 0 {
		t.Fatal("post-seal sub-route not tracked in Routes()")
	}

	router.RemoveByModule("svc")
	if got := serve(); got != http.StatusServiceUnavailable {
		t.Fatalf("after RemoveByModule: expected 503, got %d", got)
	}
}
