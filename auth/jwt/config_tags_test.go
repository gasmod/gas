package jwt

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
		{name: "rs256", values: map[string]any{"signingmethod": "RS256"}},
		{
			name:    "unsupported method",
			values:  map[string]any{"signingmethod": "ES256"},
			wantErr: true,
		},
		{name: "zero expiry", values: map[string]any{"expiry": "0s"}, wantErr: true},
		{name: "negative expiry", values: map[string]any{"expiry": "-1m"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			provider, err := configtest.NewMockConfigWithValues(map[string]any{"jwt": tt.values})
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
