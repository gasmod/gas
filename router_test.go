package gas_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gasmod/gas"
)

func TestRouter_RegisterAndResolve(t *testing.T) {
	router := gas.NewRouter()

	called := false
	router.Register(nil, "require-auth", func(next http.Handler) http.Handler {
		called = true
		return next
	})

	// Use the middleware via Handle to verify it resolves.
	router.Handle(nil, "GET", "/test", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}, gas.MiddlewareByName("require-auth"))

	router.Seal()
	req := httptest.NewRequest("GET", "/test", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if !called {
		t.Fatal("middleware was not called")
	}
}

func TestRouter_HandleUnknownNamedMiddleware(t *testing.T) {
	router := gas.NewRouter()
	assertPanics(t, "unregistered middleware", func() {
		router.Handle(
			nil,
			"GET",
			"/test",
			func(w http.ResponseWriter, r *http.Request) {},
			gas.MiddlewareByName("nonexistent"),
		)
	})
}

func TestRouter_RemoveByService_RemovesMiddleware(t *testing.T) {
	auth := &testService{name: "auth"}
	billing := &testService{name: "billing"}
	test := &testService{name: "test"}

	router := gas.NewRouter()
	router.Register(auth, "require-auth", func(next http.Handler) http.Handler { return next })
	router.Register(auth, "rate-limit", func(next http.Handler) http.Handler { return next })
	router.Register(billing, "billing-mw", func(next http.Handler) http.Handler { return next })

	router.RemoveByService(auth)

	// Auth middleware should be gone — Handle should panic.
	assertPanics(t, "require-auth", func() {
		router.Handle(
			test,
			"GET",
			"/a",
			func(w http.ResponseWriter, r *http.Request) {},
			gas.MiddlewareByName("require-auth"),
		)
	})
	assertPanics(t, "rate-limit", func() {
		router.Handle(
			test,
			"GET",
			"/b",
			func(w http.ResponseWriter, r *http.Request) {},
			gas.MiddlewareByName("rate-limit"),
		)
	})

	// Billing middleware should still exist — no panic.
	router.Handle(
		test,
		"GET",
		"/c",
		func(w http.ResponseWriter, r *http.Request) {},
		gas.MiddlewareByName("billing-mw"),
	)
	router.Seal()
}

func TestRouter_HandleAndServe(t *testing.T) {
	router := gas.NewRouter()

	router.Handle(nil, "POST", "/auth/login", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	router.Seal()
	req := httptest.NewRequest("POST", "/auth/login", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if rr.Body.String() != "ok" {
		t.Fatalf("expected 'ok', got %q", rr.Body.String())
	}
}

func TestRouter_HandleWithMiddleware(t *testing.T) {
	router := gas.NewRouter()
	router.Register(nil, "add-header", func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Test", "applied")
			next.ServeHTTP(w, r)
		})
	})

	router.Handle(nil, "GET", "/billing/plans", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}, gas.MiddlewareByName("add-header"))

	router.Seal()
	req := httptest.NewRequest("GET", "/billing/plans", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Header().Get("X-Test") != "applied" {
		t.Fatal("middleware was not applied")
	}
}

func TestRouter_HandleWithFuncMiddleware(t *testing.T) {
	router := gas.NewRouter()

	router.Handle(nil, "GET", "/billing/plans", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}, gas.MiddlewareFunc(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Inline", "yes")
			next.ServeHTTP(w, r)
		})
	}))

	router.Seal()
	req := httptest.NewRequest("GET", "/billing/plans", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Header().Get("X-Inline") != "yes" {
		t.Fatal("inline MiddlewareFunc middleware was not applied")
	}
}

func TestRouter_HandleUnknownMiddleware(t *testing.T) {
	router := gas.NewRouter()
	assertPanics(t, "unknown middleware", func() {
		router.Handle(
			nil,
			"GET",
			"/test",
			func(w http.ResponseWriter, r *http.Request) {},
			gas.MiddlewareByName("nonexistent"),
		)
	})
}

func TestRouter_RemoveByService(t *testing.T) {
	router := gas.NewRouter()

	auth := &testService{name: "auth"}
	router.Handle(auth, "GET", "/auth/me", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	router.Seal()
	// Verify route works.
	req := httptest.NewRequest("GET", "/auth/me", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 before removal, got %d", rr.Code)
	}

	// Remove service routes.
	router.RemoveByService(auth)

	// Route should now return 503.
	req = httptest.NewRequest("GET", "/auth/me", nil)
	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 after removal, got %d", rr.Code)
	}
}

