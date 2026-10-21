// Package main shows the unified error shape and the error handler.
package main

import (
	"database/sql"
	"errors"
	"log"
	"net/http"

	"github.com/gasmod/gas"
)

// User is what the handlers below return.
type User struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

// Users is a stand-in for a store backed by a database.
type Users struct{}

func (u *Users) Find(_ gas.Context, id string) (*User, error) {
	if id == "" {
		return nil, sql.ErrNoRows
	}
	return &User{ID: id}, nil
}

// #region handler
// Return a *gas.Error and the error handler renders it at its status in the
// unified envelope.
func show(ctx gas.Context, users *Users) error {
	user, err := users.Find(ctx, ctx.Param("id"))
	if errors.Is(err, sql.ErrNoRows) {
		// WithCause keeps the real error for logs and errors.Is. It is never
		// written to the response.
		return gas.NotFound("user not found").WithCause(err)
	}
	if err != nil {
		return err // not a *gas.Error: rendered as a generic 500
	}
	return ctx.JSON(http.StatusOK, user)
}

// #endregion handler

// #region context
// The Context carries the same constructors. They return error, so they end a
// handler in one line; the Err variants attach a cause.
func showViaContext(ctx gas.Context, users *Users) error {
	user, err := users.Find(ctx, ctx.Param("id"))
	if errors.Is(err, sql.ErrNoRows) {
		return ctx.NotFoundErr(err, "user not found")
	}
	if err != nil {
		return ctx.InternalErr(err, "could not load user")
	}
	return ctx.JSON(http.StatusOK, user)
}

// #endregion context

// #region custom
// NewError takes any status and code. Applications define their own codes.
const CodeQuotaExceeded = "quota_exceeded"

func quota() error {
	return gas.NewError(http.StatusPaymentRequired, CodeQuotaExceeded, "monthly quota used up")
}

// #endregion custom

// #region detail
// Fields and details travel in the response body.
func conflict(email string) error {
	return gas.Conflict("email already registered").
		WithField("email", "unique", "that address is already in use").
		WithDetail("suggestion", email+"+1@example.com")
}

// #endregion detail

// #region binding
// Binding produces the same shape. A body that fails validation is a 422 with
// one entry in fields per failed rule.
type CreateUser struct {
	Email string `json:"email" validate:"required,email"`
	Name  string `json:"name"  validate:"required,max=64"`
}

func create(ctx gas.Context) error {
	var req CreateUser
	if err := ctx.BindJSON(&req); err != nil {
		return err
	}
	return ctx.JSON(http.StatusCreated, req)
}

// #endregion binding

// #region classify
// AsError finds a *gas.Error anywhere in an error chain.
func statusOf(err error) int {
	if gasErr, ok := gas.AsError(err); ok {
		return gasErr.Status
	}
	return http.StatusInternalServerError
}

// #endregion classify

// #region handler-option
// A custom error handler owns the whole response: nothing is written unless it
// writes it, and nothing is logged unless it logs.
func errorPages() gas.AppOption {
	return gas.WithErrorHandler(func(ctx gas.Context, err error) {
		log.Printf("%s %s: %v", ctx.Request().Method, ctx.Request().URL.Path, err)

		if gas.WantsJSON(ctx.Request()) {
			if wErr := ctx.WriteError(err); wErr != nil {
				log.Printf("write error response: %v", wErr)
			}
			return
		}

		status := http.StatusInternalServerError
		if gasErr, ok := gas.AsError(err); ok {
			status = gasErr.Status
		}
		if wErr := ctx.HTML(status, "<h1>Something went wrong</h1>"); wErr != nil {
			log.Printf("write error page: %v", wErr)
		}
	})
}

// #endregion handler-option

// #region json-route
// In an app whose error handler renders HTML, a JSON API route can still
// answer with the JSON envelope.
func apiShow(ctx gas.Context, users *Users) error {
	user, err := users.Find(ctx, ctx.Param("id"))
	if err != nil {
		return ctx.WriteErrorJSON(gas.NotFound("user not found").WithCause(err))
	}
	return ctx.JSON(http.StatusOK, user)
}

// #endregion json-route

// #region write-error
// WriteError works from any http.Handler. It reads only w and r, and does not
// log, so it is safe before the request scope exists.
func requireAPIKey(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") == "" {
			if err := gas.WriteError(w, r, gas.Unauthorized("missing API key")); err != nil {
				log.Printf("write error response: %v", err)
			}
			return
		}
		next.ServeHTTP(w, r)
	})
}

// #endregion write-error

func main() {
	app := gas.NewApp(
		errorPages(),
		gas.WithSingletonService[*Users](func() *Users { return &Users{} }),
	)
	r := app.Router()
	r.Get(nil, "/users/{id}", show)
	r.Get(nil, "/v2/users/{id}", showViaContext)
	r.Get(nil, "/api/users/{id}", apiShow, gas.MiddlewareFunc(requireAPIKey))
	r.Post(nil, "/users", create)

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
