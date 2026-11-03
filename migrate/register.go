package migrate

import (
	"fmt"
	"io/fs"
	"maps"
	"slices"
	"sync"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/migrate/internal/filename"
)

// registered is a migration as it was registered.
type registered struct {
	// mig is the migration, with Service set to its owner.
	mig gas.Migration
	// owner is the name of the owner service.
	owner string
	// checksum is the checksum of the up SQL, as checksumOf returns it.
	checksum string
	// up are the directives of the up SQL.
	up directives
	// down are the directives of the down SQL.
	down directives
}

// sameAs reports whether r and o are the same registration: one owner,
// version, description and SQL.
func (r *registered) sameAs(o *registered) bool {
	return r.owner == o.owner &&
		r.mig.Version == o.mig.Version &&
		r.mig.Description == o.mig.Description &&
		r.mig.Up == o.mig.Up &&
		r.mig.Down == o.mig.Down
}

// registry holds the registered migrations and the problems found while
// registering them. It is safe for concurrent use.
type registry struct {
	// byVersion holds the first registration of each version.
	byVersion map[string]*registered
	// problems are the registration failures, in the order they
	// happened, each once.
	problems []error
	// mu guards byVersion and problems.
	mu sync.Mutex
}

// Register adds a migration owned by service, as described in the package
// documentation. A problem with it is recorded, not returned: RunPending,
// Down and CheckReady report it.
func (s *Service) Register(service gas.Service, m gas.Migration) {
	s.reg.add(service, []gas.Migration{m})
}

// RegisterSlice registers each migration of migrations as Register does.
func (s *Service) RegisterSlice(service gas.Service, migrations []gas.Migration) {
	s.reg.add(service, migrations)
}

// RegisterFS registers the migration files of fsys for service, as
// described in the package documentation. It returns an error, and
// registers nothing, when service is nil or a file cannot be read or
// registered. A nil fsys is returned and also recorded, as Register
// records its problems, as are the problems of the migrations it reads.
func (s *Service) RegisterFS(service gas.Service, fsys fs.FS) error {
	if isNil(service) {
		return fmt.Errorf("%s: register files: %w", ServiceName, ErrNilService)
	}

	if isNil(fsys) {
		err := fmt.Errorf(
			"%s: register files (owner %s): %w",
			ServiceName, service.Name(), ErrNilFS,
		)
		s.reg.record(err)

		return err
	}

	migrations, err := readFS(fsys)
	if err != nil {
		return fmt.Errorf("%s: register files (owner %s): %w", ServiceName, service.Name(), err)
	}

	s.reg.add(service, migrations)

	return nil
}

// add registers migrations for service, recording each problem.
func (r *registry) add(service gas.Service, migrations []gas.Migration) {
	owner := ""
	if !isNil(service) {
		owner = service.Name()
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	for _, m := range migrations {
		err := r.addOne(service, owner, m)
		if err != nil {
			r.recordLocked(err)
		}
	}
}

// record records a registration problem.
func (r *registry) record(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.recordLocked(err)
}

// recordLocked records a registration problem, once. It must be called
// with mu held.
func (r *registry) recordLocked(err error) {
	if !slices.ContainsFunc(r.problems, func(p error) bool { return p.Error() == err.Error() }) {
		r.problems = append(r.problems, err)
	}
}

// addOne registers m for service, named owner, and returns its problem.
// It must be called with mu held.
func (r *registry) addOne(service gas.Service, owner string, m gas.Migration) error {
	if isNil(service) {
		return nilServiceError(m.Version)
	}

	subject := fmt.Sprintf("%s: register %s (owner %s)", ServiceName, m.Version, owner)
	if m.Version == "" {
		subject = fmt.Sprintf("%s: register (owner %s)", ServiceName, owner)

		return fmt.Errorf("%s: %w: empty", subject, ErrInvalidVersion)
	}

	up, err := parseDirectives(m.Up)
	if err != nil {
		return fmt.Errorf("%s: up SQL: %w", subject, err)
	}

	down, err := parseDirectives(m.Down)
	if err != nil {
		return fmt.Errorf("%s: down SQL: %w", subject, err)
	}

	m.Service = service

	reg := &registered{mig: m, owner: owner, up: up, down: down, checksum: checksumOf(m.Up)}

	prev, ok := r.byVersion[m.Version]

	switch {
	case !ok:
		if r.byVersion == nil {
			r.byVersion = make(map[string]*registered)
		}

		r.byVersion[m.Version] = reg

		return nil
	case prev.sameAs(reg):
		return nil
	case prev.owner != owner:
		return fmt.Errorf("%s: %w: also registered by %s", subject, ErrDuplicateVersion, prev.owner)
	default:
		return fmt.Errorf(
			"%s: %w: registered again with different content",
			subject,
			ErrDuplicateVersion,
		)
	}
}

// nilServiceError returns the problem of registering version with a nil
// service.
func nilServiceError(version string) error {
	if version == "" {
		return fmt.Errorf("%s: register: %w", ServiceName, ErrNilService)
	}

	return fmt.Errorf("%s: register %s: %w", ServiceName, version, ErrNilService)
}

// snapshot returns the registered migrations in version order, or the
// first registration problem.
func (r *registry) snapshot() ([]*registered, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.problems) > 0 {
		return nil, r.problems[0]
	}

	out := make([]*registered, 0, len(r.byVersion))
	for _, reg := range r.byVersion {
		out = append(out, reg)
	}

	slices.SortFunc(out, func(a, b *registered) int {
		return compareVersions(a.mig.Version, b.mig.Version)
	})

	// All-digit versions sort first, so with both kinds the first other
	// version follows the last all-digit one.
	if len(out) > 1 && isDigits(out[0].mig.Version) && !isDigits(out[len(out)-1].mig.Version) {
		i := slices.IndexFunc(out, func(reg *registered) bool { return !isDigits(reg.mig.Version) })
		bad := out[i]

		return nil, fmt.Errorf(
			"%s: register %s (owner %s): %w: all-digit and other versions are mixed",
			ServiceName, bad.mig.Version, bad.owner, ErrInvalidVersion,
		)
	}

	return out, nil
}