func TestRouter_Mux(t *testing.T) {
	router := gas.NewRouter()

	mux := router.Mux()
	if mux == nil {
		t.Fatal("expected non-nil Chi mux")
	}
}

func TestRouter_MultipleModules(t *testing.T) {
	auth := &testService{name: "auth"}
	billing := &testService{name: "billing"}

	router := gas.NewRouter()

	router.Handle(auth, "GET", "/auth/me", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("auth"))
	})

	router.Handle(billing, "GET", "/billing/plans", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("billing"))
	})

	router.Seal()
	// Remove only auth.
	router.RemoveByService(auth)

	// Auth should be 503.
	req := httptest.NewRequest("GET", "/auth/me", http.NoBody)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 for auth, got %d", rr.Code)
	}

	// Billing should still work.
	req = httptest.NewRequest("GET", "/billing/plans", http.NoBody)
	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for billing, got %d", rr.Code)
	}
	if rr.Body.String() != billing.Name() {
		t.Fatalf("expected 'billing', got %q", rr.Body.String())
	}
}

func TestRouter_Use(t *testing.T) {
	router := gas.NewRouter()

	router.UseMiddlewareFunc(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Global", "yes")
			next.ServeHTTP(w, r)
		})
	})

	router.Handle(nil, "GET", "/hello", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	router.Seal()
	req := httptest.NewRequest("GET", "/hello", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Header().Get("X-Global") != "yes" {
		t.Fatal("Use middleware was not applied")
	}
}

func TestRouter_UseMiddlewareOverride(t *testing.T) {
	router := gas.NewRouter()

	router.UseMiddlewareFunc(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Global", "yes")
			next.ServeHTTP(w, r)
		})
	})

	router.UseMiddlewareFunc(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Global", "no") // override the middleware above
			next.ServeHTTP(w, r)
		})
	})

	router.Handle(nil, "GET", "/hello", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	router.Seal()
	req := httptest.NewRequest("GET", "/hello", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Header().Get("X-Global") != "no" {
		t.Fatal("Later middleware override did not take effect")
	}
}

func TestRouter_UseMiddlewareOrder(t *testing.T) {
	router := gas.NewRouter()

	router.Register(nil, "add-global", func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Named-Global", "yes")
			next.ServeHTTP(w, r)
		})
	})

	router.Register(nil, "remove-global", func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Named-Global", "no")
			next.ServeHTTP(w, r)
		})
	})

	router.Handle(nil, "GET", "/hello", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}, gas.MiddlewareByName("add-global"), gas.MiddlewareByName("remove-global"))
	router.Seal()
	req := httptest.NewRequest("GET", "/hello", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Header().Get("X-Named-Global") != "no" {
		t.Fatal("Middleware order was not respected")
	}
}

func TestRouter_Use_Named(t *testing.T) {
	router := gas.NewRouter()

	router.Register(nil, "add-global", func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Named-Global", "yes")
			next.ServeHTTP(w, r)
		})
	})

	router.UseMiddlewareByName("add-global")

	router.Handle(nil, "GET", "/hello", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	router.Seal()
	req := httptest.NewRequest("GET", "/hello", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Header().Get("X-Named-Global") != "yes" {
		t.Fatal("MiddlewareByName Use middleware was not applied")
	}
}

func TestRouter_Use_UnknownNamed(t *testing.T) {
	router := gas.NewRouter()
	assertPanics(t, "unknown named middleware in Use", func() {
		router.UseMiddlewareByName("nonexistent")
	})
}

func TestRouter_Group(t *testing.T) {
	router := gas.NewRouter()

	router.Group(func(sub *gas.Router) {
		sub.UseMiddlewareFunc(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-Group", "yes")
				next.ServeHTTP(w, r)
			})
		})
		sub.Handle(nil, "GET", "/grouped", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
	})

	// Route outside group should not have the middleware.
	router.Handle(nil, "GET", "/ungrouped", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	router.Seal()
	// Grouped route should have header.
	req := httptest.NewRequest("GET", "/grouped", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Header().Get("X-Group") != "yes" {
		t.Fatal("Group middleware was not applied to grouped route")
	}

	// Ungrouped route should not have header.
	req = httptest.NewRequest("GET", "/ungrouped", nil)
	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Header().Get("X-Group") != "" {
		t.Fatal("Group middleware should not apply to ungrouped route")
	}
}

func TestRouter_Route(t *testing.T) {
	router := gas.NewRouter()

	router.Route("/api", func(sub *gas.Router) {
		sub.Handle(nil, "GET", "/users", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("users"))
		})

		sub.Handle(nil, "GET", "/items", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("items"))
		})
	})

	router.Seal()
	req := httptest.NewRequest("GET", "/api/users", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || rr.Body.String() != "users" {
		t.Fatalf("expected 200 'users', got %d %q", rr.Code, rr.Body.String())
	}

	req = httptest.NewRequest("GET", "/api/items", nil)
	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || rr.Body.String() != "items" {
		t.Fatalf("expected 200 'items', got %d %q", rr.Code, rr.Body.String())
	}
}

