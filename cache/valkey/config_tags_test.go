package valkey

import (
	"testing"

	"github.com/gasmod/gas/config/configtest"
)

// The rules live in Config's validate tags, so binding through a
// ConfigProvider rejects an invalid cache config by itself.
func TestBind_ValidatesConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		cache   map[string]any
		name    string
		wantErr bool
	}{
		{name: "defaults", cache: map[string]any{}},
		{name: "db 3", cache: map[string]any{"db": 3}},
		{name: "empty addr", cache: map[string]any{"addr": ""}, wantErr: true},
		{name: "negative db", cache: map[string]any{"db": -1}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			provider, err := configtest.NewMockConfigWithValues(map[string]any{"cache": tt.cache})
			if err != nil {
				t.Fatal(err)
			}
			err = provider.Bind(DefaultConfig())
			if (err != nil) != tt.wantErr {
				t.Errorf("Bind() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
