package database_test

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcmysql "github.com/testcontainers/testcontainers-go/modules/mysql"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// The images the tests run, pinned so that a machine that has them never
// pulls another.
const (
	// postgresImage is the PostgreSQL image.
	postgresImage = "postgres:16-alpine"
	// mysqlImage is the MySQL image.
	mysqlImage = "mysql:8.4"
)

// requireDockerEnv, when set to any non-empty value, makes the container
// tests fail instead of skipping when Docker is unavailable.
const requireDockerEnv = "GAS_TEST_REQUIRE_DOCKER"

// mysqlEnv, when set to any non-empty value, runs the MySQL tests, which
// are opt-in so that no machine starts a MySQL image unasked.
const mysqlEnv = "GAS_TEST_MYSQL"

// The credentials of the test containers.
const (
	containerUser     = "testuser"
	containerPassword = "testpass"
	containerDB       = "testdb"
)

// shared is a container the tests share, started on first use.
type shared struct {
	// err is the error of starting it.
	err error
	// terminate stops it, or is nil.
	terminate func()
	// dsn is its DSN.
	dsn string
	// once starts it once.
	once sync.Once
}

// The shared containers, terminated by TestMain.
var (
	// pgShared is the PostgreSQL container.
	pgShared shared
	// myShared is the MySQL container.
	myShared shared
)

// terminateContainers stops the shared containers that started.
func terminateContainers() {
	for _, c := range []*shared{&pgShared, &myShared} {
		if c.terminate != nil {
			c.terminate()
		}
	}
}

// requireDocker skips the test when Docker is unavailable, or fails it
// when requireDockerEnv is set.
func requireDocker(t *testing.T) {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping container test in short mode")
	}

	if os.Getenv(requireDockerEnv) == "" {
		testcontainers.SkipIfProviderIsNotHealthy(t)

		return
	}

	p, err := testcontainers.ProviderDocker.GetProvider()
	require.NoError(t, err, "%s is set but Docker is unavailable", requireDockerEnv)

	defer func() { require.NoError(t, p.Close()) }()

	require.NoError(t, p.Health(context.Background()),
		"%s is set but Docker is unavailable", requireDockerEnv)
}

// postgresDSN returns the DSN of the shared PostgreSQL container, which
// it starts on first use, skipping the test without Docker.
func postgresDSN(t *testing.T) string {
	t.Helper()
	requireDocker(t)

	pgShared.once.Do(func() {
		ctx := context.Background()

		ctr, err := postgres.Run(ctx, postgresImage,
			postgres.WithDatabase(containerDB),
			postgres.WithUsername(containerUser),
			postgres.WithPassword(containerPassword),
			postgres.BasicWaitStrategies(),
		)
		if err != nil {
			pgShared.err = fmt.Errorf("start postgres: %w", err)

			return
		}

		pgShared.terminate = func() { _ = ctr.Terminate(context.Background()) }
		pgShared.dsn, pgShared.err = ctr.ConnectionString(ctx, "sslmode=disable")
	})

	require.NoError(t, pgShared.err)

	return pgShared.dsn
}

// mysqlDSN returns the DSN of the shared MySQL container, which it starts
// on first use, skipping the test unless mysqlEnv is set, and without
// Docker. The DSN sets no parameters.
func mysqlDSN(t *testing.T) string {
	t.Helper()

	if os.Getenv(mysqlEnv) == "" {
		t.Skipf("skipping MySQL test: %s is not set", mysqlEnv)
	}

	requireDocker(t)

	myShared.once.Do(func() {
		ctx := context.Background()

		ctr, err := tcmysql.Run(ctx, mysqlImage,
			tcmysql.WithDatabase(containerDB),
			tcmysql.WithUsername(containerUser),
			tcmysql.WithPassword(containerPassword),
			// The port mapping can lag the ready log line.
			testcontainers.WithAdditionalWaitStrategy(wait.ForListeningPort("3306/tcp")),
			// Native AIO takes a share of the host's fs.aio-max-nr.
			testcontainers.WithCmdArgs("--innodb-use-native-aio=OFF"),
			testcontainers.WithTmpfs(map[string]string{"/var/lib/mysql": "rw"}),
		)
		if err != nil {
			myShared.err = fmt.Errorf("start mysql: %w", err)

			return
		}

		myShared.terminate = func() { _ = ctr.Terminate(context.Background()) }
		myShared.dsn, myShared.err = ctr.ConnectionString(ctx)
	})

	require.NoError(t, myShared.err)

	return myShared.dsn
}
