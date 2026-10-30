package secretsmanager

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssm "github.com/aws/aws-sdk-go-v2/service/secretsmanager"

	"github.com/gasmod/gas/config/internal/jsonobj"
	"github.com/gasmod/gas/config/internal/maputil"
)

// checkKey reports a key with an empty level. No path reaches such a
// level, and the config module drops it with what it holds.
func checkKey(s secret) error {
	if s.key == "" {
		return nil
	}

	for level := range strings.SplitSeq(s.key, ".") {
		if strings.TrimSpace(level) == "" {
			return fmt.Errorf("%w %q for secret %s: a level is empty", ErrInvalidKey, s.key, s.name)
		}
	}

	return nil
}

// load fetches the secrets in registration order and merges them, a later
// secret winning.
func (p *Provider) load(ctx context.Context) (map[string]any, error) {
	client, err := p.getClient(ctx)
	if err != nil {
		return nil, err
	}

	values := make(map[string]any)

	for _, s := range p.secrets {
		out, err := client.GetSecretValue(ctx, &awssm.GetSecretValueInput{
			SecretId: aws.String(s.name),
		})
		if err != nil {
			return nil, fmt.Errorf("%w %s: %w", ErrSecretFetchFailed, s.name, err)
		}

		fragment, err := decode(s, out)
		if err == nil {
			// Each secret is normalized on its own, so a later secret
			// overrides an earlier one whatever the case of its keys.
			fragment, err = maputil.NormalizeKeys(fragment)
		}

		if err != nil {
			return nil, fmt.Errorf("%w %s: %w", ErrSecretDecodeFailed, s.name, err)
		}

		maputil.Merge(values, fragment)
	}

	return values, nil
}

// decode turns a secret's value into a map: its JSON object at the root,
// or the object or the value's text nested at the key.
func decode(s secret, out *awssm.GetSecretValueOutput) (map[string]any, error) {
	text := out.SecretBinary
	if out.SecretString != nil {
		text = []byte(*out.SecretString)
	}

	if s.key == "" {
		//nolint:wrapcheck // load wraps it with ErrSecretDecodeFailed and the name.
		return jsonobj.Decode(text)
	}

	// Anything that is not a JSON object is stored as its text. An object
	// holding a number out of range is a JSON object all the same, so it is
	// an error, as it is at the root.
	obj, err := jsonobj.Decode(text)
	if err == nil {
		return nest(s.key, obj), nil
	}

	if errors.Is(err, jsonobj.ErrNumberRange) {
		//nolint:wrapcheck // load wraps it with ErrSecretDecodeFailed and the name.
		return nil, err
	}

	return nest(s.key, string(text)), nil
}

// nest returns a map holding value at the dot-separated key, one map per
// level.
func nest(key string, value any) map[string]any {
	levels := strings.Split(key, ".")
	last := len(levels) - 1

	m := map[string]any{levels[last]: value}
	for _, level := range slices.Backward(levels[:last]) {
		m = map[string]any{level: m}
	}

	return m
}
