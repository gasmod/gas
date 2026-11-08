package database

import (
	"database/sql/driver"

	"github.com/go-sql-driver/mysql"

	"github.com/gasmod/gas"
)

// mysqlDriverMessage is what the service logs, at warn level, for each
// message go-sql-driver/mysql logs.
const mysqlDriverMessage = "mysql driver reported an error"

// mysqlConnector parses dsn as go-sql-driver/mysql does, turns ParseTime
// on, routes the driver's log to logger, and returns a connector for it.
// MultiStatements is left as the DSN sets it. A failure is ErrInvalidDSN,
// without the parser's error, which can quote parameter values.
func mysqlConnector(dsn string, logger gas.Logger) (driver.Connector, error) {
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return nil, invalidDSN()
	}

	cfg.ParseTime = true
	cfg.Logger = mysqlLogger{logger: logger}

	c, err := mysql.NewConnector(cfg)
	if err != nil {
		return nil, invalidDSN()
	}

	return c, nil
}

// mysqlLogger is the mysql.Logger of the connectors the service makes. The
// driver's default logger writes its messages, network errors with the
// server's address among them, to stderr; this one logs a fixed message
// instead, without the driver's text.
type mysqlLogger struct {
	// logger is the service's logger.
	logger gas.Logger
}

// Print logs mysqlDriverMessage at warn level, dropping v.
func (l mysqlLogger) Print(...any) {
	l.logger.Warn(mysqlDriverMessage).Send()
}
