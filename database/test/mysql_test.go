package database_test

import (
	"context"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/database"
)

// mysqlService returns an initialized service on the shared MySQL
// container with the DSN changed by edit, closed when the test ends.
func mysqlService(t *testing.T, edit func(*mysql.Config)) *database.Service {
	t.Helper()

	return initialized(t, database.WithConfig(mysqlConfig(t, edit)))
}

// mysqlConfig returns a configuration for the shared MySQL container with
// the DSN changed by edit.
func mysqlConfig(t *testing.T, edit func(*mysql.Config)) *database.Config {
	t.Helper()

	my, err := mysql.ParseDSN(mysqlDSN(t))
	require.NoError(t, err)

	if edit != nil {
		edit(my)
	}

	return fakeConfig(func(s *database.Settings) {
		s.Dialect, s.DSN = database.DialectMySQL, my.FormatDSN()
		s.ConnectTimeout = 5 * time.Second
	})
}

// The dialect mysql connects through go-sql-driver/mysql from the DSN,
// and Driver returns `mysql`.
func TestMySQL_Connects(t *testing.T) {
	t.Parallel()

	s := mysqlService(t, nil)

	assert.Equal(t, "mysql", s.Driver())
	require.NoError(t, s.CheckReady(t.Context()))

	var one int

	require.NoError(t, s.DB().QueryRowContext(t.Context(), "SELECT 1").Scan(&one))
	assert.Equal(t, 1, one)
}

// MultiStatements is not turned on: a DSN without it cannot run two
// statements at once, and one with it can.
func TestMySQL_MultiStatementsAsTheDSNSays(t *testing.T) {
	t.Parallel()

	_, err := mysqlService(t, nil).Exec(t.Context(), "SELECT 1; SELECT 2")
	require.Error(t, err)

	on := mysqlService(t, func(c *mysql.Config) { c.MultiStatements = true })

	_, err = on.Exec(t.Context(), "SELECT 1; SELECT 2")
	require.NoError(t, err)
}

// A wrong password and a missing database get their own reasons, from
// MySQL's error numbers, and neither the error nor the retry warnings
// hold the user, the password, the database name, the host or the port.
func TestMySQL_ServerReasonsNoSecrets(t *testing.T) {
	t.Parallel()

	good, err := mysql.ParseDSN(mysqlDSN(t))
	require.NoError(t, err)

	cases := []struct {
		edit func(*mysql.Config)
		want string
	}{
		{
			func(c *mysql.Config) { c.User, c.Passwd = secretUser, secretPassword },
			"authentication failed",
		},
		// A user without global privileges is denied a database that does
		// not exist, with error 1044.
		{func(c *mysql.Config) { c.DBName = secretDB }, "access denied"},
		// root has them, and gets error 1049.
		{func(c *mysql.Config) { c.User, c.DBName = "root", secretDB }, "database does not exist"},
	}

	for _, c := range cases {
		edit, want := c.edit, c.want
		cfg := mysqlConfig(t, edit)

		cfg.Database.ConnRetries = 1

		s, log := build(t, database.WithConfig(cfg))

		err := s.Init(t.Context())
		require.ErrorIs(t, err, database.ErrConnect)
		require.EqualError(t, err, "gas/database: connect: "+want)

		for _, text := range []string{err.Error(), logText(log)} {
			assertNoSecrets(t, text, cfg.Database.DSN, good.Addr, containerUser, containerDB)
		}
	}
}

// Close then Init restarts the MySQL connection.
func TestMySQL_Restart(t *testing.T) {
	t.Parallel()

	s, _ := build(t, database.WithConfig(mysqlConfig(t, nil)))

	require.NoError(t, s.Init(t.Context()))
	require.NoError(t, s.Close(t.Context()))
	require.NoError(t, s.Init(t.Context()))
	t.Cleanup(func() { require.NoError(t, s.Close(context.Background())) })
	require.NoError(t, s.Ping(t.Context()))
}
