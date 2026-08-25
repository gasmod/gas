package db

import "context"

// querier abstracts the sqlc-generated query methods across dialects.
// Unexported — consumers interact with Store, not this interface.
type querier interface {
	getTemplateContent(ctx context.Context, namespace, name string) ([]byte, error)
	listTemplates(ctx context.Context, namespace string) ([]string, error)
	upsertTemplate(ctx context.Context, namespace, name string, content []byte) error
	templateExists(ctx context.Context, namespace, name string) (bool, error)
	deleteTemplate(ctx context.Context, namespace, name string) (int64, error)
}
