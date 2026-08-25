package db

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"io/fs"

	"github.com/gasmod/gas"
	template "github.com/gasmod/gas/template"
	mydb "github.com/gasmod/gas/template/db/mysql"
	pgdb "github.com/gasmod/gas/template/db/postgres"
	litedb "github.com/gasmod/gas/template/db/sqlite"
	"github.com/gasmod/gas/template/internal/util"
)

//go:embed postgres/20260322235959_create_templates.up.sql
var migrationUpPostgres string

//go:embed postgres/20260322235959_create_templates.down.sql
var migrationDownPostgres string

//go:embed mysql/20260322235959_create_templates.up.sql
var migrationUpMySQL string

//go:embed mysql/20260322235959_create_templates.down.sql
var migrationDownMySQL string

//go:embed sqlite/20260322235959_create_templates.up.sql
var migrationUpSQLite string

//go:embed sqlite/20260322235959_create_templates.down.sql
var migrationDownSQLite string

// Store is a database-backed template store implementing gas.TemplateProvider.
// It delegates to sqlc-generated queries, scoped to a namespace so multiple
// Store instances can share a single table.
type Store struct {
	db           gas.DatabaseProvider
	logger       gas.Logger
	migrationMgr gas.MigrationManager
	q            querier
	namespace    string
}

var _ gas.TemplateProvider = (*Store)(nil)
var _ gas.Service = (*Store)(nil)

// Option configures a Store.
type Option func(*Store)

// WithNamespace sets the namespace for this store instance.
// Defaults to "default" if not specified.
func WithNamespace(ns string) Option {
	return func(s *Store) { s.namespace = ns }
}

// NewStore returns a DI-injectable constructor for Store.
func NewStore(opts ...Option) func(gas.DatabaseProvider, gas.Logger, gas.MigrationManager) *Store {
	return func(db gas.DatabaseProvider, logger gas.Logger, mgr gas.MigrationManager) *Store {
		s := &Store{
			db:           db,
			logger:       logger,
			migrationMgr: mgr,
			namespace:    "default",
		}
		for _, opt := range opts {
			opt(s)
		}
		return s
	}
}

// Name implements gas.Service.
func (s *Store) Name() string { return "gas/template/db" }

// Init implements gas.Service. It registers the templates table migration
// and selects the correct sqlc adapter based on the configured database driver.
func (s *Store) Init() error {
	sqlDB := s.db.DB()

	var up, down string

	switch s.db.Driver() {
	case "postgres", "pgx":
		up = migrationUpPostgres
		down = migrationDownPostgres
		s.q = newPostgresAdapter(pgdb.New(sqlDB))
	case "mysql":
		up = migrationUpMySQL
		down = migrationDownMySQL
		s.q = newMySQLAdapter(mydb.New(sqlDB))
	case "sqlite", "sqlite3":
		up = migrationUpSQLite
		down = migrationDownSQLite
		s.q = newSQLiteAdapter(litedb.New(sqlDB))
	default:
		return fmt.Errorf("gas/template/db: unsupported driver: %q", s.db.Driver())
	}

	s.migrationMgr.Register(s.Name(), gas.Migration{
		Version:     "20260322235959",
		Description: "create templates table",
		Up:          up,
		Down:        down,
	})

	s.logger.Info("template store initialized").
		Str("driver", s.db.Driver()).
		Str("namespace", s.namespace).
		Send()

	return nil
}

// Close implements gas.Service.
func (s *Store) Close() error { return nil }

// Get returns the raw template content by name.
func (s *Store) Get(ctx context.Context, name string) ([]byte, error) {
	content, err := s.q.getTemplateContent(ctx, s.namespace, name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, template.ErrTemplateNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("template: get %q: %w", name, err)
	}
	return content, nil
}

// List returns all available template names in sorted order.
func (s *Store) List(ctx context.Context) ([]string, error) {
	names, err := s.q.listTemplates(ctx, s.namespace)
	if err != nil {
		return nil, fmt.Errorf("template: list: %w", err)
	}
	return names, nil
}

// Register adds or replaces a template by name.
func (s *Store) Register(ctx context.Context, name string, content []byte) error {
	if err := s.q.upsertTemplate(ctx, s.namespace, name, content); err != nil {
		return fmt.Errorf("template: register %q: %w", name, err)
	}
	return nil
}

// RegisterFS walks an fs.FS and upserts every .html file found.
func (s *Store) RegisterFS(ctx context.Context, fsys fs.FS) error {
	if err := util.RegisterFS(ctx, s, fsys, ".html"); err != nil {
		return fmt.Errorf("template: register fs: %w", err)
	}
	return nil
}

// Exists checks whether a template with the given name exists.
func (s *Store) Exists(name string) (bool, error) {
	exists, err := s.q.templateExists(context.Background(), s.namespace, name)
	if err != nil {
		return false, fmt.Errorf("template: exists %q: %w", name, err)
	}
	return exists, nil
}

// Delete removes a template by name. Returns template.ErrTemplateNotFound
// if the template does not exist.
func (s *Store) Delete(name string) error {
	affected, err := s.q.deleteTemplate(context.Background(), s.namespace, name)
	if err != nil {
		return fmt.Errorf("template: delete %q: %w", name, err)
	}
	if affected == 0 {
		return template.ErrTemplateNotFound
	}
	return nil
}
