package secretsmanager_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssm "github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/stretchr/testify/require"
)

// errNotFound is what mockAPI returns for a secret it does not hold.
var errNotFound = errors.New("secret not found")

// mockAPI is an API that serves secrets from a map and records the name
// and context of each call.
type mockAPI struct {
	secrets map[string]*awssm.GetSecretValueOutput
	calls   []string
	ctxs    []context.Context
	mu      sync.Mutex
}

func newMock(secrets map[string]*awssm.GetSecretValueOutput) *mockAPI {
	return &mockAPI{secrets: secrets}
}

func (m *mockAPI) GetSecretValue(
	ctx context.Context,
	in *awssm.GetSecretValueInput,
	_ ...func(*awssm.Options),
) (*awssm.GetSecretValueOutput, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	name := aws.ToString(in.SecretId)

	m.calls = append(m.calls, name)
	m.ctxs = append(m.ctxs, ctx)

	out, ok := m.secrets[name]
	if !ok {
		return nil, errNotFound
	}

	return out, nil
}

// lastCtx returns the context of the latest call.
func (m *mockAPI) lastCtx(t *testing.T) context.Context {
	t.Helper()

	m.mu.Lock()
	defer m.mu.Unlock()

	require.NotEmpty(t, m.ctxs, "no GetSecretValue call")

	return m.ctxs[len(m.ctxs)-1]
}

func str(s string) *awssm.GetSecretValueOutput {
	return &awssm.GetSecretValueOutput{SecretString: aws.String(s)}
}

func bin(b []byte) *awssm.GetSecretValueOutput {
	return &awssm.GetSecretValueOutput{SecretBinary: b}
}

// request is what fakeServer records of a request.
type request struct {
	header http.Header
	body   map[string]any
}

// fakeServer is an in-process Secrets Manager endpoint. It answers
// GetSecretValue in the AWS JSON 1.1 protocol from secrets, with a
// ResourceNotFoundException for a name it does not hold, and records each
// request. A nil secrets map makes it wait until the request is canceled.
type fakeServer struct {
	srv     *httptest.Server
	secrets map[string]*awssm.GetSecretValueOutput
	reqs    []request
	mu      sync.Mutex
}

func newFakeServer(t *testing.T, secrets map[string]*awssm.GetSecretValueOutput) *fakeServer {
	t.Helper()

	f := &fakeServer{secrets: secrets}

	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)

	return f
}

func (f *fakeServer) serve(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)

		return
	}

	var body map[string]any

	jerr := json.Unmarshal(raw, &body)
	if jerr != nil {
		http.Error(w, jerr.Error(), http.StatusBadRequest)

		return
	}

	f.mu.Lock()

	f.reqs = append(f.reqs, request{header: r.Header.Clone(), body: body})
	f.mu.Unlock()

	if f.secrets == nil {
		<-r.Context().Done()

		return
	}

	w.Header().Set("Content-Type", "application/x-amz-json-1.1")

	name, _ := body["SecretId"].(string)
	out, ok := f.secrets[name]

	if !ok {
		w.Header().Set("X-Amzn-Errortype", "ResourceNotFoundException")
		w.WriteHeader(http.StatusBadRequest)

		//nolint:errchkjson // a failed write fails the request under test
		_ = json.NewEncoder(w).Encode(map[string]string{
			"__type":  "ResourceNotFoundException",
			"message": "Secrets Manager can't find the specified secret.",
		})

		return
	}

	// encoding/json writes a []byte in base64, as the protocol sends a
	// blob.
	resp := map[string]any{"Name": name}
	if out.SecretString != nil {
		resp["SecretString"] = *out.SecretString
	}

	if out.SecretBinary != nil {
		resp["SecretBinary"] = out.SecretBinary
	}

	//nolint:errchkjson // a failed write fails the request under test
	_ = json.NewEncoder(w).Encode(resp)
}

func (f *fakeServer) url() string {
	return f.srv.URL
}

func (f *fakeServer) requests() []request {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]request(nil), f.reqs...)
}

// isolateAWS hides the machine's AWS configuration and credentials from the
// SDK for the rest of the test, and keeps the SDK off the instance metadata
// service. Tests that use it set environment variables, so they cannot run
// in parallel.
func isolateAWS(t *testing.T) {
	t.Helper()

	empty := filepath.Join(t.TempDir(), "empty")
	require.NoError(t, os.WriteFile(empty, nil, 0o600))

	t.Setenv("AWS_CONFIG_FILE", empty)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", empty)
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")

	for _, name := range []string{
		"AWS_PROFILE",
		"AWS_DEFAULT_PROFILE",
		"AWS_REGION",
		"AWS_DEFAULT_REGION",
		"AWS_ACCESS_KEY_ID",
		"AWS_SECRET_ACCESS_KEY",
		"AWS_SESSION_TOKEN",
		"AWS_ENDPOINT_URL",
		"AWS_ENDPOINT_URL_SECRETS_MANAGER",
		"AWS_WEB_IDENTITY_TOKEN_FILE",
		"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI",
		"AWS_CONTAINER_CREDENTIALS_FULL_URI",
	} {
		t.Setenv(name, "")
		require.NoError(t, os.Unsetenv(name))
	}
}
