package app

import (
	"errors"
	"net/http"

	"github.com/gasmod/gas"
)

// statusError is checked by the error handler via interface assertion.
// Any error type with a StatusCode() method controls the HTTP response.
type statusError interface {
	error
	StatusCode() int
}

// errorHandler converts handler errors into JSON responses. If the error
// implements statusError, its status and message are used. Otherwise, a
// generic 500 is returned.
func errorHandler(ctx gas.Context, err error) {
	status := http.StatusInternalServerError
	message := "internal server error"

	// errors.As rather than a type assertion, so a statusError still controls
	// the response after being wrapped on its way up.
	var se statusError
	if errors.As(err, &se) {
		status = se.StatusCode()
		message = se.Error()
	}

	_ = ctx.JSON(status, map[string]string{"error": message})
}
