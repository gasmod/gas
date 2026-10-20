package gas_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gasmod/gas"
)

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
