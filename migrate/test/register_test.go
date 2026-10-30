package migrate_test

import (
	"context"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/migrate"
)

// typedNil is a gas.Service whose Name panics on a nil receiver.
type typedNil struct{ name string }

// Name returns the name, panicking on a nil receiver.
func (s *typedNil) Name() string { return s.name }

// Init does nothing.
func (*typedNil) Init(context.Context) error { return nil }

// Close does nothing.
func (*typedNil) Close(context.Context) error { return nil }

// A registration problem is recorded, not returned, and RunPending,
// Down and CheckReady report the first one, applying nothing; the
// problem migration is not registered.
func TestRegister_ProblemsReported(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t)

	e.svc.Register(app, mig("001", "a"))
	e.svc.Register(nil, mig("002", "b"))
	e.svc.Register((*typedNil)(nil), mig("003", "c"))
	e.svc.Register(owner("billing"), mig("001", "a"))

	const want = "gas/migrate: register 002: service is nil"

	for _, err := range []error{
		e.svc.RunPending(),
		e.svc.Down(1),
		e.svc.CheckReady(t.Context()),
	} {
		require.ErrorIs(t, err, migrate.ErrNilService)
		require.EqualError(t, err, want)
	}

	assert.False(t, e.exists(t, "a"))
}

// The first registration of a version stays when a later one conflicts.
func TestRegister_FirstStays(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t)
	e.svc.Register(app, mig("001", "a"))
	e.svc.Register(app, mig("001", "b"))

	peer, _ := e.peer(t)
	peer.Register(app, mig("001", "a"))
	require.NoError(t, peer.RunPending())

	err := e.svc.CheckReady(t.Context())
	require.ErrorIs(t, err, migrate.ErrDuplicateVersion)
}

// The Service field of a registered migration is the owner given, not the
// one the migration carries; the owner's name is the row's service.
func TestRegister_OwnerReplacesService(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t)
	m := mig("001", "a")

	m.Service = owner("ignored")

	e.svc.Register(app, m)
	require.NoError(t, e.svc.RunPending())
	assert.Equal(t, "app", rows(t, e.svc)[0].Service)
}

// Directives are read from the leading blank and `--` lines only, white
// space around them allowed; an unknown one after the first other line is
// plain SQL. The down SQL's indented directive is read, and refused on
// SQLite; the up SQL's unknown one is no problem.
func TestRegister_DirectivesLeadingOnly(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t)

	e.svc.Register(app, gas.Migration{
		Version: "001",
		Up:      "CREATE TABLE a (id INT);\n-- gas:whatever\n",
		Down:    "  \n  --   gas:no-transaction  \nDROP TABLE a",
	})
	require.EqualError(t, e.svc.RunPending(), "gas/migrate: register 001 (owner app): "+
		"down SQL: unsupported directive: no-transaction on sqlite")
}

// A directive needs white space after `--`, as a MySQL comment does:
// `--gas:` opens no directive, so an unknown name there is no problem and
// `--gas:no-transaction` leaves the migration in its transaction, where
// SQLite refuses VACUUM.
func TestRegister_DirectiveNeedsSpace(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t)

	e.svc.Register(app, gas.Migration{Version: "001", Up: "--gas:nope\nSELECT 1"})
	e.svc.Register(app, gas.Migration{Version: "002", Up: "--gas:no-transaction\nVACUUM"})

	err := e.svc.RunPending()
	require.ErrorIs(t, err, migrate.ErrMigrationFailed)
	assert.Contains(t, err.Error(), "apply 002 (owner app): migration failed: ")
	assert.Equal(t, []string{"001", "002"}, versions(rows(t, e.svc)))

	e2 := sqliteEnv(t)
	e2.svc.Register(app, gas.Migration{Version: "001", Up: "--\tgas:no-transaction\nVACUUM"})
	require.ErrorIs(t, e2.svc.RunPending(), migrate.ErrUnsupportedDirective)
}

// RegisterFS with a nil file system does not panic: the problem is
// returned and recorded, so the next run reports it too.
func TestRegisterFS_NilFS(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t)

	err := e.svc.RegisterFS(app, nil)
	require.ErrorIs(t, err, migrate.ErrNilFS)
	require.EqualError(t, err, "gas/migrate: register files (owner app): file system is nil")

	require.EqualError(t, e.svc.RunPending(), err.Error())
	require.ErrorIs(t, e.svc.CheckReady(t.Context()), migrate.ErrNilFS)
}

// RegisterFS registers each up file with the down file of its name, in
// any directory, version and description from the name; other files are
// ignored, and an empty down file means no down SQL.
func TestRegisterFS(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t)
	file := func(body string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(body)} }

	require.NoError(t, e.svc.RegisterFS(app, fstest.MapFS{
		"sql/20250216001_create_users.up.sql":    file("CREATE TABLE users (id INT)"),
		"down/20250216001_create_users.down.sql": file("DROP TABLE users"),
		"20250216002.up.sql":                     file("CREATE TABLE plain (id INT)"),
		"20250216002.down.sql":                   file(""),
		"README.md":                              file("# not a migration"),
		"seed.sql":                               file("INSERT INTO users VALUES (1)"),
	}))
	require.NoError(t, e.svc.RunPending())
	assert.Equal(t, []migrate.AppliedMigration{
		{Version: "20250216001", Service: "app", Description: "create users"},
		{Version: "20250216002", Service: "app"},
	}, rows(t, e.svc))

	require.ErrorIs(t, e.svc.Down(1), migrate.ErrNoDownSQL)
}

// A RegisterFS that fails registers nothing.
func TestRegisterFS_FailureRegistersNothing(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t)
	file := func(body string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(body)} }

	err := e.svc.RegisterFS(app, fstest.MapFS{
		"001_a.up.sql":   file("CREATE TABLE a (id INT)"),
		"001_a.down.sql": file("DROP TABLE a"),
		"002_b.up.sql":   file("CREATE TABLE b (id INT)"),
	})
	require.ErrorIs(t, err, migrate.ErrInvalidMigrationFile)

	require.NoError(t, e.svc.RunPending())
	assert.Empty(t, rows(t, e.svc))
}

// A file RegisterFS cannot read fails it, naming the file.
func TestRegisterFS_ReadFails(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t)

	for _, bad := range []string{"001_a.up.sql", "001_a.down.sql"} {
		fsys := fstest.MapFS{
			"001_a.up.sql":   &fstest.MapFile{Data: []byte("SELECT 1")},
			"001_a.down.sql": &fstest.MapFile{Data: []byte("SELECT 1")},
		}
		err := e.svc.RegisterFS(app, unreadable{MapFS: fsys, bad: bad})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "gas/migrate: register files (owner app): read "+bad+": ")
	}
}

// unreadable is a MapFS whose file bad cannot be read.
type unreadable struct {
	fstest.MapFS

	// bad is the file that fails.
	bad string
}

// ReadFile fails for bad.
func (u unreadable) ReadFile(name string) ([]byte, error) {
	if name == u.bad {
		return nil, errBoom
	}

	return u.MapFS.ReadFile(name)
}
