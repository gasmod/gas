package modulesui_test

import (
	"errors"
	"html/template"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/ui/uitest"
)

// Docs: modules/ui.mdx#testing
// "`uitest.MockUI` implements `gas.UIProvider`. Each method records the call in `Calls` and delegates to `RenderFn`, `RenderFragmentFn`, `RenderWithStatusFn`, or `RegisterFuncsFn` when set. Unset, a method returns nil and writes nothing to the `ResponseWriter`. `CallCount(method)` counts the calls to one method, and `Reset` clears them."
func TestMockUI(t *testing.T) {
	t.Parallel()
	var _ gas.UIProvider = (*uitest.MockUI)(nil)

	zero := &uitest.MockUI{}
	rr := httptest.NewRecorder()
	if zero.Render(rr, "a", nil) != nil || zero.RenderFragment(rr, "b", nil) != nil || zero.RenderWithStatus(rr, 201, "c", nil) != nil {
		t.Fatal("unset methods returned an error")
	}
	zero.RegisterFuncs(template.FuncMap{})
	if rr.Body.Len() != 0 || len(rr.Header()) != 0 || rr.Code != http.StatusOK {
		t.Fatalf("unset methods wrote %d %v %q", rr.Code, rr.Header(), rr.Body.String())
	}
	want := []string{"Render", "RenderFragment", "RenderWithStatus", "RegisterFuncs"}
	if len(zero.Calls) != len(want) {
		t.Fatalf("Calls = %+v", zero.Calls)
	}
	for i, m := range want {
		if zero.Calls[i].Method != m || zero.CallCount(m) != 1 {
			t.Errorf("call %d = %s (count %d), want %s once", i, zero.Calls[i].Method, zero.CallCount(m), m)
		}
	}
	zero.Reset()
	if len(zero.Calls) != 0 || zero.CallCount("Render") != 0 {
		t.Fatal("Reset left calls")
	}

	errRender := errors.New("render")
	var registered template.FuncMap
	m := &uitest.MockUI{
		RenderFn:           func(w http.ResponseWriter, _ string, _ any) error { _, _ = w.Write([]byte("r")); return errRender },
		RenderFragmentFn:   func(w http.ResponseWriter, _ string, _ any) error { _, _ = w.Write([]byte("f")); return nil },
		RenderWithStatusFn: func(w http.ResponseWriter, s int, _ string, _ any) error { w.WriteHeader(s); return nil },
		RegisterFuncsFn:    func(f template.FuncMap) { registered = f },
	}
	rr = httptest.NewRecorder()
	if err := m.Render(rr, "p", nil); !errors.Is(err, errRender) {
		t.Errorf("Render = %v, want RenderFn's error", err)
	}
	_ = m.RenderFragment(rr, "p", nil)
	if rr.Body.String() != "rf" {
		t.Errorf("body %q, want RenderFn and RenderFragmentFn output", rr.Body.String())
	}
	rr = httptest.NewRecorder()
	_ = m.RenderWithStatus(rr, http.StatusTeapot, "p", nil)
	if rr.Code != http.StatusTeapot {
		t.Errorf("RenderWithStatusFn not called: %d", rr.Code)
	}
	fm := template.FuncMap{"x": func() {}}
	m.RegisterFuncs(fm)
	if len(registered) != 1 || m.CallCount("Render") != 1 {
		t.Errorf("RegisterFuncsFn not called or calls not recorded")
	}
}
