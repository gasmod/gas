package configtest

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/config/providers"
)

// Other modules' tests pass *MockConfig where a gas.ConfigProvider is wanted.
var _ gas.ConfigProvider = (*MockConfig)(nil)

// fakeProvider is a providers.Provider to pass to the LoadProvider methods.
type fakeProvider struct{}

func (fakeProvider) Name() string                  { return "fake" }
func (fakeProvider) Load() (map[string]any, error) { return map[string]any{}, nil }

// mockCase drives one MockConfig method: call invokes it, setErr installs an
// Fn that returns err and records that it ran, and args are the arguments the
// mock must record. returnsErr is false for SetDefault and Set, which have no
// result.
type mockCase struct {
	call       func(m *MockConfig) error
	setErr     func(m *MockConfig, err error, ran *bool)
	method     string
	args       []any
	returnsErr bool
}

func mockCases() []mockCase {
	ctx := context.Background()
	values := map[string]any{"a": 1}
	dest := &struct{ A int }{}
	p := fakeProvider{}

	return []mockCase{
		{
			method: "SetDefault",
			args:   []any{"key", 1},
			call: func(m *MockConfig) error {
				m.SetDefault("key", 1)
				return nil
			},
			setErr: func(m *MockConfig, _ error, ran *bool) {
				m.SetDefaultFn = func(string, any) { *ran = true }
			},
		},
		{
			method:     "SetDefaults",
			args:       []any{values},
			returnsErr: true,
			call:       func(m *MockConfig) error { return m.SetDefaults(values) },
			setErr: func(m *MockConfig, err error, ran *bool) {
				m.SetDefaultsFn = func(any) error {
					*ran = true
					return err
				}
			},
		},
		{
			method: "Set",
			args:   []any{"key", 2},
			call: func(m *MockConfig) error {
				m.Set("key", 2)
				return nil
			},
			setErr: func(m *MockConfig, _ error, ran *bool) {
				m.SetFn = func(string, any) { *ran = true }
			},
		},
		{
			method:     "Bind",
			args:       []any{dest},
			returnsErr: true,
			call:       func(m *MockConfig) error { return m.Bind(dest) },
			setErr: func(m *MockConfig, err error, ran *bool) {
				m.BindFn = func(any, ...config.BindOption) error {
					*ran = true
					return err
				}
			},
		},
		{
			method:     "LoadProvider",
			args:       []any{p},
			returnsErr: true,
			call:       func(m *MockConfig) error { return m.LoadProvider(p) },
			setErr: func(m *MockConfig, err error, ran *bool) {
				m.LoadProviderFn = func(providers.Provider) error {
					*ran = true
					return err
				}
			},
		},
		{
			method:     "LoadProviderContext",
			args:       []any{p},
			returnsErr: true,
			call:       func(m *MockConfig) error { return m.LoadProviderContext(ctx, p) },
			setErr: func(m *MockConfig, err error, ran *bool) {
				m.LoadProviderContextFn = func(context.Context, providers.Provider) error {
					*ran = true
					return err
				}
			},
		},
	}
}

