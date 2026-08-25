package storagetest_test

import (
	"context"
	"errors"
	"testing"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/storage/storagetest"
)

func TestMockStorage_CheckReadyDefault(t *testing.T) {
	m := &storagetest.MockStorage{}
	if err := m.CheckReady(context.Background()); err != nil {
		t.Errorf("CheckReady default = %v, want nil", err)
	}
	if m.CallCount("CheckReady") != 1 {
		t.Errorf("CheckReady call count = %d, want 1", m.CallCount("CheckReady"))
	}
}

func TestMockStorage_CheckReadyFn(t *testing.T) {
	want := errors.New("not ready")
	m := &storagetest.MockStorage{
		CheckReadyFn: func(context.Context) error { return want },
	}
	if got := m.CheckReady(context.Background()); !errors.Is(got, want) {
		t.Errorf("CheckReady = %v, want %v", got, want)
	}
}

func TestMockStorage_ImplementsReadyReporter(t *testing.T) {
	var _ gas.ReadyReporter = (*storagetest.MockStorage)(nil)
}
