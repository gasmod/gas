// Package migratetest holds a mock of gas.MigrationManager,
// [MockMigrationManager], for tests of services that register migrations.
//
// # Usage
//
// The mock records each call, and returns what the function in the field
// named after the method returns:
//
//	m := &migratetest.MockMigrationManager{
//		RunPendingFn: func() error { return errBoom },
//	}
//	err := m.RunPending()
//	// err is errBoom, and m.CallCount("RunPending") is 1
//
// # Defaults
//
// With a field nil, its method succeeds and does nothing: Name returns
// migrate.ServiceName, and Init, Close, RegisterFS, RunPending and Down
// return nil. Register and RegisterSlice return nothing.
//
// # Calls
//
// Each method appends a [Call] with its name and arguments, the context
// of Init and Close included, before it calls its field. Calls returns a
// copy of them in the order they were made, CallCount counts those of one
// method, and Reset forgets them all.
//
// The methods may be called from several goroutines at once, as the calls
// are kept under a lock, which Calls, CallCount and Reset take too.
// Setting a field while a method runs is a data race.
package migratetest
