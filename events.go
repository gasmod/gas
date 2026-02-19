package gas

const (
	// SystemModuleClosed is emitted when a module is closed at runtime.
	// EventData contains: "module_name" (string)
	SystemModuleClosed = "gas:module-closed"

	// SystemModuleInitialized is emitted when a module is (re-)initialized at runtime.
	// EventData contains: "module_name" (string)
	SystemModuleInitialized = "gas:module-initialized"

	// SystemAllModulesInitialized is emitted when all modules have been successfully initialized.
	SystemAllModulesInitialized = "gas:all-modules-initialized"

	// SystemServerShuttingDown is emitted when the server is shutting down.
	SystemServerShuttingDown = "gas:server-shutting-down"
)
