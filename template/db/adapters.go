package db

import (
	"context"

	mydb "github.com/gasmod/gas/template/db/mysql"
	pgdb "github.com/gasmod/gas/template/db/postgres"
	litedb "github.com/gasmod/gas/template/db/sqlite"
)

type postgresAdapter struct {
	q *pgdb.Queries
}

func newPostgresAdapter(q *pgdb.Queries) *postgresAdapter {
	return &postgresAdapter{q: q}
}

func (a *postgresAdapter) getTemplateContent(ctx context.Context, namespace, name string) ([]byte, error) {
	//nolint:wrapcheck // wrapped in the Store
	return a.q.GetTemplateContent(ctx, &pgdb.GetTemplateContentParams{
		Namespace: namespace,
		Name:      name,
	})
}

func (a *postgresAdapter) listTemplates(ctx context.Context, namespace string) ([]string, error) {
	//nolint:wrapcheck // wrapped in the Store
	return a.q.ListTemplates(ctx, namespace)
}

func (a *postgresAdapter) upsertTemplate(ctx context.Context, namespace, name string, content []byte) error {
	//nolint:wrapcheck // wrapped in the Store
	return a.q.UpsertTemplate(ctx, &pgdb.UpsertTemplateParams{
		Namespace: namespace,
		Name:      name,
		Content:   content,
	})
}

func (a *postgresAdapter) templateExists(ctx context.Context, namespace, name string) (bool, error) {
	//nolint:wrapcheck // wrapped in the Store
	return a.q.TemplateExists(ctx, &pgdb.TemplateExistsParams{
		Namespace: namespace,
		Name:      name,
	})
}

func (a *postgresAdapter) deleteTemplate(ctx context.Context, namespace, name string) (int64, error) {
	//nolint:wrapcheck // wrapped in the Store
	return a.q.DeleteTemplate(ctx, &pgdb.DeleteTemplateParams{
		Namespace: namespace,
		Name:      name,
	})
}

type mysqlAdapter struct {
	q *mydb.Queries
}

func newMySQLAdapter(q *mydb.Queries) *mysqlAdapter {
	return &mysqlAdapter{q: q}
}

func (a *mysqlAdapter) getTemplateContent(ctx context.Context, namespace, name string) ([]byte, error) {
	//nolint:wrapcheck // wrapped in the Store
	return a.q.GetTemplateContent(ctx, &mydb.GetTemplateContentParams{
		Namespace: namespace,
		Name:      name,
	})
}

func (a *mysqlAdapter) listTemplates(ctx context.Context, namespace string) ([]string, error) {
	//nolint:wrapcheck // wrapped in the Store
	return a.q.ListTemplates(ctx, namespace)
}

func (a *mysqlAdapter) upsertTemplate(ctx context.Context, namespace, name string, content []byte) error {
	//nolint:wrapcheck // wrapped in the Store
	return a.q.UpsertTemplate(ctx, &mydb.UpsertTemplateParams{
		Namespace: namespace,
		Name:      name,
		Content:   content,
	})
}

func (a *mysqlAdapter) templateExists(ctx context.Context, namespace, name string) (bool, error) {
	//nolint:wrapcheck // wrapped in the Store
	return a.q.TemplateExists(ctx, &mydb.TemplateExistsParams{
		Namespace: namespace,
		Name:      name,
	})
}

func (a *mysqlAdapter) deleteTemplate(ctx context.Context, namespace, name string) (int64, error) {
	//nolint:wrapcheck // wrapped in the Store
	return a.q.DeleteTemplate(ctx, &mydb.DeleteTemplateParams{
		Namespace: namespace,
		Name:      name,
	})
}

type sqliteAdapter struct {
	q *litedb.Queries
}

func newSQLiteAdapter(q *litedb.Queries) *sqliteAdapter {
	return &sqliteAdapter{q: q}
}

func (a *sqliteAdapter) getTemplateContent(ctx context.Context, namespace, name string) ([]byte, error) {
	//nolint:wrapcheck // wrapped in the Store
	return a.q.GetTemplateContent(ctx, &litedb.GetTemplateContentParams{
		Namespace: namespace,
		Name:      name,
	})
}

func (a *sqliteAdapter) listTemplates(ctx context.Context, namespace string) ([]string, error) {
	//nolint:wrapcheck // wrapped in the Store
	return a.q.ListTemplates(ctx, namespace)
}

func (a *sqliteAdapter) upsertTemplate(ctx context.Context, namespace, name string, content []byte) error {
	//nolint:wrapcheck // wrapped in the Store
	return a.q.UpsertTemplate(ctx, &litedb.UpsertTemplateParams{
		Namespace: namespace,
		Name:      name,
		Content:   content,
	})
}

func (a *sqliteAdapter) templateExists(ctx context.Context, namespace, name string) (bool, error) {
	count, err := a.q.TemplateExists(ctx, &litedb.TemplateExistsParams{
		Namespace: namespace,
		Name:      name,
	})
	//nolint:wrapcheck // wrapped in the Store
	return count > 0, err
}

func (a *sqliteAdapter) deleteTemplate(ctx context.Context, namespace, name string) (int64, error) {
	//nolint:wrapcheck // wrapped in the Store
	return a.q.DeleteTemplate(ctx, &litedb.DeleteTemplateParams{
		Namespace: namespace,
		Name:      name,
	})
}