func TestRouter_Route_DuplicatePattern_Idempotent(t *testing.T) {
	router := gas.NewRouter()

	router.Route("/documents", func(sub *gas.Router) {
		sub.Handle(nil, "GET", "/", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("list"))
		})
	})
	router.Route("/documents", func(sub *gas.Router) {
		sub.Handle(nil, "POST", "/upload", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("upload"))
		})
	})

	router.Seal()

	req := httptest.NewRequest("GET", "/documents/", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || rr.Body.String() != "list" {
		t.Fatalf("expected 200 'list', got %d %q", rr.Code, rr.Body.String())
	}

	req = httptest.NewRequest("POST", "/documents/upload", nil)
	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || rr.Body.String() != "upload" {
		t.Fatalf("expected 200 'upload', got %d %q", rr.Code, rr.Body.String())
	}
}

func TestRouter_Route_DuplicatePattern_MiddlewareIsolated(t *testing.T) {
	router := gas.NewRouter()

	router.Route("/documents", func(sub *gas.Router) {
		sub.UseMiddlewareFunc(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-A", "1")
				next.ServeHTTP(w, r)
			})
		})
		sub.Handle(nil, "GET", "/a", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
	})
	router.Route("/documents", func(sub *gas.Router) {
		sub.UseMiddlewareFunc(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-B", "1")
				next.ServeHTTP(w, r)
			})
		})
		sub.Handle(nil, "GET", "/b", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
	})

	router.Seal()

	req := httptest.NewRequest("GET", "/documents/a", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Header().Get("X-A") != "1" {
		t.Fatalf("block A middleware missing on /documents/a: %v", rr.Header())
	}
	if rr.Header().Get("X-B") != "" {
		t.Fatalf("block B middleware leaked onto /documents/a: %v", rr.Header())
	}

	req = httptest.NewRequest("GET", "/documents/b", nil)
	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Header().Get("X-B") != "1" {
		t.Fatalf("block B middleware missing on /documents/b: %v", rr.Header())
	}
	if rr.Header().Get("X-A") != "" {
		t.Fatalf("block A middleware leaked onto /documents/b: %v", rr.Header())
	}
}

func TestRouter_Route_DuplicatePattern_ParentMiddlewareApplies(t *testing.T) {
	router := gas.NewRouter()

	router.UseMiddlewareFunc(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Global", "1")
			next.ServeHTTP(w, r)
		})
	})
	router.Route("/documents", func(sub *gas.Router) {
		sub.Handle(nil, "GET", "/a", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
	})
	router.Route("/documents", func(sub *gas.Router) {
		sub.Handle(nil, "GET", "/b", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
	})

	router.Seal()

	for _, path := range []string{"/documents/a", "/documents/b"} {
		req := httptest.NewRequest("GET", path, nil)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		if rr.Header().Get("X-Global") != "1" {
			t.Fatalf("global middleware missing on %s: %v", path, rr.Header())
		}
	}
}

func TestRouter_Route_DuplicatePattern_NestedRoute(t *testing.T) {
	router := gas.NewRouter()

	router.Route("/api", func(sub *gas.Router) {
		sub.Route("/v1", func(inner *gas.Router) {
			inner.Handle(nil, "GET", "/users", func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte("users"))
			})
		})
	})
	router.Route("/api", func(sub *gas.Router) {
		sub.Route("/v1", func(inner *gas.Router) {
			inner.Handle(nil, "GET", "/items", func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte("items"))
			})
		})
	})

	router.Seal()

	for _, tc := range []struct {
		path, want string
	}{
		{"/api/v1/users", "users"},
		{"/api/v1/items", "items"},
	} {
		req := httptest.NewRequest("GET", tc.path, nil)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK || rr.Body.String() != tc.want {
			t.Fatalf("%s: expected 200 %q, got %d %q", tc.path, tc.want, rr.Code, rr.Body.String())
		}
	}
}

