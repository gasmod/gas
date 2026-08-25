package secretsmanager

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssm "github.com/aws/aws-sdk-go-v2/service/secretsmanager"

	"github.com/gasmod/gas/config/internal/maputils"
)

// load fetches all registered secrets and merges them in registration order,
// later secrets overriding earlier ones on key conflicts.
func (p *Provider) load(ctx context.Context) (map[string]any, error) {
	client, err := p.getClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrClientInitFailed, err)
	}

	values := make(map[string]any)

	for _, ref := range p.secrets {
		out, err := client.GetSecretValue(ctx, &awssm.GetSecretValueInput{
			SecretId: aws.String(ref.name),
		})
		if err != nil {
			return nil, fmt.Errorf("%w %s: %w", ErrSecretFetchFailed, ref.name, err)
		}

		payload := out.SecretBinary
		if out.SecretString != nil {
			payload = []byte(*out.SecretString)
		}

		fragment, err := decodeSecret(ref, payload)
		if err != nil {
			return nil, err
		}

		maputils.Merge(values, fragment)
	}

	return values, nil
}

// decodeSecret converts one secret payload into a map fragment. Root-merged
// secrets (empty key) must be JSON objects; keyed secrets nest a JSON object
// or, failing that, the raw string at the key.
func decodeSecret(ref secretRef, payload []byte) (map[string]any, error) {
	var obj map[string]any
	objErr := json.Unmarshal(payload, &obj)
	if objErr == nil && obj == nil {
		objErr = fmt.Errorf("value is null")
	}

	if ref.key == "" {
		if objErr != nil {
			return nil, fmt.Errorf("%w %s: value is not a JSON object: %w", ErrSecretDecodeFailed, ref.name, objErr)
		}

		return obj, nil
	}

	if objErr == nil {
		return nestAtKey(ref.key, obj), nil
	}

	return nestAtKey(ref.key, string(payload)), nil
}

// nestAtKey builds a nested map placing value at the dot-notation key.
func nestAtKey(key string, value any) map[string]any {
	parts := strings.Split(key, ".")

	root := make(map[string]any)
	current := root

	for _, part := range parts[:len(parts)-1] {
		child := make(map[string]any)
		current[part] = child
		current = child
	}

	current[parts[len(parts)-1]] = value

	return root
}
