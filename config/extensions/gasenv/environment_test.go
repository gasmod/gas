package gasenv

import (
	"testing"
)

func TestEnvironment_IsDevelopment(t *testing.T) {
	tests := []struct {
		name string
		env  Environment
		want bool
	}{
		{
			name: "development environment returns true",
			env:  Development,
			want: true,
		},
		{
			name: "testing environment returns false",
			env:  Testing,
			want: false,
		},
		{
			name: "staging environment returns false",
			env:  Staging,
			want: false,
		},
		{
			name: "production environment returns false",
			env:  Production,
			want: false,
		},
		{
			name: "custom environment returns false",
			env:  Environment("custom"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.env.IsDevelopment(); got != tt.want {
				t.Errorf("GasEnv.IsDevelopment() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEnvironment_IsTesting(t *testing.T) {
	tests := []struct {
		name string
		env  Environment
		want bool
	}{
		{
			name: "development environment returns false",
			env:  Development,
			want: false,
		},
		{
			name: "testing environment returns true",
			env:  Testing,
			want: true,
		},
		{
			name: "staging environment returns false",
			env:  Staging,
			want: false,
		},
		{
			name: "production environment returns false",
			env:  Production,
			want: false,
		},
		{
			name: "custom environment returns false",
			env:  Environment("custom"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.env.IsTesting(); got != tt.want {
				t.Errorf("GasEnv.IsTesting() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEnvironment_IsStaging(t *testing.T) {
	tests := []struct {
		name string
		env  Environment
		want bool
	}{
		{
			name: "development environment returns false",
			env:  Development,
			want: false,
		},
		{
			name: "testing environment returns false",
			env:  Testing,
			want: false,
		},
		{
			name: "staging environment returns true",
			env:  Staging,
			want: true,
		},
		{
			name: "production environment returns false",
			env:  Production,
			want: false,
		},
		{
			name: "custom environment returns false",
			env:  Environment("custom"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.env.IsStaging(); got != tt.want {
				t.Errorf("GasEnv.IsStaging() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEnvironment_IsProduction(t *testing.T) {
	tests := []struct {
		name string
		env  Environment
		want bool
	}{
		{
			name: "development environment returns false",
			env:  Development,
			want: false,
		},
		{
			name: "testing environment returns false",
			env:  Testing,
			want: false,
		},
		{
			name: "staging environment returns false",
			env:  Staging,
			want: false,
		},
		{
			name: "production environment returns true",
			env:  Production,
			want: true,
		},
		{
			name: "custom environment returns false",
			env:  Environment("custom"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.env.IsProduction(); got != tt.want {
				t.Errorf("GasEnv.IsProduction() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEnvironment_IsDevelopmentLike(t *testing.T) {
	tests := []struct {
		name string
		env  Environment
		want bool
	}{
		{
			name: "development environment returns true",
			env:  Development,
			want: true,
		},
		{
			name: "testing environment returns true",
			env:  Testing,
			want: true,
		},
		{
			name: "staging environment returns false",
			env:  Staging,
			want: false,
		},
		{
			name: "production environment returns false",
			env:  Production,
			want: false,
		},
		{
			name: "custom environment returns false",
			env:  Environment("custom"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.env.IsDevelopmentLike(); got != tt.want {
				t.Errorf("GasEnv.IsDevelopmentLike() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEnvironment_IsProductionLike(t *testing.T) {
	tests := []struct {
		name string
		env  Environment
		want bool
	}{
		{
			name: "development environment returns false",
			env:  Development,
			want: false,
		},
		{
			name: "testing environment returns false",
			env:  Testing,
			want: false,
		},
		{
			name: "staging environment returns true",
			env:  Staging,
			want: true,
		},
		{
			name: "production environment returns true",
			env:  Production,
			want: true,
		},
		{
			name: "custom environment returns false",
			env:  Environment("custom"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.env.IsProductionLike(); got != tt.want {
				t.Errorf("GasEnv.IsProductionLike() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEnvironment_String(t *testing.T) {
	tests := []struct {
		name string
		env  Environment
		want string
	}{
		{
			name: "development environment string",
			env:  Development,
			want: "development",
		},
		{
			name: "testing environment string",
			env:  Testing,
			want: "testing",
		},
		{
			name: "staging environment string",
			env:  Staging,
			want: "staging",
		},
		{
			name: "production environment string",
			env:  Production,
			want: "production",
		},
		{
			name: "custom environment string",
			env:  Environment("custom"),
			want: "custom",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.env.String(); got != tt.want {
				t.Errorf("GasEnv.String() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEnvironmentConstants(t *testing.T) {
	tests := []struct {
		name     string
		env      Environment
		expected string
	}{
		{
			name:     "Development constant",
			env:      Development,
			expected: "development",
		},
		{
			name:     "Testing constant",
			env:      Testing,
			expected: "testing",
		},
		{
			name:     "Staging constant",
			env:      Staging,
			expected: "staging",
		},
		{
			name:     "Production constant",
			env:      Production,
			expected: "production",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if string(tt.env) != tt.expected {
				t.Errorf("GasEnv constant %s = %v, want %v", tt.name, string(tt.env), tt.expected)
			}
		})
	}
}
