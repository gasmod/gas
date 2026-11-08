package composite

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"slices"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/template"
	"github.com/gasmod/gas/template/internal/value"
)

// ErrNilProvider indicates that the constructor was given a nil provider.
var ErrNilProvider = errors.New(template.ErrorPrefix + "provider is nil")

// Store chains a writable provider and read-only fallbacks, as described
// in the package documentation. It is safe for concurrent use when its
// providers are.
type Store struct {
	// writable receives the writes and is read first.
	writable gas.TemplateProvider
	// readers are read in order after writable.
	readers []gas.TemplateProvider
}

var _ gas.TemplateProvider = (*Store)(nil)

// NewStore returns the constructor of a Store over writable and readers,
// in the shape the gas container expects. The constructor returns an
// error wrapping ErrNilProvider that names the position of a nil
// provider.
func NewStore(
	writable gas.TemplateProvider,
	readers ...gas.TemplateProvider,
) func() (*Store, error) {
	readers = slices.Clone(readers)

	return func() (*Store, error) {
		if value.IsNil(writable) {
			return nil, fmt.Errorf("%w: the writable provider", ErrNilProvider)
		}

		for i, r := range readers {
			if value.IsNil(r) {
				return nil, fmt.Errorf("%w: reader %d", ErrNilProvider, i+1)
			}
		}

		return &Store{writable: writable, readers: readers}, nil
	}
}

// Get returns the template name from the first provider that has it: the
// writable provider, then each reader in order. It goes on to the next
// provider on an error wrapping template.ErrTemplateNotFound or
// template.ErrInvalidName, and returns any other error unchanged. When no
// provider has the name it returns the last not-found error, or, when
// every provider refused the name, the first provider's error.
func (s *Store) Get(ctx context.Context, name string) ([]byte, error) {
	var notFound, invalid error

	for _, p := range s.providers() {
		content, err := p.Get(ctx, name)

		switch {
		case errors.Is(err, template.ErrTemplateNotFound):
			notFound = err
		case errors.Is(err, template.ErrInvalidName):
			if invalid == nil {
				invalid = err
			}
		default:
			return content, err //nolint:wrapcheck // a provider's error is returned unchanged.
		}
	}

	if notFound != nil {
		return nil, notFound
	}

	return nil, invalid
}

// List returns the names of every provider's templates, once each, in
// byte order. The first provider that fails stops it, and its error is
// returned unchanged.
func (s *Store) List(ctx context.Context) ([]string, error) {
	all := []string{}

	for _, p := range s.providers() {
		names, err := p.List(ctx)
		if err != nil {
			return nil, err //nolint:wrapcheck // a provider's error is returned unchanged.
		}

		all = append(all, names...)
	}

	slices.Sort(all)

	return slices.Compact(all), nil
}

// Register registers the template with the writable provider and returns
// its error unchanged.
func (s *Store) Register(ctx context.Context, name string, content []byte) error {
	//nolint:wrapcheck // the provider's error is returned unchanged.
	return s.writable.Register(ctx, name, content)
}

// RegisterFS registers the files of fsys with the writable provider and
// returns its error unchanged.
func (s *Store) RegisterFS(ctx context.Context, fsys fs.FS) error {
	//nolint:wrapcheck // the provider's error is returned unchanged.
	return s.writable.RegisterFS(ctx, fsys)
}

// providers returns the writable provider and then the readers.
func (s *Store) providers() []gas.TemplateProvider {
	return append([]gas.TemplateProvider{s.writable}, s.readers...)
}
