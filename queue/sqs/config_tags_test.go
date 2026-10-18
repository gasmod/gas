package sqs

import (
	"testing"

	"github.com/gasmod/gas/config/configtest"
)

// The rules live in Config's validate tags, so binding through a
// ConfigProvider rejects an invalid queue config by itself.
func TestBind_ValidatesConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		queue   map[string]any
		name    string
		wantErr bool
	}{
		{name: "defaults", queue: map[string]any{}},
		{name: "wait 0", queue: map[string]any{"waittimeseconds": 0}},
		{name: "wait 20", queue: map[string]any{"waittimeseconds": 20}},
		{name: "empty region", queue: map[string]any{"region": ""}, wantErr: true},
		{name: "wait 21", queue: map[string]any{"waittimeseconds": 21}, wantErr: true},
		{name: "wait -1", queue: map[string]any{"waittimeseconds": -1}, wantErr: true},
		{
			name:    "negative visibility",
			queue:   map[string]any{"visibilitytimeout": "-1s"},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			provider, err := configtest.NewMockConfigWithValues(map[string]any{"queue": tt.queue})
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
