package jwt_test

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth"
	"github.com/gasmod/gas/auth/internal/svcutil"
	"github.com/gasmod/gas/auth/internal/testutil"
	"github.com/gasmod/gas/auth/jwt"
)

// The constructor has the shape the gas container expects, and Service
// implements the interfaces it documents.
var (
	_ func(gas.ConfigProvider, gas.Logger) (*jwt.Service, error) = jwt.New()
	_ gas.Service                                                = (*jwt.Service)(nil)
	_ auth.Authenticator                                         = (*jwt.Service)(nil)
	_ jwt.Provider                                               = (*jwt.Service)(nil)
	_ auth.Authenticator                                         = jwt.Provider(nil)
)

// The shared sentinels are aliases of the auth module's, so they match
// the same errors from its other services.
func TestErrors_SharedSentinels(t *testing.T) {
	t.Parallel()

	assert.Same(t, auth.ErrEmptySubject, jwt.ErrEmptySubject)
	assert.Same(t, auth.ErrValueTooLong, jwt.ErrValueTooLong)

	for _, pair := range [][2]error{
		{svcutil.ErrNilLogger, jwt.ErrNilLogger},
		{svcutil.ErrNilConfig, jwt.ErrNilConfig},
		{svcutil.ErrNoConfigProvider, jwt.ErrNoConfigProvider},
		{svcutil.ErrInvalidConfig, jwt.ErrInvalidConfig},
		{svcutil.ErrNotInitialized, jwt.ErrNotInitialized},
	} {
		assert.Same(t, pair[0], pair[1])
	}
}

// Name returns ServiceName.
func TestService_Name(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "gas/auth/jwt", build(t, hsConfig()).Name())
	assert.Equal(t, jwt.ServiceName, build(t, hsConfig()).Name())
}

// The constructor fails with ErrNilLogger for a nil logger.
func TestNew_NilLogger(t *testing.T) {
	t.Parallel()

	svc, err := jwt.New(jwt.WithConfig(hsConfig()))(nil, nil)
	require.ErrorIs(t, err, jwt.ErrNilLogger)
	require.EqualError(t, err, "gas/auth/jwt: logger is nil")
	assert.Nil(t, svc)
}

// A successful Init logs `service initialized` at info level with the
// signing method and whether the service only verifies.
func TestInit_Logs(t *testing.T) {
	t.Parallel()

	pub, _ := rsaFiles(t, rsaKey2048())

	for _, tt := range []struct {
		cfg        *jwt.Config
		method     string
		verifyOnly bool
	}{
		{cfg: hsConfig(), method: "HS256"},
		{cfg: rsConfig(pub, ""), method: "RS256", verifyOnly: true},
	} {
		logger := &testutil.RecordingLogger{}

		svc, err := jwt.New(jwt.WithConfig(tt.cfg))(nil, logger)
		require.NoError(t, err)
		require.NoError(t, svc.Init(t.Context()))

		assert.Equal(t, []testutil.Entry{{
			Level: "info",
			Msg:   "service initialized",
			Fields: map[string]any{
				"service":        "gas/auth/jwt",
				"signing_method": tt.method,
				"verify_only":    tt.verifyOnly,
			},
		}}, logger.Entries())
	}

	logger := &testutil.RecordingLogger{}
	svc, err := jwt.New(jwt.WithConfig(nil))(nil, logger)
	require.NoError(t, err)
	require.Error(t, svc.Init(t.Context()))
	assert.Empty(t, logger.Entries(), "a failed Init logs nothing")
}

// Close makes the service uninitialized and returns nil; Init brings it
// back, any number of times.
func TestService_Restart(t *testing.T) {
	t.Parallel()

	svc := build(t, hsConfig())
	require.NoError(t, svc.Close(t.Context()), "Close before Init")

	for range 3 {
		require.NoError(t, svc.Init(t.Context()))

		token, err := svc.Sign("alice", nil)
		require.NoError(t, err)

		_, err = svc.Verify(token)
		require.NoError(t, err)

		require.NoError(t, svc.Close(t.Context()))

		_, err = svc.Verify(token)
		require.ErrorIs(t, err, jwt.ErrNotInitialized)

		_, err = svc.Sign("alice", nil)
		require.ErrorIs(t, err, jwt.ErrNotInitialized)
		require.EqualError(t, err, errNotInitial)
	}
}

// A failed Init leaves the service uninitialized, not on the keys of the
// Init before it.
func TestInit_FailureUninitializes(t *testing.T) {
	t.Parallel()

	cfg := hsConfig()
	svc := newService(t, cfg)

	token, err := svc.Sign("alice", nil)
	require.NoError(t, err)

	cfg.JWT.SigningKey = "short"

	require.ErrorIs(t, svc.Init(t.Context()), jwt.ErrInvalidConfig)

	_, err = svc.Verify(token)
	require.ErrorIs(t, err, jwt.ErrNotInitialized)

	_, err = svc.Authenticate(t.Context(), request("Bearer "+token))
	require.ErrorIs(t, err, jwt.ErrNotInitialized)
}

// Sign, Verify and Authenticate are safe to call from many goroutines,
// beside Init; each call sees one Init's keys. Run with -race.
func TestService_Concurrency(t *testing.T) {
	t.Parallel()

	svc := newService(t, hsConfig())

	var wg sync.WaitGroup

	for range 8 {
		wg.Go(func() {
			for range 50 {
				// t.Error, not require, since this is not the test's
				// goroutine.
				token, err := svc.Sign("alice", m{"k": "v"})
				if err != nil {
					t.Error(err)

					return
				}

				_, err = svc.Verify(token)
				if err != nil {
					t.Error(err)
				}

				_, err = svc.Authenticate(t.Context(), request("Bearer "+token))
				if err != nil {
					t.Error(err)
				}
			}
		})
	}

	wg.Go(func() {
		for range 50 {
			err := svc.Init(t.Context())
			if err != nil {
				t.Error(err)
			}
		}
	})

	wg.Wait()
}
