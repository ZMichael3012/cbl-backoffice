package auth_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cbl/cbl-kit/grpcmw"

	"github.com/cbl/cbl-backoffice/internal/auth"
	"github.com/cbl/cbl-backoffice/internal/domain"
)

// rsaKey generates an RSA key for the algorithm-pinning rejection case.
func rsaKey() (*rsa.PrivateKey, error) {
	return rsa.GenerateKey(rand.Reader, 2048)
}

const testSecret = "test-admin-jwt-secret-0123456789"

// mint signs a token with the given secret and arbitrary claims for the table.
func mint(t *testing.T, secret string, method jwt.SigningMethod, claims jwt.Claims) string {
	t.Helper()
	token := jwt.NewWithClaims(method, claims)
	signed, err := token.SignedString([]byte(secret))
	require.NoError(t, err)
	return signed
}

// validClaims returns a well-formed claim set.
func validClaims() *struct {
	Roles []string `json:"roles"`
	jwt.RegisteredClaims
} {
	claims := &struct {
		Roles []string `json:"roles"`
		jwt.RegisteredClaims
	}{
		Roles: []string{"auditor"},
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "op-1",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	return claims
}

func TestNewVerifierRejectsShortSecret(t *testing.T) {
	t.Parallel()

	_, err := auth.NewVerifier("short")
	require.Error(t, err)
}

func TestVerify(t *testing.T) {
	t.Parallel()

	verifier, err := auth.NewVerifier(testSecret)
	require.NoError(t, err)

	t.Run("valid token", func(t *testing.T) {
		t.Parallel()
		operator, err := verifier.Verify(context.Background(), mint(t, testSecret, jwt.SigningMethodHS256, validClaims()))
		require.NoError(t, err)
		assert.Equal(t, "op-1", operator.Subject)
		assert.Equal(t, []domain.Role{domain.RoleAuditor}, operator.Roles)
	})

	t.Run("empty roles authenticate with no permissions", func(t *testing.T) {
		t.Parallel()
		claims := validClaims()
		claims.Roles = nil
		operator, err := verifier.Verify(context.Background(), mint(t, testSecret, jwt.SigningMethodHS256, claims))
		require.NoError(t, err)
		assert.False(t, operator.Has(domain.PermissionAuditRead))
	})

	t.Run("duplicate roles collapse in the matrix", func(t *testing.T) {
		t.Parallel()
		claims := validClaims()
		claims.Roles = []string{"auditor", "auditor"}
		operator, err := verifier.Verify(context.Background(), mint(t, testSecret, jwt.SigningMethodHS256, claims))
		require.NoError(t, err)
		assert.True(t, operator.Has(domain.PermissionAuditRead))
	})

	rejections := []struct {
		name   string
		token  func(t *testing.T) string
		reason string
	}{
		{
			name: "expired",
			token: func(t *testing.T) string {
				claims := validClaims()
				claims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Minute))
				return mint(t, testSecret, jwt.SigningMethodHS256, claims)
			},
		},
		{
			name: "missing expiration",
			token: func(t *testing.T) string {
				claims := validClaims()
				claims.ExpiresAt = nil
				return mint(t, testSecret, jwt.SigningMethodHS256, claims)
			},
		},
		{
			name: "missing subject",
			token: func(t *testing.T) string {
				claims := validClaims()
				claims.Subject = ""
				return mint(t, testSecret, jwt.SigningMethodHS256, claims)
			},
		},
		{
			name: "unknown role",
			token: func(t *testing.T) string {
				claims := validClaims()
				claims.Roles = []string{"root"}
				return mint(t, testSecret, jwt.SigningMethodHS256, claims)
			},
		},
		{
			name: "wrong secret",
			token: func(t *testing.T) string {
				return mint(t, "another-secret-0123456789", jwt.SigningMethodHS256, validClaims())
			},
		},
		{
			name: "alg none",
			token: func(t *testing.T) string {
				token := jwt.NewWithClaims(jwt.SigningMethodNone, validClaims())
				signed, err := token.SignedString(jwt.UnsafeAllowNoneSignatureType)
				require.NoError(t, err)
				return signed
			},
		},
		{
			name: "alg rs256 pinned out",
			token: func(t *testing.T) string {
				key, err := rsaKey()
				require.NoError(t, err)
				token := jwt.NewWithClaims(jwt.SigningMethodRS256, validClaims())
				signed, err := token.SignedString(key)
				require.NoError(t, err)
				return signed
			},
		},
		{
			name:  "garbage",
			token: func(t *testing.T) string { return "not-a-jwt" },
		},
		{
			name:  "empty",
			token: func(t *testing.T) string { return "" },
		},
	}
	for _, tc := range rejections {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			operator, err := verifier.Verify(context.Background(), tc.token(t))
			require.Error(t, err)
			assert.Empty(t, operator.Subject)
			assert.Equal(t, grpcmw.KindUnauthenticated, grpcmw.KindOf(err))
			// The exposed message stays generic; the reason lives only in the
			// error cause (log chain).
			var target *grpcmw.Error
			require.ErrorAs(t, err, &target)
			assert.NotContains(t, target.Message, "expired")
		})
	}
}
