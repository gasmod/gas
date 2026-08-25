package providers_test

import (
	"os"
	"testing"
	"testing/fstest"

	"github.com/gasmod/gas/config/providers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDotEnvProvider_DoesNotMirrorToProcessEnvByDefault guards against a
// security issue in DotEnvProvider.Load() (gas/config/providers/dotenv_provider.go).
//
// Historically appendToOSEnv defaulted to true, so Load() copied EVERY entry
// from the .env file into the LIVE process environment via os.Setenv — using
// the raw, untransformed key, with no allow/deny-list. Two consequences:
//
//  1. Secret exposure (CWE-200 / CWE-526): every secret in .env (DB passwords,
//     API keys, JWT signing keys) is promoted into the process environment,
//     where it is inherited by ANY child process the app later spawns
//     (os/exec) and is exposed via the OS (e.g. /proc/<pid>/environ). A config
//     library that handles secrets should not silently widen their blast radius
//     to the whole process tree.
//
//  2. Arbitrary environment injection (CWE-15): because keys are written raw and
//     unfiltered, a .env entry can OVERWRITE security-relevant process variables
//     such as PATH or LD_PRELOAD. If any part of the .env is attacker-influenced,
//     overwriting PATH and letting the app shell out is a route to code
//     execution.
//
// The fix is: default the OS-env mirror OFF. With default construction, Load()
// must NOT touch the process environment at all. This test FAILS while the
// insecure default is in place and only PASSES once the default is flipped.
//
// NOTE: this test mutates the global process environment, so it must NOT be
// parallel; it snapshots and restores every variable it touches.
func TestDotEnvProvider_DoesNotMirrorToProcessEnvByDefault(t *testing.T) {
	const (
		secretKey = "GAS_DOTENV_LEAKED_SECRET"
		injectKey = "PATH" // a security-relevant variable the .env should never control
		evilPath  = "/tmp/attacker-controlled"
	)

	// Snapshot and restore the variables this test perturbs.
	origPath, hadPath := os.LookupEnv(injectKey)
	_, hadSecret := os.LookupEnv(secretKey)
	t.Cleanup(func() {
		if hadPath {
			_ = os.Setenv(injectKey, origPath)
		} else {
			_ = os.Unsetenv(injectKey)
		}
		if !hadSecret {
			_ = os.Unsetenv(secretKey)
		}
	})

	// Preconditions: the process env does not yet hold the .env's values.
	require.NotEqual(t, evilPath, os.Getenv(injectKey))
	require.Empty(t, os.Getenv(secretKey))

	fsys := fstest.MapFS{
		".env": &fstest.MapFile{Data: []byte(
			secretKey + "=super-secret-db-password\n" +
				injectKey + "=" + evilPath + "\n",
		)},
	}

	// Default construction: the OS-env mirror must be OFF by default.
	p := providers.NewDotEnvProvider(
		providers.WithDotEnvFilePath(".env"),
		providers.WithDotEnvFileFS(&fsys),
	)

	_, err := p.Load()
	require.NoError(t, err)

	// (1) The secret must NOT leak into the live process environment.
	assert.Empty(t, os.Getenv(secretKey),
		"secret from .env was silently exported into the process environment via os.Setenv")

	// (2) A .env entry must NOT overwrite PATH in the running process.
	assert.NotEqual(t, evilPath, os.Getenv(injectKey),
		"a .env entry overwrote PATH in the live process environment (no allow/deny-list)")
}

// TestDotEnvProvider_AppendToOSEnvDoesNotOverwriteProtectedVars guards CWE-15
// for callers who explicitly opt into the OS-env mirror.
//
// Even when appendToOSEnv is enabled, Load() must not let a raw, unfiltered
// .env key clobber a security-relevant process variable such as PATH or
// LD_PRELOAD. This test FAILS while keys are written verbatim and only PASSES
// once a deny-list / no-overwrite guard protects such variables.
func TestDotEnvProvider_AppendToOSEnvDoesNotOverwriteProtectedVars(t *testing.T) {
	const (
		secretKey = "GAS_DOTENV_OPTED_IN_VAR"
		injectKey = "PATH"
		evilPath  = "/tmp/attacker-controlled"
	)

	origPath, hadPath := os.LookupEnv(injectKey)
	_, hadSecret := os.LookupEnv(secretKey)
	t.Cleanup(func() {
		if hadPath {
			_ = os.Setenv(injectKey, origPath)
		} else {
			_ = os.Unsetenv(injectKey)
		}
		if !hadSecret {
			_ = os.Unsetenv(secretKey)
		}
	})

	require.NotEqual(t, evilPath, os.Getenv(injectKey))

	fsys := fstest.MapFS{
		".env": &fstest.MapFile{Data: []byte(
			secretKey + "=ordinary-value\n" +
				injectKey + "=" + evilPath + "\n",
		)},
	}

	// Explicit opt-in to mirroring.
	p := providers.NewDotEnvProvider(
		providers.WithDotEnvFilePath(".env"),
		providers.WithDotEnvFileFS(&fsys),
		providers.WithDotEnvFileAppendToOSEnv(true),
	)

	_, err := p.Load()
	require.NoError(t, err)

	// An ordinary, non-protected var may be mirrored when explicitly opted in.
	assert.Equal(t, "ordinary-value", os.Getenv(secretKey),
		"opted-in mirror should export ordinary .env vars")

	// But a protected variable like PATH must never be overwritten.
	assert.NotEqual(t, evilPath, os.Getenv(injectKey),
		"a .env entry overwrote the protected PATH variable despite the deny-list")
}
