// Package auth verifies operator bearer tokens of the back-office admin API:
// stateless HS256 JWTs (CBL-12.1); the issuer is out of band (KI-backoffice-02).
package auth

import (
	"context"
	"fmt"

	"github.com/golang-jwt/jwt/v5"

	"github.com/cbl/cbl-kit/grpcmw"

	"github.com/cbl/cbl-backoffice/internal/domain"
)

// MinSecretLength is the minimum accepted length of the HS256 signing key:
// RFC 7518 section 3.2 requires a key of at least 256 bits for HMAC-SHA256.
const MinSecretLength = 32

// Verifier validates operator tokens and extracts the operator identity.
type Verifier struct {
	key []byte
}

// operatorClaims is the JWT claim set of an operator token.
type operatorClaims struct {
	// Roles are the operator role names; every entry must be a known role.
	Roles []string `json:"roles"`
	jwt.RegisteredClaims
}

// NewVerifier creates a verifier with the given HS256 signing key.
func NewVerifier(secret string) (*Verifier, error) {
	if len(secret) < MinSecretLength {
		return nil, fmt.Errorf("auth: jwt secret must be at least %d characters", MinSecretLength)
	}
	return &Verifier{key: []byte(secret)}, nil
}

// Verify parses and validates an operator token. Every rejection is a single
// unauthenticated error: the caller must not learn why the token was refused.
func (v *Verifier) Verify(_ context.Context, token string) (domain.Operator, error) {
	claims := &operatorClaims{}
	parsed, err := jwt.ParseWithClaims(token, claims,
		func(*jwt.Token) (any, error) { return v.key, nil },
		jwt.WithValidMethods([]string{"HS256"}),
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(0),
	)
	if err != nil || !parsed.Valid {
		return domain.Operator{}, invalidToken(err)
	}
	if claims.Subject == "" {
		return domain.Operator{}, invalidToken(nil)
	}
	roles := make([]domain.Role, 0, len(claims.Roles))
	for _, name := range claims.Roles {
		role, ok := domain.ParseRole(name)
		if !ok {
			return domain.Operator{}, invalidToken(fmt.Errorf("unknown role %q", name))
		}
		roles = append(roles, role)
	}
	return domain.Operator{Subject: claims.Subject, Roles: roles}, nil
}

// invalidToken builds the uniform rejection error: the cause is kept for the
// log chain but the exposed message stays generic.
func invalidToken(cause error) error {
	return grpcmw.E(grpcmw.KindUnauthenticated, "unauthenticated",
		"operator token is missing or invalid", cause)
}
