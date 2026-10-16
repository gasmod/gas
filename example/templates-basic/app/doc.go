// Package app contains the templates-basic example: templates read from the
// ./templates directory and rendered through gas/ui with layouts, partials,
// and static files, plus configuration from a JSON file and request
// logging.
//
// New loads config.json, stopping the process if it cannot, and registers
// the read-only filesystem template store, gas/ui, a singleton logger, a
// scoped RequestLogger, and the Module, which renders the home page at GET /
// and a 404 page for unmatched routes. The static file settings, including
// the strip prefix, come from config.json.
package app
