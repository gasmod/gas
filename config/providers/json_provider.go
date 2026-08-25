package providers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"

	"github.com/gasmod/gas/config/internal/providers"
)

var (
	// ErrJSONFilePathNotSet indicates that the JSON file path is not configured.
	ErrJSONFilePathNotSet = errors.New("JSON file path is not set")
	// ErrJSONFileReadFailed indicates failure to read the JSON config file.
	ErrJSONFileReadFailed = errors.New("failed to read JSON config file")
	// ErrJSONDecodeFailed indicates failure to decode JSON content.
	ErrJSONDecodeFailed = errors.New("failed to decode JSON")
)

const (
	jsonProviderName = "JSON"
)

// JSONProvider reads configuration from a JSON file.
type JSONProvider struct {
	*providers.FSProvider

	filePath string
}

var _ Provider = (*JSONProvider)(nil)

// JSONOption is a function that configures a JSONProvider.
type JSONOption func(*JSONProvider)

// WithJSONFilePath sets the JSON file path.
func WithJSONFilePath(filePath string) JSONOption {
	return func(p *JSONProvider) {
		p.filePath = filePath
	}
}

// WithJSONFileFS sets the fs of which to read the JSON file from.
//
// Default: sysfs.SysFS.
func WithJSONFileFS(fileFS fs.FS) JSONOption {
	return func(p *JSONProvider) {
		p.SetFS(fileFS)
	}
}

// NewJSONProvider creates a new file provider.
func NewJSONProvider(opts ...JSONOption) *JSONProvider {
	pvd := &JSONProvider{
		FSProvider: providers.NewFSProvider(nil),
	}

	for _, opt := range opts {
		opt(pvd)
	}

	return pvd
}

// Load implements the Provider interface.
func (p *JSONProvider) Load() (map[string]any, error) {
	if p.filePath == "" {
		return nil, ErrJSONFilePathNotSet
	}

	file, err := p.ReadFile(p.filePath)
	if err != nil {
		return nil, fmt.Errorf("%w %s: %w", ErrJSONFileReadFailed, p.filePath, err)
	}

	var data map[string]any
	if err = json.Unmarshal(file, &data); err != nil {
		return nil, fmt.Errorf("%w from %s: %w", ErrJSONDecodeFailed, p.filePath, err)
	}

	return data, nil
}

// Name implements the Provider interface.
func (p *JSONProvider) Name() string {
	return jsonProviderName
}
