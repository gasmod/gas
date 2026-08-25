package cachetest

import (
	"context"
	"errors"
	"testing"
)

func TestMockCache_CheckHealth(t *testing.T) {
	t.Parallel()

	mock := &MockCache{}

	if err := mock.CheckHealth(context.Background()); err != nil {
		t.Errorf("default CheckHealth = %v, want nil", err)
	}
	if mock.CallCount("CheckHealth") != 1 {
		t.Errorf("CallCount(CheckHealth) = %d, want 1", mock.CallCount("CheckHealth"))
	}

	wantErr := errors.New("unhealthy")
	mock.CheckHealthFn = func(context.Context) error { return wantErr }

	if err := mock.CheckHealth(context.Background()); !errors.Is(err, wantErr) {
		t.Errorf("CheckHealth = %v, want %v", err, wantErr)
	}
	if mock.CallCount("CheckHealth") != 2 {
		t.Errorf("CallCount(CheckHealth) = %d, want 2", mock.CallCount("CheckHealth"))
	}
}

func TestMockCache_CheckReady(t *testing.T) {
	t.Parallel()

	mock := &MockCache{}

	if err := mock.CheckReady(context.Background()); err != nil {
		t.Errorf("default CheckReady = %v, want nil", err)
	}
	if mock.CallCount("CheckReady") != 1 {
		t.Errorf("CallCount(CheckReady) = %d, want 1", mock.CallCount("CheckReady"))
	}

	wantErr := errors.New("not ready")
	mock.CheckReadyFn = func(context.Context) error { return wantErr }

	if err := mock.CheckReady(context.Background()); !errors.Is(err, wantErr) {
		t.Errorf("CheckReady = %v, want %v", err, wantErr)
	}
}
