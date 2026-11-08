package config

import (
	"errors"
	"fmt"
	"io/fs"

	"github.com/gasmod/gas/config/internal/jsonobj"
	"github.com/gasmod/gas/config/internal/providers"
)

var (
	// ErrJSONFilePathNotSet indicates that the JSON file path is not set.
	ErrJSONFilePathNotSet = errors.New("JSON file path is not set")
	// ErrJSONFileReadFailed indicates that the JSON file could not be read.
	ErrJSONFileReadFailed = errors.New("failed to read JSON config file")
	// ErrJSONDecodeFailed indicates that the file does not hold a JSON
	// object.
	ErrJSONDecodeFailed = errors.New("failed to decode JSON")
)

const (
	jsonProviderName = "JSON"
)

// JSONProvider loads configuration from a JSON file, as described in the
// package documentation.
type JSONProvider struct {
	files    providers.FSProvider
	filePath string
}

var _ Provider = (*JSONProvider)(nil)

// JSONOption configures a JSONProvider.
type JSONOption func(*JSONProvider)

// WithJSONFilePath sets the path of the JSON file. It has no default.
func WithJSONFilePath(filePath string) JSONOption {
	return func(p *JSONProvider) {
		p.filePath = filePath
	}
}

// WithJSONFileFS makes the provider read the file from fileFS, such as an
// embed.FS, instead of the working directory. A nil fileFS reads from the
// working directory.
func WithJSONFileFS(fileFS fs.FS) JSONOption {
	return func(p *JSONProvider) {
		p.files.SetFS(fileFS)
	}
}

// NewJSONProvider returns a JSONProvider configured by opts.
func NewJSONProvider(opts ...JSONOption) *JSONProvider {
	p := &JSONProvider{}
	for _, opt := range opts {
		opt(p)
	}

	return p
}

// Load reads the file.
func (p *JSONProvider) Load() (map[string]any, error) {
	if p.filePath == "" {
		return nil, ErrJSONFilePathNotSet
	}

	data, err := p.files.ReadFile(p.filePath)
	if err != nil {
		return nil, fmt.Errorf("%w %s: %w", ErrJSONFileReadFailed, p.filePath, err)
	}

	values, err := jsonobj.Decode(data)
	if err != nil {
		return nil, fmt.Errorf("%w from %s: %w", ErrJSONDecodeFailed, p.filePath, err)
	}

	return values, nil
}

// Name returns "JSON".
func (p *JSONProvider) Name() string {
	return jsonProviderName
}
