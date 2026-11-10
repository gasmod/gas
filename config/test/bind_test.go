package config_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-playground/validator/v10"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/config"
)

// Bind fills the fields that have keys, matching names without regard to
// case and converting values to the fields' types, and leaves the others
// as they are, so a struct filled with defaults keeps them.
func TestBind_Fields(t *testing.T) {
	t.Parallel()

	c := newConfig(t, m{
		"server": m{"port": "8080", "timeout": "30s", "hosts": "a, b"},
		"apiKey": "k",
		"unused": true,
	})

	var app struct {
		APIKey string `json:"apiKey"`
		Server struct {
			Hosts   []string
			Port    int
			Timeout time.Duration
			Debug   bool
		}
	}

	app.Server.Debug = true

	require.NoError(t, c.Bind(&app))
	assert.Equal(t, 8080, app.Server.Port)
	assert.Equal(t, 30*time.Second, app.Server.Timeout)
	assert.Equal(t, []string{"a", "b"}, app.Server.Hosts)
	assert.True(t, app.Server.Debug)
	assert.Equal(t, "k", app.APIKey)
}

// A conversion that fails is an error that names the key and never
// includes the value.
func TestBind_Error(t *testing.T) {
	t.Parallel()

	c := newConfig(t, m{"server": m{"port": "hunter2", "timeout": "30"}})

	var app struct{ Server struct{ Port int } }

	err := c.Bind(&app)
	require.EqualError(
		t,
		err,
		"bind config: server.port: cannot convert string to int: invalid syntax",
	)

	var d struct {
		Server struct{ Timeout time.Duration }
	}

	err = c.Bind(&d)
	require.EqualError(
		t,
		err,
		`bind config: server.timeout: duration has no unit; add one such as "s" or "ms"`,
	)

	err = c.Bind(app)
	require.EqualError(
		t,
		err,
		"bind config: invalid destination: want a non-nil pointer to a struct, "+
			"got struct { Server struct { Port int } }",
	)
}

// After filling the struct, Bind validates it by its `validate` tags. The
// validator's error is wrapped, so errors.As finds it.
func TestBind_Validates(t *testing.T) {
	t.Parallel()

	c := newConfig(t, m{"server": m{"port": "80"}})

	var app struct {
		Server struct {
			Host string `validate:"required"`
			Port int    `validate:"min=1024"`
		}
	}

	err := c.Bind(&app)

	var verrs validator.ValidationErrors

	require.ErrorAs(t, err, &verrs)
	assert.Len(t, verrs, 2)
	assert.Contains(t, err.Error(), "validate config: ")
	assert.Equal(t, 80, app.Server.Port, "the struct is filled before it is validated")
}

var errInvalid = errors.New("invalid")

type recordingValidator struct {
	got any
	err error
}

func (v *recordingValidator) StructCtx(_ context.Context, s any) error {
	v.got = s

	return v.err
}

// WithValidator replaces the validator; a nil one keeps the default, and
// so does a nil *validator.Validate, which used to panic in Bind.
func TestBind_WithValidator(t *testing.T) {
	t.Parallel()

	v := &recordingValidator{err: errInvalid}
	c := config.New(
		config.WithProvider(newStatic("static", m{"port": "80"})),
		config.WithValidator(v),
	)
	require.NoError(t, c.Load())

	var app struct {
		Port int `validate:"min=1024"`
	}

	err := c.Bind(&app)
	require.ErrorIs(t, err, errInvalid)
	require.EqualError(t, err, "validate config: invalid")
	assert.Same(t, &app, v.got)

	c = config.New(
		config.WithProvider(newStatic("static", m{"port": "80"})),
		config.WithValidator(nil),
	)
	require.NoError(t, c.Load())

	var verrs validator.ValidationErrors

	require.ErrorAs(t, c.Bind(&app), &verrs)

	var typedNil *validator.Validate

	c = config.New(
		config.WithProvider(newStatic("static", m{"port": "80"})),
		config.WithValidator(typedNil),
	)
	require.NoError(t, c.Load())

	require.NotPanics(t, func() { err = c.Bind(&app) })
	require.ErrorAs(t, err, &verrs)
}
