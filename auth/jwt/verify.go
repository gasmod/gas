package jwt

import (
	"errors"
	"fmt"
	"slices"
	"time"

	gojwt "github.com/golang-jwt/jwt/v5"
)

// TokenClaims are the claims of a verified token.
type TokenClaims struct {
	// ExpiresAt is the `exp` claim.
	ExpiresAt time.Time
	// IssuedAt is the `iat` claim, or the zero time when the token has
	// none.
	IssuedAt time.Time
	// CustomClaims holds every claim that is not registered, as decoded
	// from JSON: numbers are float64, objects map[string]any and arrays
	// []any. It is never nil.
	CustomClaims map[string]any
	// Subject is the `sub` claim, never empty.
	Subject string
	// TokenID is the `jti` claim, or empty when the token has none.
	TokenID string
	// Issuer is the `iss` claim, or empty when the token has none.
	Issuer string
	// Audience is the `aud` claim, or nil when the token has none.
	Audience []string
}

// The reasons a token is invalid, appended to ErrTokenInvalid. None of
// them quotes the token.
const (
	reasonMalformed     = "malformed"
	reasonAlgorithm     = "unknown or missing algorithm"
	reasonSignature     = "bad signature or unexpected algorithm"
	reasonMissingClaim  = "missing required claim"
	reasonClaimType     = "claim of the wrong type"
	reasonFutureIssued  = "issued in the future"
	reasonNotValidYet   = "not valid yet"
	reasonIssuer        = "issuer not accepted"
	reasonAudience      = "audience not accepted"
	reasonMultipleToken = "more than one bearer token"
)

// parseReasons maps the parser's errors to reasons, in the order they are
// checked: the first that matches is reported. ErrTokenExpired is not
// among them, so an expired token that fails another check is reported
// for that check.
var parseReasons = []struct {
	cause  error
	reason string
}{
	{gojwt.ErrTokenMalformed, reasonMalformed},
	{gojwt.ErrTokenUnverifiable, reasonAlgorithm},
	{gojwt.ErrTokenSignatureInvalid, reasonSignature},
	{gojwt.ErrTokenRequiredClaimMissing, reasonMissingClaim},
	{gojwt.ErrInvalidType, reasonClaimType},
	{gojwt.ErrTokenUsedBeforeIssued, reasonFutureIssued},
	{gojwt.ErrTokenNotValidYet, reasonNotValidYet},
	{gojwt.ErrTokenInvalidIssuer, reasonIssuer},
	{gojwt.ErrTokenInvalidAudience, reasonAudience},
}

// Verify checks token and returns its claims, as described in the package
// documentation. Every error it returns for a token wraps ErrTokenInvalid
// or ErrTokenExpired, and so auth.ErrUnauthenticated, and never includes
// the token. Before Init it returns an error wrapping ErrNotInitialized.
func (s *Service) Verify(token string) (*TokenClaims, error) {
	st, err := s.loaded()
	if err != nil {
		return nil, err
	}

	claims, err := st.verify(token)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ServiceName, err)
	}

	return claims, nil
}

// verify parses and checks token with the state's parser and key. Its
// callers prefix ServiceName to its errors.
func (st *state) verify(token string) (*TokenClaims, error) {
	parsed, err := st.parser.Parse(token, st.keyFunc)
	if err != nil {
		verr := parseError(err)
		if !errors.Is(verr, ErrTokenExpired) {
			return nil, verr
		}

		// The signature checked out and only `exp` failed among the
		// parser's checks; the token is expired only if it passes ours.
		mc, _ := parsed.Claims.(gojwt.MapClaims)

		_, cerr := claimsFrom(mc)
		if cerr != nil {
			return nil, cerr
		}

		return nil, verr
	}

	mc, _ := parsed.Claims.(gojwt.MapClaims)

	return claimsFrom(mc)
}

// keyFunc returns the verification key. The parser has checked the
// token's algorithm against the configured one before it calls keyFunc.
func (st *state) keyFunc(*gojwt.Token) (any, error) {
	return st.verifyKey, nil
}

// parseError replaces an error of the parser, which may quote the token,
// with ErrTokenInvalid and a fixed reason, or with ErrTokenExpired.
func parseError(err error) error {
	for _, r := range parseReasons {
		if errors.Is(err, r.cause) {
			return invalid(r.reason)
		}
	}

	if errors.Is(err, gojwt.ErrTokenExpired) {
		return ErrTokenExpired
	}

	return ErrTokenInvalid
}

// invalid returns ErrTokenInvalid with reason.
func invalid(reason string) error {
	return fmt.Errorf("%w: %s", ErrTokenInvalid, reason)
}

// claimsFrom checks the claims the parser does not, `sub` present and
// `sub`, `jti`, `iss` and `aud` of the right types, and returns the
// TokenClaims.
func claimsFrom(mc gojwt.MapClaims) (*TokenClaims, error) {
	tc, err := stringClaims(mc)
	if err != nil {
		return nil, err
	}

	if tc.Subject == "" {
		return nil, invalid(reasonMissingClaim)
	}

	aud, err := mc.GetAudience()
	if err != nil {
		return nil, invalid(reasonClaimType)
	}

	if len(aud) > 0 {
		tc.Audience = aud
	}

	// The parser has checked that both are numbers, and `exp` present.
	exp, _ := mc.GetExpirationTime()

	tc.ExpiresAt = exp.Time

	iat, _ := mc.GetIssuedAt()
	if iat != nil {
		tc.IssuedAt = iat.Time
	}

	tc.CustomClaims = make(map[string]any, len(mc))

	for name, v := range mc {
		if !slices.Contains(registeredClaims, name) {
			tc.CustomClaims[name] = v
		}
	}

	return tc, nil
}

// stringClaims returns TokenClaims holding `sub`, `jti` and `iss`, each
// empty when absent, or an error when one is not a string.
func stringClaims(mc gojwt.MapClaims) (*TokenClaims, error) {
	sub, err := mc.GetSubject()
	if err != nil {
		return nil, invalid(reasonClaimType)
	}

	iss, err := mc.GetIssuer()
	if err != nil {
		return nil, invalid(reasonClaimType)
	}

	var tokenID string

	if v, ok := mc["jti"]; ok {
		tokenID, ok = v.(string)
		if !ok {
			return nil, invalid(reasonClaimType)
		}
	}

	return &TokenClaims{Subject: sub, TokenID: tokenID, Issuer: iss}, nil
}
