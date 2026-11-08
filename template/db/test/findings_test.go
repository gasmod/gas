package db_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/template/db"
)

// KI-049: the template migration held two statements, and MySQL without
// multiStatements in the DSN refused it. migrate now sends a MySQL
// migration a statement at a time, so the store's migrations apply with
// gas/database's default MySQL DSN, which sets no parameters.
func TestKI049_MySQLMigrationsWithoutMultiStatements(t *testing.T) {
	t.Parallel()

	e := newEnv(t, mysqlBackend)
	assert.NotContains(t, e.dsn, "multiStatements")

	s := e.store(t)

	applied, err := e.migrations.Applied(t.Context())
	require.NoError(t, err)
	require.Len(t, applied, 2)

	for _, m := range applied {
		assert.Equal(t, db.ServiceName, m.Service)
		assert.False(t, m.Dirty)
	}

	require.NoError(t, s.Register(t.Context(), "a.html", []byte("a")))
}
