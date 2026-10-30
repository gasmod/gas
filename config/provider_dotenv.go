package config

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
	// ErrDotEnvFilePathNotSet indicates that the `.env` file path is empty.
	ErrDotEnvFilePathNotSet = errors.New(".env file path is not set")
	// ErrDotEnvFileReadFailed indicates that the `.env` file could not be
	// read.
	ErrDotEnvFileReadFailed = errors.New("failed to read .env file")
	// ErrDotEnvParseFailed indicates that the `.env` file does not follow
	// the format, or holds names that conflict.
	ErrDotEnvParseFailed = errors.New("failed to parse .env file")
	// ErrSetEnv indicates that a variable could not be copied into the
	// process environment.
	ErrSetEnv = errors.New("failed to set os env")
)

const (
	defaultDotEnvFilePath = ".env"

	dotenvProviderName = "DotEnv"
)

// DotEnvProvider loads configuration from a `.env` file, as described in
// the package documentation.
type DotEnvProvider struct {
	files         providers.FSProvider
	filePath      string
	separator     string
	fileRequired  bool
	appendToOSEnv bool
}

var _ Provider = (*DotEnvProvider)(nil)

// DotEnvOption configures a DotEnvProvider.
type DotEnvOption func(*DotEnvProvider)

// WithDotEnvFilePath sets the path of the `.env` file.
//
// Default: `.env`.
func WithDotEnvFilePath(filePath string) DotEnvOption {
	return func(p *DotEnvProvider) {
		p.filePath = filePath
	}
}

// WithDotEnvSeparator sets the text that separates the levels of a name,
// as WithEnvSeparator does for EnvProvider.
//
// Default: `__`.
func WithDotEnvSeparator(sep string) DotEnvOption {
	return func(p *DotEnvProvider) {
		p.separator = sep
	}
}

// WithDotEnvFileFS makes the provider read the file from fileFS, such as an
// embed.FS, instead of the working directory. A nil fileFS reads from the
// working directory.
func WithDotEnvFileFS(fileFS fs.FS) DotEnvOption {
	return func(p *DotEnvProvider) {
		p.files.SetFS(fileFS)
	}
}

// WithDotEnvFileNotFoundPanic sets whether a missing `.env` file is an
// error, wrapping ErrDotEnvFileReadFailed. With false, a missing file loads
// no values. Despite the name, nothing panics.
//
// Default: true.
func WithDotEnvFileNotFoundPanic(panicIfNotFound bool) DotEnvOption {
	return func(p *DotEnvProvider) {
		p.fileRequired = panicIfNotFound
	}
}

// WithDotEnvFileAppendToOSEnv sets whether each load also copies the file's
// variables into the process environment, apart from the unsafe ones. Child
// processes then inherit them, secrets included, and a variable in the file
// replaces one the process had.
//
// Default: false.
func WithDotEnvFileAppendToOSEnv(appendToOSEnv bool) DotEnvOption {
	return func(p *DotEnvProvider) {
		p.appendToOSEnv = appendToOSEnv
	}
}

// NewDotEnvProvider returns a DotEnvProvider configured by opts.
func NewDotEnvProvider(opts ...DotEnvOption) *DotEnvProvider {
	p := &DotEnvProvider{
		filePath:     defaultDotEnvFilePath,
		separator:    defaultEnvSeparator,
		fileRequired: true,
	}
	for _, opt := range opts {
		opt(p)
	}

	return p
}

// Load reads the file.
func (p *DotEnvProvider) Load() (map[string]any, error) {
	if p.filePath == "" {
		return nil, ErrDotEnvFilePathNotSet
	}

	file, err := p.files.ReadFile(p.filePath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) && !p.fileRequired {
			return make(map[string]any), nil
		}

		return nil, fmt.Errorf("%w %s: %w", ErrDotEnvFileReadFailed, p.filePath, err)
	}

	var parser dotenv.Parser

	vars, err := parser.Parse(file)
	if err != nil {
		return nil, fmt.Errorf("%w %s: %w", ErrDotEnvParseFailed, p.filePath, err)
	}

	// Values are built before the process environment is touched, so a
	// conflict leaves it unchanged.
	values, err := env.ParseVariables(vars, "", p.separator)
	if err != nil {
		return nil, fmt.Errorf("%w %s: %w", ErrDotEnvParseFailed, p.filePath, err)
	}

	if p.appendToOSEnv {
		err = setOSEnv(vars)
		if err != nil {
			return nil, err
		}
	}

	return values, nil
}

// prevEnv is a variable's state before setOSEnv changed it.
type prevEnv struct {
	value string
	ok    bool
}

// setOSEnv copies vars into the process environment, skipping unsafe ones.
// It is all or nothing: if any variable cannot be set, the ones already set
// are restored to their previous state.
func setOSEnv(vars map[string]string) error {
	set := make(map[string]prevEnv, len(vars))

	for k, v := range vars {
		// Never let a .env entry overwrite security-relevant process
		// variables (PATH, LD_PRELOAD, ...): that is an arbitrary
		// environment-injection / code-execution vector (CWE-15).
		if env.IsUnsafeVar(k) {
			continue
		}

		old, ok := os.LookupEnv(k)

		err := os.Setenv(k, v)
		if err != nil {
			return errors.Join(fmt.Errorf("%w %s: %w", ErrSetEnv, k, err), restoreOSEnv(set))
		}

		set[k] = prevEnv{value: old, ok: ok}
	}

	return nil
}

// restoreOSEnv undoes a partial setOSEnv.
func restoreOSEnv(set map[string]prevEnv) error {
	var errs []error

	for k, prev := range set {
		var err error

		if prev.ok {
			err = os.Setenv(k, prev.value)
		} else {
			err = os.Unsetenv(k)
		}

		if err != nil {
			errs = append(errs, fmt.Errorf("restore %s: %w", k, err))
		}
	}

	return errors.Join(errs...)
}

// Name returns "DotEnv".
func (p *DotEnvProvider) Name() string {
	return dotenvProviderName
}
