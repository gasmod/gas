package testutil

import (
	"context"
	"database/sql"
	"os"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	tcmysql "github.com/testcontainers/testcontainers-go/modules/mysql"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/gasmod/gas/database"
	"github.com/gasmod/gas/migrate"
)

// MySQLEnv is the variable that, when set to any non-empty value, adds the
// MySQL backend to ForEachBackend and lets MySQLBackend and SetupMySQL run. It is opt-in so
// that no machine starts, or pulls, a MySQL image unasked.
const MySQLEnv = "GAS_TEST_MYSQL"

// MySQLImage is the image the MySQL backend runs. It is pinned so that a
// machine that already has it never pulls another.
const MySQLImage = "mysql:8.4"

// mysqlDriver is the driver name the MySQL database service reports, the
// name the stores pick their dialect by.
const mysqlDriver = "mysql"

// mysqlEnabled reports whether MySQLEnv is set.
func mysqlEnabled() bool {
	return os.Getenv(MySQLEnv) != ""
}

// MySQLBackend returns a backend on a fresh MySQL container, skipping the
// test under -short, when MySQLEnv is not set, or without Docker. Tests
// that only hold on MySQL use it directly.
func MySQLBackend(tb testing.TB) Backend {
	tb.Helper()

	if testing.Short() {
		tb.Skip("skipping MySQL test in short mode")
	}

	my := SetupMySQL(tb)

	return Backend{DB: my.Provider(), Migrations: my.MigrationManager(), Name: mysqlDriver}
}

// MySQLContainer is a running MySQL container with real database and
// migrate services bound to it.
type MySQLContainer struct {
	// Container is the running container.
	Container *tcmysql.MySQLContainer
	// DB is the database service's connection pool.
	DB *sql.DB
	// dbSvc is the database service.
	dbSvc *database.Service
	// migSvc is the migration service.
	migSvc *migrate.Service
	// ConnStr is the connection string the database service uses, with
	// the parameters described in the package documentation.
	ConnStr string
}

// SetupMySQL starts a MySQLImage container for the test, terminated when
// it ends, and returns real database and migrate services bound to it. It
// skips the test when MySQLEnv is not set, and skips or fails it without
// Docker, as described in the package documentation.
func SetupMySQL(tb testing.TB) *MySQLContainer {
	tb.Helper()

	if !mysqlEnabled() {
		tb.Skipf("skipping MySQL test: %s is not set", MySQLEnv)
	}

	requireDocker(tb)

	ctx := context.Background()
	myContainer := runMySQL(ctx, tb)

	// parseTime makes the driver scan DATETIME into time.Time, which the
	// stores and the migrate tracking table need; multiStatements lets a
	// migration file hold several statements, which migrate runs in one
	// ExecContext.
	connStr, err := myContainer.ConnectionString(ctx, "parseTime=true", "multiStatements=true")
	if err != nil {
		tb.Fatalf(
			"failed to get connection string: %v (%s)",
			err,
			containerStatus(ctx, myContainer),
		)
	}

	// The service opens the DSN with the mysql dialect, which keeps
	// multiStatements as the DSN sets it.
	dbSvc := newDatabaseService(tb, func(s *database.Settings) {
		s.Dialect = database.DialectMySQL
		s.DSN = connStr
		s.ConnRetries = 5
		s.ConnRetryInterval = connRetryInterval
	})
	migSvc := newMigrateService(tb, dbSvc)

	return &MySQLContainer{
		Container: myContainer,
		ConnStr:   connStr,
		DB:        dbSvc.DB(),
		dbSvc:     dbSvc,
		migSvc:    migSvc,
	}
}

// runMySQL starts a MySQLImage container, terminated when the test ends,
// failing the test when it does not start.
func runMySQL(ctx context.Context, tb testing.TB) *tcmysql.MySQLContainer {
	tb.Helper()

	myContainer, err := tcmysql.Run(ctx,
		MySQLImage,
		tcmysql.WithDatabase("testdb"),
		tcmysql.WithUsername("testuser"),
		tcmysql.WithPassword("testpass"),
		// The module waits for the server's ready log line; the port
		// mapping can lag it, as with PostgreSQL, so wait for the port
		// to be served on the host too.
		testcontainers.WithAdditionalWaitStrategy(wait.ForListeningPort("3306/tcp")),
		// Each server with native AIO takes a share of the host's
		// fs.aio-max-nr, and parallel tests ran out of it ("Cannot
		// initialize AIO sub-system"), so the server uses its own I/O
		// threads. The entrypoint passes these flags to mysqld.
		testcontainers.WithCmdArgs("--innodb-use-native-aio=OFF"),
		// The data directory lives in memory: the database is thrown
		// away with the container, and writes do not wait for a disk.
		testcontainers.WithTmpfs(map[string]string{"/var/lib/mysql": "rw"}),
	)
	if err != nil {
		tb.Fatalf("failed to start mysql container: %v", err)
	}

	tb.Cleanup(func() {
		tErr := myContainer.Terminate(ctx)
		if tErr != nil {
			tb.Logf("failed to terminate mysql container: %v", tErr)
		}
	})

	return myContainer
}

// Provider returns the container's database service.
func (mc *MySQLContainer) Provider() *database.Service {
	return mc.dbSvc
}

// MigrationManager returns the container's migration service.
func (mc *MySQLContainer) MigrationManager() *migrate.Service {
	return mc.migSvc
}
