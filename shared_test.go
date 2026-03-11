package gas_test

import (
	"sync/atomic"

	"github.com/gasmod/gas"
)

// testService is a minimal Service implementation for testing App lifecycle.
type testService struct {
	initErr   error
	closeErr  error
	name      string
	initCount atomic.Int32
	closed    atomic.Bool
}

func (s *testService) Name() string { return s.name }

func (s *testService) Init() error {
	s.initCount.Add(1)
	s.closed.Store(false)
	return s.initErr
}

func (s *testService) Close() error {
	s.closed.Store(true)
	return s.closeErr
}

var _ gas.Service = (*testService)(nil)
