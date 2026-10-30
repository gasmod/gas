package jwt

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth"
)

// bearerScheme is the authentication scheme of a JWT in the Authorization
// header, matched without regard to case.
const bearerScheme = "Bearer"

// Authenticate returns the principal of the bearer token in r's
// Authorization header, as described in the package documentation: with
// no bearer token, or a nil r, an error wrapping auth.ErrNoCredentials;
// with one that does not verify, Verify's error. Before Init it returns
// an error wrapping ErrNotInitialized, whatever the request carries. The
// context is unused.
func (s *Service) Authenticate(_ context.Context, r *http.Request) (gas.Principal, error) {
	st, err := s.loaded()
	if err != nil {
		return nil, err
	}

	if r == nil {
		return nil, fmt.Errorf("%s: %w", ServiceName, auth.ErrNoCredentials)
	}

	token, err := bearerToken(r.Header)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ServiceName, err)
	}

	claims, err := st.verify(token)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ServiceName, err)
	}

	return auth.NewPrincipal(
		claims.Subject,
		auth.SchemeJWT,
		claims.TokenID,
		gas.BasePrincipalMetadata(claims.CustomClaims),
	), nil
}

// bearerToken returns the token of the one Authorization header whose
// scheme is Bearer. Headers with other schemes, or empty ones, are not
// credentials for this service.
func bearerToken(h http.Header) (string, error) {
	var (
		token string
		found int
	)

	for _, v := range h.Values("Authorization") {
		scheme, rest, _ := strings.Cut(strings.TrimSpace(v), " ")
		if !strings.EqualFold(scheme, bearerScheme) {
			continue
		}

		token = strings.TrimLeft(rest, " ")
		found++
	}

	switch {
	case found == 0:
		return "", auth.ErrNoCredentials
	case found > 1:
		return "", invalid(reasonMultipleToken)
	case token == "":
		return "", invalid(reasonMalformed)
	default:
		return token, nil
	}
}
