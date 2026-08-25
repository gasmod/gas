// Example: loading configuration from AWS Secrets Manager.
//
// Against real AWS, credentials come from the default chain (env vars,
// shared config, IAM role) and only WithSecret/WithRegion are needed.
// Against LocalStack, set the endpoint and static test credentials:
//
//	awslocal secretsmanager create-secret --name myapp/config \
//	    --secret-string '{"database": {"host": "db.internal"}, "api_key": "abc"}'
//	awslocal secretsmanager create-secret --name myapp/db-pass \
//	    --secret-string 'hunter2'
//	AWS_ENDPOINT_URL=http://localhost:4566 go run .
package main

import (
	"fmt"
	"log"
	"os"

	config "github.com/gasmod/gas/config"
	"github.com/gasmod/gas/config/providers/secretsmanager"
)

func main() {
	opts := []secretsmanager.Option{
		secretsmanager.WithSecret("myapp/config"),
		secretsmanager.WithSecretAtKey("myapp/db-pass", "database.password"),
		secretsmanager.WithRegion("us-east-1"),
	}

	// Point at LocalStack (or any custom endpoint) when AWS_ENDPOINT_URL is set.
	if endpoint := os.Getenv("AWS_ENDPOINT_URL"); endpoint != "" {
		opts = append(opts,
			secretsmanager.WithEndpoint(endpoint),
			secretsmanager.WithStaticCredentials("test", "test"),
		)
	}

	cfg := config.New(
		config.WithProvider(secretsmanager.NewProvider(opts...)),
	)

	if err := cfg.Load(); err != nil {
		log.Fatalf("load config: %v", err)
	}

	fmt.Println("database.host:", cfg.Get("database.host"))
	fmt.Println("database.password:", cfg.Get("database.password"))
	fmt.Println("api_key:", cfg.Get("api_key"))
}
