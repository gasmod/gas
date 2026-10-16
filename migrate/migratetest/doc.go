// Package migratetest provides a mock implementation of gas.MigrationManager
// for use in tests. The mock records all calls and allows configuring
// per-method behavior via function fields.
//
//	mock := &migratetest.MockMigrationManager{}
//	mock.RunPendingFn = func() error {
//	    return nil
//	}
package migratetest
