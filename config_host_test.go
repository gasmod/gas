package gas_test

import (
	"strings"
	"testing"

	"github.com/go-playground/validator/v10"

	"github.com/gasmod/gas"
)

// Server.Host is checked for shape by its validate tag when the config is
// bound, not resolved: a well-formed name passes even when it does not
// resolve (.invalid never does, RFC 6761), and a malformed one fails without
// a lookup. hostname_rfc1123 takes no trailing dot.
func TestConfig_BindValidatesHost(t *testing.T) {
	t.Parallel()

	tests := []struct {
		host  string
		valid bool
	}{
		{"0.0.0.0", true},
		{"127.0.0.1", true},
		{"::", true},
		{"::1", true},
		{"localhost", true},
		{"api.example.com", true},
		{"unresolvable.invalid", true},
		{"my-host", true},
		{"1api.example.com", true},
		{"", false},
		{"example.com.", false},
		{"bad host", false},
		{"-bad.example.com", false},
		{"bad-.example.com", false},
		{"a..b", false},
		{"host:8080", false},
		{"under_score.example.com", false},
		{strings.Repeat("a", 64) + ".example.com", false},
		{strings.Repeat("a.", 127) + "com", false},
	}
	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			t.Parallel()
			err := hostConfig(t, tt.host).Bind(gas.DefaultConfig())
			if tt.valid && err != nil {
				t.Errorf("Bind() with Host %q = %v, want nil", tt.host, err)
			}
			if !tt.valid && err == nil {
				t.Errorf("Bind() with Host %q = nil, want an error", tt.host)
			}
		})
	}
}

// hostConfig returns a ConfigProvider whose Bind sets only Server.Host and
// then validates with a real validator, as the config module does. Core does
// not depend on config, so its tests fake the provider.
func hostConfig(t *testing.T, host string) gas.ConfigProvider {
	t.Helper()
	return &hostProvider{host: host}
}

type hostProvider struct {
	gas.ConfigProvider // methods other than Bind are unused; calling one panics

	host string
}

func (p *hostProvider) Bind(dest any, options ...gas.BindOption) error {
	opts := gas.BindOptions{Validate: true}
	for _, opt := range options {
		opt(&opts)
	}
	if cfg, ok := dest.(*gas.Config); ok {
		cfg.Server.Host = p.host
	}
	if !opts.Validate {
		return nil
	}
	return validator.New().Struct(dest) //nolint:wrapcheck // the test reads it as is
}
