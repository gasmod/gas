package sqs_test

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/queue"
	"github.com/gasmod/gas/queue/sqs"
)

// credentials returns the credentials of the client of an initialized
// store.
func credentials(t *testing.T, s *sqs.Service) aws.Credentials {
	t.Helper()

	creds, err := s.Client().Options().Credentials.Retrieve(t.Context())
	require.NoError(t, err)

	return creds
}

// With no Region setting, the AWS SDK's chain decides the region, such as
// AWS_REGION; a Region setting takes precedence over it.
//
// Not parallel: it sets AWS variables.
func TestRegion_Chain(t *testing.T) {
	isolateAWS(t)
	t.Setenv("AWS_REGION", "eu-west-3")

	cfg := config("https://sqs.example")

	cfg.Queue.Region = ""
	assert.Equal(t, "eu-west-3", open(t, cfg).Client().Options().Region)

	cfg.Queue.Region = "ap-south-1"
	assert.Equal(t, "ap-south-1", open(t, cfg).Client().Options().Region)
}

// Init fails with ErrNoRegion when neither the Region setting nor the
// AWS SDK's chain has a region.
//
// Not parallel: it clears AWS variables.
func TestRegion_None(t *testing.T) {
	isolateAWS(t)

	cfg := config("https://sqs.example")

	cfg.Queue.Region = ""

	err := initErr(t, sqs.WithConfig(cfg))
	require.ErrorIs(t, err, sqs.ErrNoRegion)
	require.EqualError(t, err, "gas/queue/sqs: init: no region")
}

// Static credentials are used when both keys are set, with the session
// token; with one key missing, the AWS SDK's chain applies, as
// config/secretsmanager does it.
//
// Not parallel: it sets AWS variables.
func TestCredentials_Static(t *testing.T) {
	isolateAWS(t)
	t.Setenv("AWS_ACCESS_KEY_ID", "ENVKEY")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "envsecret")

	cfg := config("https://sqs.example")

	cfg.Queue.SessionToken = "token"

	creds := credentials(t, open(t, cfg))
	assert.Equal(t, testKeyID, creds.AccessKeyID)
	assert.Equal(t, "token", creds.SessionToken)

	cfg.Queue.SecretAccessKey = ""
	assert.Equal(t, "ENVKEY", credentials(t, open(t, cfg)).AccessKeyID)

	cfg.Queue.SecretAccessKey, cfg.Queue.AccessKeyID = testSecret, ""
	creds = credentials(t, open(t, cfg))
	assert.Equal(t, "ENVKEY", creds.AccessKeyID)
	assert.Empty(t, creds.SessionToken)
}

// Init logs where the credentials come from, `static` or `default
// chain`; a partial static set logs a warning naming the keys missing,
// and no value, the access key ID included.
//
// Not parallel: it sets AWS variables.
func TestLogs_CredentialSource(t *testing.T) {
	isolateAWS(t)
	t.Setenv("AWS_ACCESS_KEY_ID", "ENVKEY")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "envsecret")

	const chain = "default chain"

	cases := []struct {
		edit    func(*sqs.Settings)
		source  string
		missing string
	}{
		{func(*sqs.Settings) {}, "static", ""},
		{func(s *sqs.Settings) { s.AccessKeyID, s.SecretAccessKey = "", "" }, chain, ""},
		{func(s *sqs.Settings) { s.SecretAccessKey = "" }, chain, "queue.secretaccesskey"},
		{func(s *sqs.Settings) { s.AccessKeyID = "" }, chain, "queue.accesskeyid"},
		{
			func(s *sqs.Settings) {
				s.AccessKeyID, s.SecretAccessKey, s.SessionToken = "", "", "zqxtoken"
			},
			chain,
			"queue.accesskeyid, queue.secretaccesskey",
		},
	}

	for _, c := range cases {
		cfg := config("https://sqs.example")

		cfg.Queue.AccessKeyID = "AKIAZQXKEYID"
		c.edit(&cfg.Queue)

		s, log := build(t, sqs.WithConfig(cfg))
		require.NoError(t, s.Init(t.Context()))

		entries := log.entries()
		info := entries[len(entries)-1]
		assert.Equal(t, "service initialized", info.msg)
		assert.Equal(t, c.source, info.fields["credentials"])

		if c.missing == "" {
			assert.Len(t, entries, 1)
		} else {
			require.Len(t, entries, 2)
			assert.Equal(t, entry{
				level: "warn",
				msg:   "static credentials incomplete, using the default chain",
				fields: map[string]any{
					"service": sqs.ServiceName,
					"missing": c.missing,
				},
			}, entries[0])
		}

		assertNoSecrets(t, log.text(), "AKIA", "ENVKEY", "envsecret")
	}
}

// Init fails with ErrNoCredentials when no credentials can be retrieved,
// without the AWS SDK's error.
//
// Not parallel: it clears AWS variables.
func TestCredentials_None(t *testing.T) {
	isolateAWS(t)

	cfg := config("https://sqs.example")

	cfg.Queue.AccessKeyID = ""

	err := initErr(t, sqs.WithConfig(cfg))
	require.ErrorIs(t, err, sqs.ErrNoCredentials)
	require.EqualError(t, err, "gas/queue/sqs: init: no credentials: failed")
}

// Init retrieves the credentials within its context and OpTimeout, so a
// credential source that never answers fails Init with `timed out`, and
// with the context's error too when Init's context ended first.
//
// Not parallel: it sets AWS variables.
func TestCredentials_Timeout(t *testing.T) {
	isolateAWS(t)
	t.Setenv("AWS_CONTAINER_CREDENTIALS_FULL_URI", "http://"+blackhole(t)+"/creds")

	cfg := config("https://sqs.example")

	cfg.Queue.AccessKeyID = ""
	cfg.Queue.OpTimeout = 300 * time.Millisecond

	err := initErr(t, sqs.WithConfig(cfg))
	require.ErrorIs(t, err, sqs.ErrNoCredentials)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.EqualError(t, err, "gas/queue/sqs: init: no credentials: timed out")

	cfg.Queue.OpTimeout = time.Minute

	s, _ := build(t, sqs.WithConfig(cfg))

	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()

	err = s.Init(ctx)
	require.ErrorIs(t, err, sqs.ErrNoCredentials)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.EqualError(
		t,
		err,
		"gas/queue/sqs: init: no credentials: timed out: context deadline exceeded",
	)
	assert.ErrorIs(t, s.CheckReady(t.Context()), queue.ErrNotInitialized)
}

// Init fails with queue.ErrInvalidConfig when the AWS configuration does
// not load, without the AWS SDK's error, which names the profile.
//
// Not parallel: it sets AWS variables.
func TestInit_AWSConfig(t *testing.T) {
	isolateAWS(t)
	t.Setenv("AWS_PROFILE", "zqxprofile")

	err := initErr(t, sqs.WithConfig(config("https://sqs.example")))
	require.ErrorIs(t, err, queue.ErrInvalidConfig)
	require.EqualError(
		t,
		err,
		"gas/queue/sqs: init: invalid config: AWS configuration failed to load",
	)
}
