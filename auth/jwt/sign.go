package jwt

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"time"

	gojwt "github.com/golang-jwt/jwt/v5"

	"github.com/gasmod/gas/auth"
	"github.com/gasmod/gas/auth/internal/cryptoutil"
)

// tokenIDBytes is the number of random bytes in a token's `jti`.
const tokenIDBytes = 16

// registeredClaims are the claim names the service sets or checks, which
// custom claims may not use, in sorted order.
var registeredClaims = []string{"aud", "exp", "iat", "iss", "jti", "nbf", "sub"}

// Sign is SignWithExpiry with the configured expiry.
func (s *Service) Sign(subject string, claims map[string]any) (string, error) {
	st, err := s.loaded()
	if err != nil {
		return "", err
	}

	token, err := st.sign(subject, claims, st.settings.Expiry)
	if err != nil {
		return "", fmt.Errorf("%s: %w", ServiceName, err)
	}

	return token, nil
}

// SignWithExpiry returns a token for subject that expires after expiry,
// carrying claims besides the registered ones, as described in the
// package documentation. It returns an error, and no token, when the
// service is not initialized or only verifies, when subject is empty or
// too long, expiry is not positive, or a claim is reserved or cannot be
// encoded.
func (s *Service) SignWithExpiry(
	subject string,
	claims map[string]any,
	expiry time.Duration,
) (string, error) {
	st, err := s.loaded()
	if err != nil {
		return "", err
	}

	token, err := st.sign(subject, claims, expiry)
	if err != nil {
		return "", fmt.Errorf("%s: %w", ServiceName, err)
	}

	return token, nil
}

// sign checks its arguments in the documented order and returns the
// signed token. Its callers prefix ServiceName to its errors.
func (st *state) sign(subject string, claims map[string]any, expiry time.Duration) (string, error) {
	if st.signKey == nil {
		return "", ErrSigningKeyNotConfigured
	}

	if subject == "" {
		return "", ErrEmptySubject
	}

	err := auth.CheckLength("subject", subject)
	if err != nil {
		//nolint:wrapcheck // Sign and SignWithExpiry prefix the service name.
		return "", err
	}

	if expiry <= 0 {
		return "", fmt.Errorf("%w: must be positive", ErrInvalidTTL)
	}

	mc, err := customClaims(claims)
	if err != nil {
		return "", err
	}

	jti, err := cryptoutil.RandomString(tokenIDBytes)
	if err != nil {
		return "", fmt.Errorf("generate token ID: %w", err)
	}

	now := time.Now()

	mc["sub"] = subject
	mc["jti"] = jti
	mc["iat"] = gojwt.NewNumericDate(now)
	mc["exp"] = gojwt.NewNumericDate(now.Add(expiry))

	if st.settings.Issuer != "" {
		mc["iss"] = st.settings.Issuer
	}

	if st.settings.Audience != "" {
		mc["aud"] = st.settings.Audience
	}

	signed, err := gojwt.NewWithClaims(st.method, mc).SignedString(st.signKey)
	if err != nil {
		return "", fmt.Errorf("sign token: %w", err)
	}

	return signed, nil
}

// customClaims returns a copy of claims with room for the registered
// claims. It rejects a claim named after a registered claim, and one whose
// value cannot be encoded as JSON, naming the first such claim in sorted
// order. The encoder's error is replaced, since it may quote the value.
func customClaims(claims map[string]any) (gojwt.MapClaims, error) {
	mc := make(gojwt.MapClaims, len(claims)+len(registeredClaims))

	for _, name := range slices.Sorted(maps.Keys(claims)) {
		if slices.Contains(registeredClaims, name) {
			return nil, fmt.Errorf("%w %q", ErrReservedClaim, name)
		}

		_, err := json.Marshal(claims[name])
		if err != nil {
			return nil, fmt.Errorf("%w %q: value cannot be encoded as JSON", ErrInvalidClaim, name)
		}

		mc[name] = claims[name]
	}

	return mc, nil
}
