package apikey

import (
	"testing"

	"github.com/gasmod/gas/config/configtest"
)

// The rules a tag can express live in Config's validate tags, so binding
// through a ConfigProvider rejects those violations by itself.
func TestBind_ValidatesConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		values  map[string]any
		name    string
		wantErr bool
	}{
		{name: "defaults", values: map[string]any{}},
		{name: "length 16", values: map[string]any{"keylength": 16}},
		{name: "length 15", values: map[string]any{"keylength": 15}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			provider, err := configtest.NewMockConfigWithValues(map[string]any{"apikey": tt.values})
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
