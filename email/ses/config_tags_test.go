package ses

import (
	"testing"

	"github.com/gasmod/gas/config/configtest"
)

// The rules live in Config's validate tags, so binding through a
// ConfigProvider rejects an invalid email config by itself.
func TestBind_ValidatesConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		email   map[string]any
		name    string
		wantErr bool
	}{
		{name: "valid", email: map[string]any{"region": "us-east-1", "fromemail": "a@example.com"}},
		{name: "empty region", email: map[string]any{"fromemail": "a@example.com"}, wantErr: true},
		{name: "empty from email", email: map[string]any{"region": "us-east-1"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			provider, err := configtest.NewMockConfigWithValues(map[string]any{"email": tt.email})
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
