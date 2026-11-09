package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/gasmod/gas"
)

// Environment is an application environment, such as development or
// production. It is gas.Environment, so a value from either package passes
// for the other, and it has gas.Environment's methods, such as
// IsDevelopment and IsProductionLike.
type Environment = gas.Environment

// ErrEnvironmentNotAllowed indicates that the environment variable, or the
// key a provider sets, holds a value that EnvExtension does not allow.
var ErrEnvironmentNotAllowed = errors.New("environment not allowed")

// The environments EnvExtension allows by default, gas's standard
// environments.
const (
	EnvDevelopment = gas.EnvDevelopment
	EnvTesting     = gas.EnvTesting
	EnvStaging     = gas.EnvStaging
	EnvProduction  = gas.EnvProduction
)

const (
	// ExtensionName is the name EnvExtension reports.
	ExtensionName = "GasEnv"
	// DefaultConfigKey is the key EnvExtension stores the environment at,
	// and the one an Environment field binds from.
	DefaultConfigKey = "GasEnv"
	// defaultConfigKeyLower is DefaultConfigKey as the configuration stores
	// it.
	defaultConfigKeyLower = "gasenv"
	// DefaultEnvVarName is the variable EnvExtension reads the environment
	// from.
	DefaultEnvVarName = "GAS_ENV"
	// DefaultEnvironment is the environment EnvExtension falls back to.
	DefaultEnvironment = EnvDevelopment
)

// EnvOption configures an EnvExtension.
type EnvOption func(*EnvExtension)

// WithEnvVarName sets the variable the extension reads the environment
// from.
//
// Default: DefaultEnvVarName.
func WithEnvVarName(name string) EnvOption {
	return func(em *EnvExtension) {
		em.envVarName = name
	}
}

// WithAllowedEnvs sets the environments the extension accepts.
//
// Default: EnvDevelopment, EnvTesting, EnvStaging and EnvProduction.
func WithAllowedEnvs(envs ...Environment) EnvOption {
	return func(em *EnvExtension) {
		em.allowedEnvs = envs
	}
}

// WithDefault sets the environment the extension falls back to when the
// variable is unset or empty. The fallback is allowed even when it is not
// one of the allowed environments.
//
// Default: DefaultEnvironment.
func WithDefault(env Environment) EnvOption {
	return func(em *EnvExtension) {
		em.defaultEnv = env
	}
}

// WithConfigKey sets the key the extension reads and stores the
// environment at. With another key than DefaultConfigKey, the environment
// is stored at DefaultConfigKey too.
//
// Default: DefaultConfigKey.
func WithConfigKey(key string) EnvOption {
	return func(em *EnvExtension) {
		em.configKey = key
	}
}

// EnvExtension resolves the application environment during each load, as
// described in the package documentation.
type EnvExtension struct {
	envVarName string
	defaultEnv Environment
	configKey  string
	// current is the environment PostLoad resolved, under mu.
	current     Environment
	allowedEnvs []Environment
	mu          sync.RWMutex
}

var _ Extension = (*EnvExtension)(nil)

// NewExtension returns an EnvExtension configured by opts.
func NewExtension(opts ...EnvOption) *EnvExtension {
	em := &EnvExtension{
		envVarName:  DefaultEnvVarName,
		defaultEnv:  DefaultEnvironment,
		configKey:   DefaultConfigKey,
		allowedEnvs: []Environment{EnvDevelopment, EnvTesting, EnvStaging, EnvProduction},
	}
	for _, opt := range opts {
		opt(em)
	}

	return em
}

// Name returns ExtensionName.
func (em *EnvExtension) Name() string {
	return ExtensionName
}

