package config

import "context"

// Validator checks a struct after Bind fills it. *validator.Validate from
// github.com/go-playground/validator/v10 implements it, and New uses one
// unless WithValidator passes another.
type Validator interface {
	StructCtx(ctx context.Context, s any) error
}
