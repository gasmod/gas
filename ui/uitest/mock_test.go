package uitest

import (
	"errors"
	"html/template"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
)

// mockCase drives one MockUI method: call invokes it, setErr installs an Fn
// that returns err and records that it ran, and args are the arguments the
// mock must record. returnsErr is false for RegisterFuncs, which has no
// result.
type mockCase struct {
	call       func(m *MockUI) error
	setErr     func(m *MockUI, err error, ran *bool)
	method     string
	args       []any
	returnsErr bool
}

func mockCases() []mockCase {
	w := httptest.NewRecorder()
	data := map[string]any{"k": "v"}
	funcs := template.FuncMap{}

	return []mockCase{
		{
			method:     "Render",
			args:       []any{"page", data},
			returnsErr: true,
			call:       func(m *MockUI) error { return m.Render(w, "page", data) },
			setErr: func(m *MockUI, err error, ran *bool) {
				m.RenderFn = func(http.ResponseWriter, string, any) error {
					*ran = true
					return err
				}
			},
		},
		{
			method:     "RenderFragment",
			args:       []any{"frag", data},
			returnsErr: true,
			call:       func(m *MockUI) error { return m.RenderFragment(w, "frag", data) },
			setErr: func(m *MockUI, err error, ran *bool) {
				m.RenderFragmentFn = func(http.ResponseWriter, string, any) error {
					*ran = true
					return err
				}
			},
		},
		{
			method:     "RenderWithStatus",
			args:       []any{http.StatusTeapot, "page", data},
			returnsErr: true,
			call: func(m *MockUI) error {
				return m.RenderWithStatus(w, http.StatusTeapot, "page", data)
			},
			setErr: func(m *MockUI, err error, ran *bool) {
				m.RenderWithStatusFn = func(http.ResponseWriter, int, string, any) error {
					*ran = true
					return err
				}
			},
		},
		{
			method: "RegisterFuncs",
			args:   []any{funcs},
			call: func(m *MockUI) error {
				m.RegisterFuncs(funcs)
				return nil
			},
			setErr: func(m *MockUI, _ error, ran *bool) {
				m.RegisterFuncsFn = func(template.FuncMap) { *ran = true }
			},
		},
	}
}

func TestMockUI_Methods(t *testing.T) {
	t.Parallel()

	for _, tc := range mockCases() {
		t.Run(tc.method, func(t *testing.T) {
			t.Parallel()

			m := &MockUI{}
			if err := tc.call(m); err != nil {
				t.Errorf("default %s = %v, want nil", tc.method, err)
			}
			if len(m.Calls) != 1 || m.Calls[0].Method != tc.method {
				t.Fatalf("Calls = %+v, want one %s call", m.Calls, tc.method)
			}
			if !reflect.DeepEqual(m.Calls[0].Args, tc.args) {
				t.Errorf("recorded args = %#v, want %#v", m.Calls[0].Args, tc.args)
			}

			wantErr := errors.New("injected")
			var ran bool
			tc.setErr(m, wantErr, &ran)
			err := tc.call(m)
			if tc.returnsErr && !errors.Is(err, wantErr) {
				t.Errorf("%s with Fn = %v, want %v", tc.method, err, wantErr)
			}
			if !ran {
				t.Errorf("%s did not call its Fn", tc.method)
			}
			if got := m.CallCount(tc.method); got != 2 {
				t.Errorf("CallCount(%s) = %d, want 2", tc.method, got)
			}
		})
	}
}

// TestMockUI_DefaultWritesNothing checks the documented default: with no Fn
// set, the render methods leave the response untouched.
func TestMockUI_DefaultWritesNothing(t *testing.T) {
	t.Parallel()

	m := &MockUI{}
	w := httptest.NewRecorder()
	_ = m.Render(w, "page", nil)
	_ = m.RenderFragment(w, "frag", nil)
	_ = m.RenderWithStatus(w, http.StatusTeapot, "page", nil)

	if w.Code != http.StatusOK || w.Body.Len() != 0 || len(w.Header()) != 0 {
		t.Errorf("response = %d %q %v, want untouched", w.Code, w.Body.String(), w.Header())
	}
}

func TestMockUI_CallCountAndReset(t *testing.T) {
	t.Parallel()

	m := &MockUI{}
	w := httptest.NewRecorder()
	_ = m.Render(w, "a", nil)
	_ = m.Render(w, "b", nil)
	m.RegisterFuncs(nil)

	if got := m.CallCount("Render"); got != 2 {
		t.Errorf("CallCount(Render) = %d, want 2", got)
	}
	if got := m.CallCount("RegisterFuncs"); got != 1 {
		t.Errorf("CallCount(RegisterFuncs) = %d, want 1", got)
	}
	if got := m.CallCount("RenderFragment"); got != 0 {
		t.Errorf("CallCount(RenderFragment) = %d, want 0", got)
	}

	m.Reset()
	if len(m.Calls) != 0 || m.CallCount("Render") != 0 {
		t.Errorf("after Reset, Calls = %+v, want none", m.Calls)
	}
}

// TestMockUI_ConcurrentCalls checks, under -race, that the mock records
// concurrent calls without a data race and loses none.
func TestMockUI_ConcurrentCalls(t *testing.T) {
	t.Parallel()

	const workers, perWorker = 8, 50
	m := &MockUI{}

	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			w := httptest.NewRecorder()
			for range perWorker {
				_ = m.Render(w, "page", nil)
				_ = m.CallCount("Render")
			}
		})
	}
	wg.Wait()

	if got := m.CallCount("Render"); got != workers*perWorker {
		t.Errorf("CallCount(Render) = %d, want %d", got, workers*perWorker)
	}
}