// PreLoad stores the variable's environment, or the fallback when it is
// unset or empty, at the key unless the key holds a value. A variable that
// holds an environment that is not allowed is an error wrapping
// ErrEnvironmentNotAllowed.
func (em *EnvExtension) PreLoad(_ context.Context, cfg *Config) error {
	env := os.Getenv(em.envVarName)

	switch {
	case env == "":
		env = string(em.defaultEnv)
	case !em.isAllowed(env):
		return fmt.Errorf(
			"%w: variable %s holds %s",
			ErrEnvironmentNotAllowed,
			em.envVarName,
			em.allowedText(),
		)
	}

	err := cfg.SetDefault(em.configKey, env)
	if err != nil {
		return fmt.Errorf("store environment: %w", err)
	}

	return nil
}

// PostLoad takes the environment at the key and makes it the current one.
// A key that holds anything but an allowed environment, as a string, is an
// error wrapping ErrEnvironmentNotAllowed, and the current environment
// stays as it was.
func (em *EnvExtension) PostLoad(_ context.Context, cfg *Config) error {
	env, ok := cfg.Get(em.configKey).(string)
	if !ok || !em.isAllowed(env) {
		return fmt.Errorf(
			"%w: key %s holds %s",
			ErrEnvironmentNotAllowed,
			em.configKey,
			em.allowedText(),
		)
	}

	// Compared as the configuration compares keys, trimmed and lowercased,
	// so any key that is not GasEnv's gets the copy. strings.EqualFold
	// would also match a key that only Unicode folding makes GasEnv's, such
	// as one with a long s, which the configuration keeps apart.
	if strings.ToLower(strings.TrimSpace(em.configKey)) != defaultConfigKeyLower {
		err := cfg.Set(DefaultConfigKey, env)
		if err != nil {
			return fmt.Errorf("store environment: %w", err)
		}
	}

	em.mu.Lock()

	em.current = Environment(env)
	em.mu.Unlock()

	return nil
}

// Current returns the environment the last load resolved, or the empty
// Environment before the first load.
func (em *EnvExtension) Current() Environment {
	em.mu.RLock()
	defer em.mu.RUnlock()

	return em.current
}

// Is reports whether the current environment is env.
func (em *EnvExtension) Is(env Environment) bool {
	return em.Current() == env
}

// IsDevelopment reports whether the current environment is EnvDevelopment.
func (em *EnvExtension) IsDevelopment() bool {
	return em.Current().IsDevelopment()
}

// IsTesting reports whether the current environment is EnvTesting.
func (em *EnvExtension) IsTesting() bool {
	return em.Current().IsTesting()
}

// IsStaging reports whether the current environment is EnvStaging.
func (em *EnvExtension) IsStaging() bool {
	return em.Current().IsStaging()
}

// IsProduction reports whether the current environment is EnvProduction.
func (em *EnvExtension) IsProduction() bool {
	return em.Current().IsProduction()
}

// IsDevelopmentLike reports whether the current environment is
// EnvDevelopment or EnvTesting.
func (em *EnvExtension) IsDevelopmentLike() bool {
	return em.Current().IsDevelopmentLike()
}

// IsProductionLike reports whether the current environment is EnvProduction
// or EnvStaging.
func (em *EnvExtension) IsProductionLike() bool {
	return em.Current().IsProductionLike()
}

// isAllowed reports whether env is one of the allowed environments or the
// fallback.
func (em *EnvExtension) isAllowed(env string) bool {
	return Environment(env) == em.defaultEnv || slices.Contains(em.allowedEnvs, Environment(env))
}

// allowedText describes what the extension accepts, for an error. It names
// the allowed environments and the fallback, never the value it got, which
// came from the configuration.
func (em *EnvExtension) allowedText() string {
	names := make([]string, 0, len(em.allowedEnvs)+1)
	for _, env := range em.allowedEnvs {
		names = append(names, string(env))
	}

	if !slices.Contains(em.allowedEnvs, em.defaultEnv) {
		names = append(names, string(em.defaultEnv))
	}

	return "a value that is not one of " + strings.Join(names, ", ")
}
