package gas_test

import (
	"context"
	"testing"
	"time"

	"github.com/gasmod/gas"
)

// DefaultConfig bounds the time to read request headers, so a server whose
// ReadTimeout is zeroed to allow streaming still has a header deadline:
// net/http falls back to ReadTimeout only when ReadHeaderTimeout is zero.
// The default is the 5s that fallback gave a default config before.
func TestDefaultConfig_ReadHeaderTimeout(t *testing.T) {
	t.Parallel()
	if got := gas.DefaultConfig().Server.ReadHeaderTimeout; got != 5*time.Second {
		t.Errorf("DefaultConfig().Server.ReadHeaderTimeout = %v, want 5s", got)
	}
}

// Server copies ReadHeaderTimeout from the config, the default and a
// configured value alike.
func TestServer_ReadHeaderTimeout(t *testing.T) {
	t.Parallel()
	for name, tt := range map[string]struct {
		configure func(*gas.Config)
		want      time.Duration
	}{
		"default":    {configure: func(*gas.Config) {}, want: 5 * time.Second},
		"configured": {configure: func(c *gas.Config) { c.Server.ReadHeaderTimeout = 3 * time.Second }, want: 3 * time.Second},
		"streaming": {
			configure: func(c *gas.Config) { c.Server.ReadTimeout = 0 },
			want:      5 * time.Second,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			app := gas.NewApp()
			tt.configure(app.Config())
			if err := app.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = app.Shutdown(context.Background()) })
			if got := app.Server().ReadHeaderTimeout; got != tt.want {
				t.Errorf("Server().ReadHeaderTimeout = %v, want %v", got, tt.want)
			}
		})
	}
}
