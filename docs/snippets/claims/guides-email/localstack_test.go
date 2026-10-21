package guidesemail_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsses "github.com/aws/aws-sdk-go-v2/service/ses"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/gasmod/gas"
)

// localstackImage is the LocalStack release the storage module's tests pin,
// by digest, so a retagged image cannot change the behavior under test.
const localstackImage = "localstack/localstack:4.14.0@sha256:3ebc37595918b8accb852f8048fef2aff047d465167edd655528065b07bc364a"

func startLocalStack(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("LocalStack is a Linux container image")
	}
	if testing.Short() {
		t.Skip("skipping LocalStack test in short mode")
	}
	requireDocker(t)
	ctx := context.Background()
	ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        localstackImage,
			ExposedPorts: []string{"4566/tcp"},
			Env:          map[string]string{"SERVICES": "ses"},
			WaitingFor: wait.ForHTTP("/_localstack/health").
				WithPort("4566/tcp").
				WithStartupTimeout(90 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start localstack: %v", err)
	}
	t.Cleanup(func() { _ = ctr.Terminate(context.Background()) })
	host, err := ctr.Host(ctx)
	if err != nil {
		t.Fatalf("localstack host: %v", err)
	}
	port, err := ctr.MappedPort(ctx, "4566/tcp")
	if err != nil {
		t.Fatalf("localstack port: %v", err)
	}
	return fmt.Sprintf("http://%s:%s", host, port.Port())
}

// Docs: guides/email.mdx#local-development
// "Point `Email.Endpoint` at LocalStack and nothing else changes"
func TestSendThroughLocalStack(t *testing.T) {
	t.Parallel()
	endpoint := startLocalStack(t)
	ctx := context.Background()

	cfg := testConfig(endpoint)
	cfg.Email.FromEmail = "noreply@example.com"
	svc := newService(t, cfg, nil)

	// LocalStack, like SES in its sandbox, only sends from a verified
	// identity. Verifying it is LocalStack setup, not part of the service.
	_, err := svc.Client().VerifyEmailIdentity(ctx, &awsses.VerifyEmailIdentityInput{
		EmailAddress: aws.String("noreply@example.com"),
	})
	if err != nil {
		t.Fatalf("VerifyEmailIdentity: %v", err)
	}

	err = svc.Send(ctx, &gas.Email{
		To:       []string{"to@example.com"},
		Subject:  "LocalStack claim",
		TextBody: "text body",
		HTMLBody: "<p>html body</p>",
	})
	if err != nil {
		t.Fatalf("Send to LocalStack: %v", err)
	}

	// LocalStack lists the messages its SES received at /_aws/ses.
	resp, err := http.Get(endpoint + "/_aws/ses")
	if err != nil {
		t.Fatalf("GET /_aws/ses: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var store struct {
		Messages []struct {
			Source  string `json:"Source"`
			Subject string `json:"Subject"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&store); err != nil {
		t.Fatalf("decode /_aws/ses: %v", err)
	}
	for _, m := range store.Messages {
		if m.Subject == "LocalStack claim" && m.Source == "noreply@example.com" {
			return
		}
	}
	t.Fatalf("LocalStack received %+v, want a message with subject %q from %q",
		store.Messages, "LocalStack claim", "noreply@example.com")
}