func TestRouter_HeadAutoRegistered(t *testing.T) {
	router := gas.NewRouter()

	router.Handle(nil, "GET", "/data", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	router.Seal()

	// GET should return body.
	req := httptest.NewRequest("GET", "/data", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET: expected 200, got %d", rr.Code)
	}
	if rr.Body.String() != `{"ok":true}` {
		t.Fatalf("GET: expected body, got %q", rr.Body.String())
	}

	// HEAD should return 200 with headers but no body.
	req = httptest.NewRequest("HEAD", "/data", nil)
	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("HEAD: expected 200, got %d", rr.Code)
	}
	if rr.Header().Get("Content-Type") != "application/json" {
		t.Fatal("HEAD: expected Content-Type header to be preserved")
	}
}

func TestRouter_HeadMiddlewareSeesCorrectMethod(t *testing.T) {
	router := gas.NewRouter()

	router.Register(nil, "capture-method", func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Captured-Method", r.Method)
			next.ServeHTTP(w, r)
		})
	})

	router.Handle(nil, "GET", "/check", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}, gas.MiddlewareByName("capture-method"))

	router.Seal()

	req := httptest.NewRequest("HEAD", "/check", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if got := rr.Header().Get("X-Captured-Method"); got != "HEAD" {
		t.Fatalf("expected middleware to see HEAD, got %q", got)
	}
}

func TestRouter_HeadRemoveByService(t *testing.T) {
	router := gas.NewRouter()

	auth := &testService{name: "auth"}
	router.Handle(auth, "GET", "/auth/me", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	router.Seal()

	// HEAD should work before removal.
	req := httptest.NewRequest("HEAD", "/auth/me", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 before removal, got %d", rr.Code)
	}

	router.RemoveByService(auth)

	// HEAD should return 503 after removal.
	req = httptest.NewRequest("HEAD", "/auth/me", http.NoBody)
	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 after removal, got %d", rr.Code)
	}
}

func TestRouter_HeadAutoExcludedFromRoutes(t *testing.T) {
	svc := &testService{name: "test"}

	router := gas.NewRouter()

	router.Handle(svc, "GET", "/items", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	router.Seal()

	routes := router.Routes()
	for _, rt := range routes[svc.Name()] {
		if rt.Method == "HEAD" {
			t.Fatal("auto-registered HEAD should not appear in Routes()")
		}
	}
}

func TestRouter_HeadExplicitAppearsInRoutes(t *testing.T) {
	router := gas.NewRouter()

	test := &testService{name: "test"}
	router.Handle(test, "HEAD", "/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	router.Seal()

	var found bool
	for _, rt := range router.Routes()[test.name] {
		if rt.Method == "HEAD" && rt.Path == "/health" {
			found = true
		}
	}
	if !found {
		t.Fatal("explicitly registered HEAD should appear in Routes()")
	}
}

func TestRouter_HeadNotRegisteredForNonGetMethods(t *testing.T) {
	router := gas.NewRouter()

	router.Handle(nil, "POST", "/submit", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	router.Seal()

	req := httptest.NewRequest("HEAD", "/submit", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code == http.StatusOK {
		t.Fatal("HEAD should not be auto-registered for POST routes")
	}
}

func TestRouter_HeadExplicitOverride(t *testing.T) {
	router := gas.NewRouter()

	router.Handle(nil, "GET", "/resource", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Handler", "get")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("full body"))
	})

	// Explicit HEAD registered after GET should override the auto-registered one.
	router.Handle(nil, "HEAD", "/resource", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Handler", "head")
		w.WriteHeader(http.StatusOK)
	})

	router.Seal()

	req := httptest.NewRequest("HEAD", "/resource", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if got := rr.Header().Get("X-Handler"); got != "head" {
		t.Fatalf("expected explicit HEAD handler to win, got X-Handler=%q", got)
	}
}

func TestRouter_HeadInSubRoute(t *testing.T) {
	router := gas.NewRouter()

	router.Route("/api", func(sub *gas.Router) {
		sub.Handle(nil, "GET", "/users", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
	})

	router.Seal()

	req := httptest.NewRequest("HEAD", "/api/users", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for HEAD on sub-route, got %d", rr.Code)
	}
}

func TestRouter_NotFound_DIHandler(t *testing.T) {
	app := gas.NewApp()

	// Register NotFound before InitServices so it's in place before Seal().
	app.Router().NotFound(nil, func(ctx gas.Context) error {
		return ctx.Text(http.StatusNotFound, "custom 404")
	})

	if err := app.InitServices(context.Background()); err != nil {
		t.Fatal(err)
	}

	// At least one route must exist so Chi builds the middleware handler
	// chain. Without any route, Chi's ServeHTTP bypasses middleware for
	// NotFound — this is a Chi implementation detail.
	app.Router().Handle(nil, "GET", "/exists", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	rr := httptest.NewRecorder()
	app.Router().ServeHTTP(rr, httptest.NewRequest("GET", "/nonexistent", nil))

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
	if rr.Body.String() != "custom 404" {
		t.Fatalf("expected 'custom 404', got %q", rr.Body.String())
	}
}

// A plain NotFound handler that panics is recovered too.
func TestRouter_PlainNotFoundPanicRecovered(t *testing.T) {
	app := gas.NewApp()
	// chi runs middleware, including the request scope, only once a route
	// exists, so register one as a real app would.
	app.Router().Handle(nil, http.MethodGet, "/", func(http.ResponseWriter, *http.Request) {})
	app.Router().NotFound(nil, http.HandlerFunc(panicking))
	if err := app.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })

	rr := httptest.NewRecorder()
	app.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/nowhere", nil))
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("GET /nowhere = %d, want 500", rr.Code)
	}
}

// Each method helper registers its path for that method only.
func TestRouter_MethodHelpers(t *testing.T) {
	t.Parallel()

	type register func(r *gas.Router, path string, h any)
	helpers := map[string]register{
		http.MethodGet: func(r *gas.Router, p string, h any) { r.Get(nil, p, h) },
		http.MethodHead: func(r *gas.Router, p string, h any) {
			r.Head(nil, p, h)
		},
		http.MethodPost:   func(r *gas.Router, p string, h any) { r.Post(nil, p, h) },
		http.MethodPut:    func(r *gas.Router, p string, h any) { r.Put(nil, p, h) },
		http.MethodPatch:  func(r *gas.Router, p string, h any) { r.Patch(nil, p, h) },
		http.MethodDelete: func(r *gas.Router, p string, h any) { r.Delete(nil, p, h) },
	}
	for method, reg := range helpers {
		t.Run(method, func(t *testing.T) {
			t.Parallel()

			router := gas.NewRouter()
			reg(router, "/r", func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusTeapot)
			})
			router.Seal()

			for other := range helpers {
				rr := httptest.NewRecorder()
				router.ServeHTTP(rr, httptest.NewRequest(other, "/r", nil))
				// Get also serves HEAD.
				served := other == method || (method == http.MethodGet && other == http.MethodHead)
				if got := rr.Code == http.StatusTeapot; got != served {
					t.Errorf("%s /r served = %v (status %d), want %v", other, got, rr.Code, served)
				}
			}
		})
	}
}

