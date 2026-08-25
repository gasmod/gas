package app

import "github.com/gasmod/gas"

// RequestLogger is a distinct type for the request-scoped logger so it can be
// registered alongside the singleton gas.Logger without colliding with it in
// the container.
type RequestLogger interface {
	gas.Logger
}