// snapshotFor returns the registered migrations in version order, or the
// first registration problem: one snapshot returns, or else a directive
// that d does not support, as unsupported finds it.
func (r *registry) snapshotFor(d *dialect) ([]*registered, error) {
	regs, err := r.snapshot()
	if err != nil {
		return nil, err
	}

	err = unsupported(regs, d)
	if err != nil {
		return nil, err
	}

	return regs, nil
}

// unsupported returns an error wrapping ErrUnsupportedDirective for the
// first of regs, in their order, whose up or down SQL opens with a
// directive d does not support, or nil when there is none.
func unsupported(regs []*registered, d *dialect) error {
	if d.noTransaction {
		return nil
	}

	for _, reg := range regs {
		var side string

		switch {
		case reg.up.noTransaction:
			side = "up SQL"
		case reg.down.noTransaction:
			side = "down SQL"
		default:
			continue
		}

		return fmt.Errorf(
			"%s: register %s (owner %s): %s: %w: %s on %s",
			ServiceName, reg.mig.Version, reg.owner, side,
			ErrUnsupportedDirective, directiveNoTransaction, d.name,
		)
	}

	return nil
}

// fsFile is a migration file RegisterFS found.
type fsFile struct {
	// path is the file's path in the file system.
	path string
	// name is what its name says.
	name filename.Name
}

// readFS returns the migrations of the files of fsys, in the order of
// their paths, or an error that names the first file that fails.
func readFS(fsys fs.FS) ([]gas.Migration, error) {
	ups, downs, err := listFS(fsys)
	if err != nil {
		return nil, err
	}

	versions := make(map[string]string, len(ups))
	out := make([]gas.Migration, 0, len(ups))

	for _, up := range ups {
		if up.name.Version == "" {
			return nil, fmt.Errorf("%w: %s has an empty version", ErrInvalidMigrationFile, up.path)
		}

		if other, ok := versions[up.name.Version]; ok {
			return nil, fmt.Errorf(
				"%w: %s repeats the version of %s",
				ErrInvalidMigrationFile,
				up.path,
				other,
			)
		}

		versions[up.name.Version] = up.path

		down, ok := downs[up.name.Stem]
		if !ok {
			return nil, fmt.Errorf("%w: %s has no down file", ErrInvalidMigrationFile, up.path)
		}

		delete(downs, up.name.Stem)

		m, readErr := readPair(fsys, up.path, down.path)
		if readErr != nil {
			return nil, readErr
		}

		m.Version, m.Description = up.name.Version, up.name.Description
		out = append(out, m)
	}

	if len(downs) > 0 {
		stem := slices.Min(slices.Collect(maps.Keys(downs)))

		return nil, fmt.Errorf("%w: %s has no up file", ErrInvalidMigrationFile, downs[stem].path)
	}

	return out, nil
}

// listFS returns the up files of fsys in path order and its down files by
// stem. Two down files of one stem are an error.
func listFS(fsys fs.FS) ([]fsFile, map[string]fsFile, error) {
	var (
		ups     []fsFile
		fileErr error
	)

	downs := map[string]fsFile{}

	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		name, ok := filename.Parse(p)
		if d.IsDir() || !ok {
			return nil
		}

		f := fsFile{path: p, name: name}

		if name.Kind == filename.Up {
			ups = append(ups, f)

			return nil
		}

		if other, ok := downs[name.Stem]; ok {
			fileErr = fmt.Errorf("%w: %s repeats %s", ErrInvalidMigrationFile, p, other.path)

			return fs.SkipAll
		}

		downs[name.Stem] = f

		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("read files: %w", err)
	}

	if fileErr != nil {
		return nil, nil, fileErr
	}

	return ups, downs, nil
}

// readPair returns a migration holding the SQL of the files up and down.
func readPair(fsys fs.FS, up, down string) (gas.Migration, error) {
	upSQL, err := fs.ReadFile(fsys, up)
	if err != nil {
		return gas.Migration{}, fmt.Errorf("read %s: %w", up, err)
	}

	downSQL, err := fs.ReadFile(fsys, down)
	if err != nil {
		return gas.Migration{}, fmt.Errorf("read %s: %w", down, err)
	}

	return gas.Migration{Up: string(upSQL), Down: string(downSQL)}, nil
}
