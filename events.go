package gas

// SystemServiceClosed is emitted when a service is closed at runtime.
type SystemServiceClosed struct {
	Event[SystemServiceClosedPayload]
}

// SystemServiceClosedPayload carries the name of the closed service.
type SystemServiceClosedPayload struct {
	ServiceName string
}

// SystemServiceInitialized is emitted when a service is (re-)initialized at runtime.
type SystemServiceInitialized struct {
	Event[SystemServiceInitializedPayload]
}

// SystemServiceInitializedPayload carries the name of the initialized service.
type SystemServiceInitializedPayload struct {
	ServiceName string
}

// SystemAllServicesInitialized is emitted when all services have been successfully initialized.
type SystemAllServicesInitialized struct {
	Event[SystemAllServicesInitializedPayload]
}

// SystemAllServicesInitializedPayload is an empty payload for the all-services-initialized event.
type SystemAllServicesInitializedPayload struct{}

// SystemShuttingDown is emitted when a Worker or App begins its shutdown
// sequence. It fires for both HTTP (App) and non-HTTP (Worker) workloads.
type SystemShuttingDown struct {
	Event[SystemShuttingDownPayload]
}

// SystemShuttingDownPayload is an empty payload for the shutting-down event.
type SystemShuttingDownPayload struct{}

// SystemServerShuttingDown is emitted when the HTTP server is shutting down.
// For code that should run on any shutdown (not just HTTP), subscribe to
// SystemShuttingDown instead.
type SystemServerShuttingDown struct {
	Event[SystemServerShuttingDownPayload]
}

// SystemServerShuttingDownPayload is an empty payload for the server-shutting-down event.
type SystemServerShuttingDownPayload struct{}

// AppConfigUpdated is emitted when the app config is updated.
type AppConfigUpdated struct {
	Event[AppConfigUpdatedPayload]
}

// AppConfigUpdatedPayload carries the updated config.
type AppConfigUpdatedPayload struct {
	Config Config
}
