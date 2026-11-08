package valkey_test

import (
	"testing"
	"time"

	"github.com/gasmod/gas/cache/internal/conformance"
	"github.com/gasmod/gas/cache/valkey"
)

// The Valkey store keeps the contract of the cache package, on a server
// shared by every test, each store with a key prefix of its own.
func TestConformance(t *testing.T) {
	t.Parallel()

	addr := valkeyServer(t).addr

	conformance.Run(t, conformance.Subject{
		Name: valkey.ServiceName,
		New: func(t *testing.T, defaultTTL time.Duration) conformance.Store {
			t.Helper()

			cfg := config(addr)

			cfg.Cache.DefaultTTL = defaultTTL

			s, _ := build(t, valkey.WithConfig(cfg))

			return s
		},
	})
}
