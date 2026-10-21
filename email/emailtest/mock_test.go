package emailtest

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/gasmod/gas"
)

// mockCase drives one MockEmail method: call invokes it, setErr installs an
// Fn that returns err and records that it ran, and args are the arguments the
// mock must record.
type mockCase struct {
	call   func(m *MockEmail) error
	setErr func(m *MockEmail, err error, ran *bool)
	method string
	args   []any
}

func mockCases() []mockCase {
	ctx := context.Background()
	msg := &gas.Email{To: []string{"a@example.com"}, Subject: "hi"}
	tmsg := &gas.TemplatedEmail{HTMLTemplate: "welcome"}

	return []mockCase{
		{
			method: "Send",
			args:   []any{msg},
			call:   func(m *MockEmail) error { return m.Send(ctx, msg) },
			setErr: func(m *MockEmail, err error, ran *bool) {
				m.SendFn = func(context.Context, *gas.Email) error {
					*ran = true
					return err
				}
			},
		},
		{
			method: "SendFromTemplate",
			args:   []any{tmsg},
			call:   func(m *MockEmail) error { return m.SendFromTemplate(ctx, tmsg) },
			setErr: func(m *MockEmail, err error, ran *bool) {
				m.SendFromTemplateFn = func(context.Context, *gas.TemplatedEmail) error {
					*ran = true
					return err
				}
			},
		},
		{
			method: "CheckReady",
			args:   nil,
			call:   func(m *MockEmail) error { return m.CheckReady(ctx) },
			setErr: func(m *MockEmail, err error, ran *bool) {
				m.CheckReadyFn = func(context.Context) error {
					*ran = true
					return err
				}
			},
		},
	}
}

func TestMockEmail_Methods(t *testing.T) {
	t.Parallel()

	for _, tc := range mockCases() {
		t.Run(tc.method, func(t *testing.T) {
			t.Parallel()

			m := &MockEmail{}
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
			if err := tc.call(m); !errors.Is(err, wantErr) {
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

func TestMockEmail_CallCountAndReset(t *testing.T) {
	t.Parallel()

	m := &MockEmail{}
	ctx := context.Background()
	_ = m.Send(ctx, &gas.Email{})
	_ = m.Send(ctx, &gas.Email{})
	_ = m.CheckReady(ctx)

	if got := m.CallCount("Send"); got != 2 {
		t.Errorf("CallCount(Send) = %d, want 2", got)
	}
	if got := m.CallCount("CheckReady"); got != 1 {
		t.Errorf("CallCount(CheckReady) = %d, want 1", got)
	}
	if got := m.CallCount("SendFromTemplate"); got != 0 {
		t.Errorf("CallCount(SendFromTemplate) = %d, want 0", got)
	}

	m.Reset()
	if len(m.Calls) != 0 || m.CallCount("Send") != 0 {
		t.Errorf("after Reset, Calls = %+v, want none", m.Calls)
	}
}

// TestMockEmail_ConcurrentCalls checks, under -race, that the mock records
// concurrent calls without a data race and loses none.
func TestMockEmail_ConcurrentCalls(t *testing.T) {
	t.Parallel()

	const workers, perWorker = 8, 50
	m := &MockEmail{}
	ctx := context.Background()

	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for range perWorker {
				_ = m.Send(ctx, &gas.Email{})
				_ = m.CallCount("Send")
			}
		})
	}
	wg.Wait()

	if got := m.CallCount("Send"); got != workers*perWorker {
		t.Errorf("CallCount(Send) = %d, want %d", got, workers*perWorker)
	}
}