// NamedMiddleware maps each registered name to its owning service.
func TestRouter_NamedMiddleware(t *testing.T) {
	t.Parallel()

	router := gas.NewRouter()
	pass := func(next http.Handler) http.Handler { return next }
	router.Register(&testService{name: "auth"}, "require-auth", pass)
	router.Register(&testService{name: "limits"}, "rate-limit", pass)

	got := router.NamedMiddleware()
	want := map[string]string{"require-auth": "auth", "rate-limit": "limits"}
	if len(got) != len(want) {
		t.Fatalf("NamedMiddleware() = %v, want %v", got, want)
	}
	for name, svc := range want {
		if got[name] != svc {
			t.Errorf("NamedMiddleware()[%q] = %q, want %q", name, got[name], svc)
		}
	}

	// The result is a snapshot, not the live registry.
	delete(got, "require-auth")
	if _, ok := router.NamedMiddleware()["require-auth"]; !ok {
		t.Error("deleting from the returned map changed the registry")
	}
}

func TestRouter_NotFound_PanicOnDoubleRegistration(t *testing.T) {
	svc1 := &testService{name: "svc1"}
	svc2 := &testService{name: "svc2"}

	router := gas.NewRouter()
	router.NotFound(svc1, func(ctx gas.Context) error { return nil })

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on double NotFound registration")
		}
	}()

	router.NotFound(svc2, func(ctx gas.Context) error { return nil })
}

func TestRouter_Handle_PanicOnMissingNamedMiddleware(t *testing.T) {
	svc := &testService{name: "svc"}

	router := gas.NewRouter()

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic when registering route with missing named middleware")
		}
	}()

	// Handle calls resolveMiddleware immediately, even if unsealed
	router.Handle(
		svc,
		"GET",
		"/",
		func(ctx gas.Context) error { return nil },
		gas.MiddlewareByName("missing"),
	)
}
