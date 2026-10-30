package migrate_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/migrate"
)

// KI-048: the tracking table was read with applied_at, which the MySQL
// driver returns as bytes without parseTime, so the second RunPending
// on a provider other than gas/database failed to scan it. applied_at is
// never read now: a raw MySQL provider without parseTime runs, reads and
// checks the table as often as it likes.
func TestKI048_MySQLWithoutParseTime(t *testing.T) {
	t.Parallel()

	p := openRaw(t, "mysql", mysqlDSN(t), "mysql")
	svc, _ := started(t, p)

	svc.Register(app, mig("001", "a"))
	require.NoError(t, svc.RunPending())

	svc.Register(app, mig("002", "b"))
	require.NoError(t, svc.RunPending())
	require.NoError(t, svc.RunPending())
	require.NoError(t, svc.CheckReady(t.Context()))
	assert.Equal(t, []string{"001", "002"}, versions(rows(t, svc)))
	require.NoError(t, svc.Down(2))
}

// KI-057: services register their migrations in Init, and each restart
// of a service added another copy. An identical registration is now
// dropped when it is made, and the migration still runs once.
func TestKI057_RestartRegistersOnce(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t)

	for range 3 {
		e.svc.RegisterSlice(app, []gas.Migration{mig("001", "a"), mig("002", "b")})
		require.NoError(t, e.svc.Close(t.Context()))
		require.NoError(t, e.svc.Init(t.Context()))
	}

	require.NoError(t, e.svc.RunPending())
	assert.Equal(t, []string{"001", "002"}, versions(rows(t, e.svc)))

	applied := 0

	for _, en := range e.log.entries() {
		if en.msg == "migration applied" {
			applied++
		}
	}

	assert.Equal(t, 2, applied)
}

// KI-049: template/db's MySQL migration holds a CREATE TABLE and a
// CREATE INDEX, which failed through gas/database's default DSN, since
// migrate sent a migration as one query and the DSN allows one
// statement. migrate now splits MySQL migrations, so it applies.
func TestKI049_MySQLMultiStatement(t *testing.T) {
	t.Parallel()

	e := newEnv(t, mysqlBackend)

	e.svc.Register(owner("gas/template/db"), gas.Migration{
		Version:     "20260322235959",
		Description: "create templates table",
		Up: `CREATE TABLE __gas_templates (
    namespace VARCHAR(255) NOT NULL,
    name      VARCHAR(255) NOT NULL,
    body      TEXT         NOT NULL,
    PRIMARY KEY (namespace, name)
);

CREATE INDEX idx___gas_templates_namespace ON __gas_templates (namespace);
`,
		Down: "DROP TABLE __gas_templates;",
	})

	require.NoError(t, e.svc.RunPending())
	assert.True(t, e.exists(t, "__gas_templates"))
}

// Survey P3: two runners applying one migration at once wedged the app:
// the loser's failure marked the winner's clean row dirty. The lock
// serializes the runners, and a dirty mark never touches a row that
// exists, so the row stays clean (TestConcurrency_TwoRunnersOneApplies
// and TestRunPending_DirtyMarkKeepsCleanRow pin both halves); here the
// loser finds nothing to do and the app is ready.
func TestP3_NoWedge(t *testing.T) {
	t.Parallel()

	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()

			e := newEnv(t, b)
			peer, _ := e.peer(t)

			for _, s := range []*migrate.Service{e.svc, peer} {
				s.Register(app, mig("003", "p3"))
			}

			errs := make(chan error, 2)

			go func() { errs <- e.svc.RunPending() }()
			go func() { errs <- peer.RunPending() }()

			require.NoError(t, <-errs)
			require.NoError(t, <-errs)
			require.NoError(t, e.svc.CheckReady(t.Context()))
			assert.Equal(
				t,
				[]migrate.AppliedMigration{
					{Version: "003", Service: "app", Description: "create p3"},
				},
				rows(t, e.svc),
			)
		})
	}
}
