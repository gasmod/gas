// Package main shows the unified error shape.
package main

// #region imports
import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/gasmod/gas"
)

// #endregion imports

// #region handler
// Handlers return error. Return a gas.Error and core renders it at the right
// status in a stable JSON shape, so applications do not each invent their own.
func show(ctx gas.Context, db gas.DatabaseProvider) error {
	user, err := find(ctx, db, ctx.Param("id"))
	if errors.Is(err, sql.ErrNoRows) {
		// WithCause keeps the real error reachable through errors.Is and puts
		// it in the log. It is never serialized into the response.
		return gas.NotFound("user not found").WithCause(err)
	}
	if err != nil {
		return err // renders as a generic 500; the real error goes to the log
	}
	return ctx.JSON(http.StatusOK, user)
}

// #endregion handler

// #region binding
// Binding produces the same shape for free. A malformed body is a 400, and a
// struct that fails validation is a 422 with per-field detail, named by the
// JSON tag the client actually sent.
type CreateUser struct {
	Email string `json:"email" validate:"required,email"`
	Name  string `json:"name"  validate:"required"`
}

func create(ctx gas.Context) error {
	var req CreateUser
	if err := ctx.BindJSON(&req); err != nil {
		return err
	}
	return ctx.JSON(http.StatusCreated, req)
}

// #endregion binding

// #region detail
// Build a richer error when the client needs more than a message.
func conflict(email string) error {
	return gas.Conflict("email already registered").
		WithField("email", "unique", "that address is already in use").
		WithDetail("suggestion", email+"+1@example.com")
}

// #endregion detail

// #region classify
// AsError classifies an error coming back from a service call.
func classify(err error) int {
	if gasErr, ok := gas.AsError(err); ok {
		return gasErr.Status
	}
	return http.StatusInternalServerError
}

// #endregion classify

// #region custom-handler
// A custom handler owns the whole response. gas.WriteError renders the unified
// shape from any http.Handler, without logging or touching the request scope,
// so it is safe in middleware that runs before the scope exists.
func htmlOrJSON() gas.AppOption {
	return gas.WithErrorHandler(func(ctx gas.Context, err error) {
		if gas.WantsJSON(ctx.Request()) {
			_ = ctx.Error(err)
			return
		}
		_ = ctx.HTML(http.StatusInternalServerError, "<h1>Something went wrong</h1>")
	})
}

// #endregion custom-handler

func find(_ gas.Context, _ gas.DatabaseProvider, _ string) (any, error) { return nil, nil }

func main() {}
