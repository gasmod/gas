package providers

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/gasmod/gas/config/internal/dotenv"
	"github.com/gasmod/gas/config/internal/env"
	"github.com/gasmod/gas/config/internal/providers"
)

var (
	// ErrDotEnvFilePathNotSet indicates that the .env file path is not configured.
	ErrDotEnvFilePathNotSet = errors.New(".env file path is not set")
	// ErrDotEnvFileReadFailed indicates failure to read the .env file.
	ErrDotEnvFileReadFailed = errors.New("failed to read .env file")
	// ErrDotEnvParseFailed indicates failure to parse the .env file content.
	ErrDotEnvParseFailed = errors.New("failed to parse .env file")
	// ErrSetEnv indicates a failure call to os.Setenv().
	ErrSetEnv = errors.New("failed to set os env")
)

const (
	defaultDotEnvFilePath = ".env"

	dotenvProviderName = "DotEnv"
)

// DotEnvProvider reads configuration from .env file.
type DotEnvProvider struct {
	*providers.FSProvider
	*EnvProvider

	filePath string
	// flag to panic if the .env file is not found, default to true
	panicFileNotFound bool
	// flag to append variables from the .env file to the OS's env vars.
	appendToOSEnv bool
}

var _ Provider = (*DotEnvProvider)(nil)

// DotEnvOption is a function that configures a DotEnvProvider.
type DotEnvOption func(*DotEnvProvider)

// WithDotEnvFilePath sets the .env file path.
func WithDotEnvFilePath(filePath string) DotEnvOption {
	return func(p *DotEnvProvider) {
		p.filePath = filePath
	}
}

// WithDotEnvSeparator sets the separator for nested map values.
// Given a sep=__ variables like DATABASE__URL become database.url in the resulting map.
func WithDotEnvSeparator(sep string) DotEnvOption {
	return func(p *DotEnvProvider) {
		p.separator = sep
	}
}

// WithDotEnvFileFS sets the fs of which to read the .env file from.
//
// Default: sysfs.SysFS.
func WithDotEnvFileFS(fileFS fs.FS) DotEnvOption {
	return func(p *DotEnvProvider) {
		p.SetFS(fileFS)
	}
}

// WithDotEnvFileNotFoundPanic sets the flag to panic if the .env file
// is not found.
//
// Default: true.
func WithDotEnvFileNotFoundPanic(panicIfNotFound bool) DotEnvOption {
	return func(p *DotEnvProvider) {
		p.panicFileNotFound = panicIfNotFound
	}
}

// WithDotEnvFileAppendToOSEnv sets the flag to append variables from the .env
// file to OS's env vars.
//
// Default: false. Mirroring .env entries into the live process environment
// widens the blast radius of secrets (they are inherited by child processes
// and exposed via the OS) and lets a .env entry overwrite process variables,
// so it must be opted into explicitly.
func WithDotEnvFileAppendToOSEnv(appendToOSEnv bool) DotEnvOption {
	return func(p *DotEnvProvider) {
		p.appendToOSEnv = appendToOSEnv
	}
}

// NewDotEnvProvider creates .env provider with options.
func NewDotEnvProvider(opts ...DotEnvOption) *DotEnvProvider {
	p := &DotEnvProvider{
		FSProvider:        providers.NewFSProvider(nil),
		EnvProvider:       NewEnvProvider(),
		filePath:          defaultDotEnvFilePath,
		panicFileNotFound: true,
		appendToOSEnv:     false,
	}

	for _, opt := range opts {
		opt(p)
	}

	return p
}

// Load implements the Provider interface.
func (p *DotEnvProvider) Load() (map[string]any, error) {
	if p.filePath == "" {
		return nil, ErrDotEnvFilePathNotSet
	}

	file, err := p.ReadFile(p.filePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && !p.panicFileNotFound {
			// Don't panic if file doesn't exist.
			return make(map[string]any), nil
		}

		return nil, fmt.Errorf("%w %s: %w", ErrDotEnvFileReadFailed, p.filePath, err)
	}

	vars, err := dotenv.Parse(file)
	if err != nil {
		return nil, fmt.Errorf("%w %s: %w", ErrDotEnvParseFailed, p.filePath, err)
	}

	if p.appendToOSEnv {
		for k, v := range vars {
			// Never let a .env entry overwrite security-relevant process
			// variables (PATH, LD_PRELOAD, ...): that is an arbitrary
			// environment-injection / code-execution vector (CWE-15).
			if env.IsUnsafeVar(k) {
				continue
			}
			if eErr := os.Setenv(k, v); eErr != nil {
				return nil, fmt.Errorf("%w %s: %w", ErrSetEnv, k, eErr)
			}
		}
	}

	return env.ParseVariables(vars, p.prefix, p.separator), nil
}

// Name implements the Provider interface.
func (p *DotEnvProvider) Name() string {
	return dotenvProviderName
}
