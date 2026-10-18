package gas_test

import (
	"sync"
	"testing"

	"github.com/gasmod/gas"
)

type lateDep struct{}

// Registering a service while other goroutines resolve from the container is
// safe: the registrations map is guarded like the instance maps. Run with
// -race; before the guard it reported a concurrent map write and read.
func TestServiceContainer_RegisterDuringResolve(t *testing.T) {
	c := gas.NewServiceContainer()
	c.RegisterSingletonService[*testDep](newTestDep)

	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 200 {
				if _, err := c.Resolve[*testDep](); err != nil {
					t.Errorf("Resolve[*testDep]() = %v", err)
					return
				}
				_ = c.CanResolve[*lateDep]()
			}
		})
	}
	wg.Go(func() {
		for range 200 {
			c.RegisterTransientService[*lateDep](func() *lateDep { return &lateDep{} })
		}
	})
	wg.Wait()

	if !c.CanResolve[*lateDep]() {
		t.Error("CanResolve[*lateDep]() = false after registering it")
	}
}
