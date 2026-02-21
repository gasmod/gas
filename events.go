package gas

const (
	// SystemServiceClosed is emitted when a service is closed at runtime.
	// EventData contains: "service_name" (string)
	SystemServiceClosed = "gas:service-closed"

	// SystemServiceInitialized is emitted when a service is (re-)initialized at runtime.
	// EventData contains: "service_name" (string)
	SystemServiceInitialized = "gas:service-initialized"

	// SystemAllServicesInitialized is emitted when all services have been successfully initialized.
	SystemAllServicesInitialized = "gas:all-services-initialized"

	// SystemServerShuttingDown is emitted when the server is shutting down.
	SystemServerShuttingDown = "gas:server-shutting-down"

	// AppConfigUpdated is emitted when the app config is updated.
	AppConfigUpdated = "gas:config-updated"
)