func TestMockConfig_Methods(t *testing.T) {
	t.Parallel()

	for _, tc := range mockCases() {
		t.Run(tc.method, func(t *testing.T) {
			t.Parallel()

			m := &MockConfig{}
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

// TestMockConfig_Getters covers the methods that return values rather than
// an error: Get, Find, and Values.
func TestMockConfig_Getters(t *testing.T) {
	t.Parallel()

	m := &MockConfig{}
	if got := m.Get("k"); got != nil {
		t.Errorf("default Get = %v, want nil", got)
	}
	if v, ok := m.Find("k"); v != nil || ok {
		t.Errorf("default Find = (%v, %v), want (nil, false)", v, ok)
	}
	if got := m.Values(); got != nil {
		t.Errorf("default Values = %v, want nil", got)
	}

	m.GetFn = func(key string) any { return "get:" + key }
	m.FindFn = func(key string) (any, bool) { return "find:" + key, true }
	m.ValuesFn = func() map[string]any { return map[string]any{"x": 1} }

	if got := m.Get("k"); got != "get:k" {
		t.Errorf("Get with GetFn = %v, want get:k", got)
	}
	if v, ok := m.Find("k"); v != "find:k" || !ok {
		t.Errorf("Find with FindFn = (%v, %v), want (find:k, true)", v, ok)
	}
	if got := m.Values(); !reflect.DeepEqual(got, map[string]any{"x": 1}) {
		t.Errorf("Values with ValuesFn = %v, want map[x:1]", got)
	}

	for _, method := range []string{"Get", "Find", "Values"} {
		if got := m.CallCount(method); got != 2 {
			t.Errorf("CallCount(%s) = %d, want 2", method, got)
		}
	}
	if got := m.Calls[0].Args; !reflect.DeepEqual(got, []any{"k"}) {
		t.Errorf("Get recorded args = %#v, want [k]", got)
	}
}

func TestMockConfig_CallCountAndReset(t *testing.T) {
	t.Parallel()

	m := &MockConfig{}
	_ = m.Get("a")
	_ = m.Get("b")
	m.Set("c", 1)

	if got := m.CallCount("Get"); got != 2 {
		t.Errorf("CallCount(Get) = %d, want 2", got)
	}
	if got := m.CallCount("Set"); got != 1 {
		t.Errorf("CallCount(Set) = %d, want 1", got)
	}
	if got := m.CallCount("Bind"); got != 0 {
		t.Errorf("CallCount(Bind) = %d, want 0", got)
	}

	m.Reset()
	if len(m.Calls) != 0 || m.CallCount("Get") != 0 {
		t.Errorf("after Reset, Calls = %+v, want none", m.Calls)
	}
}

// TestMockConfig_ConcurrentCalls checks, under -race, that the mock records
// concurrent calls without a data race and loses none.
func TestMockConfig_ConcurrentCalls(t *testing.T) {
	t.Parallel()

	const workers, perWorker = 8, 50
	m := &MockConfig{}

	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for range perWorker {
				_ = m.Get("k")
				_ = m.CallCount("Get")
			}
		})
	}
	wg.Wait()

	if got := m.CallCount("Get"); got != workers*perWorker {
		t.Errorf("CallCount(Get) = %d, want %d", got, workers*perWorker)
	}
}

func TestNewMockConfigWithValues(t *testing.T) {
	// An env var that the default EnvProvider would pick up must not leak
	// into the mock, which loads only the given values.
	t.Setenv("CONFIGTEST_LEAK", "from-env")

	m, err := NewMockConfigWithValues(map[string]any{
		"server": map[string]any{"port": 8080, "host": "localhost"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if got := m.Get("server.port"); got != 8080 {
		t.Errorf("Get(server.port) = %v, want 8080", got)
	}
	if v, ok := m.Find("server.host"); !ok || v != "localhost" {
		t.Errorf("Find(server.host) = (%v, %v), want (localhost, true)", v, ok)
	}
	if v, ok := m.Find("configtest_leak"); ok {
		t.Errorf("Find(configtest_leak) = %v, want the env var not loaded", v)
	}

	var dest struct {
		Server struct {
			Host string
			Port int
		}
	}
	if err := m.Bind(&dest); err != nil {
		t.Fatal(err)
	}
	if dest.Server.Host != "localhost" || dest.Server.Port != 8080 {
		t.Errorf("Bind = %+v, want localhost:8080", dest.Server)
	}

	m.Set("server.port", 9090)
	if got := m.Get("server.port"); got != 9090 {
		t.Errorf("Get after Set = %v, want 9090", got)
	}

	for _, method := range []string{"Get", "Find", "Bind", "Set"} {
		if m.CallCount(method) == 0 {
			t.Errorf("CallCount(%s) = 0, want the call recorded", method)
		}
	}

	// A field overridden after construction takes over from the real config.
	m.GetFn = func(string) any { return "overridden" }
	if got := m.Get("server.port"); got != "overridden" {
		t.Errorf("Get with overridden GetFn = %v, want overridden", got)
	}
}
