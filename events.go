package gas

// SystemServiceClosed is emitted when a service is closed at runtime.
var SystemServiceClosed = Event[SystemServiceClosedPayload]{Name: "gas:service-closed"}

// SystemServiceClosedPayload carries the name of the closed service.
type SystemServiceClosedPayload struct {
	ServiceName string
}

// SystemServiceInitialized is emitted when a service is (re-)initialized at runtime.
var SystemServiceInitialized = Event[SystemServiceInitializedPayload]{Name: "gas:service-initialized"}

// SystemServiceInitializedPayload carries the name of the initialized service.
type SystemServiceInitializedPayload struct {
	ServiceName string
}

// SystemAllServicesInitialized is emitted when all services have been successfully initialized.
var SystemAllServicesInitialized = Event[SystemAllServicesInitializedPayload]{Name: "gas:all-services-initialized"}

// SystemAllServicesInitializedPayload is an empty payload for the all-services-initialized event.
type SystemAllServicesInitializedPayload struct{}

// SystemServerShuttingDown is emitted when the server is shutting down.
var SystemServerShuttingDown = Event[SystemServerShuttingDownPayload]{Name: "gas:server-shutting-down"}

// SystemServerShuttingDownPayload is an empty payload for the server-shutting-down event.
type SystemServerShuttingDownPayload struct{}

// AppConfigUpdated is emitted when the app config is updated.
var AppConfigUpdated = Event[AppConfigUpdatedPayload]{Name: "gas:config-updated"}

// AppConfigUpdatedPayload carries the updated config.
type AppConfigUpdatedPayload struct {
	Config Config
}
