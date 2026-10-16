// Package httpmw contains the HTTP middleware of the back-office service:
// operator bearer authentication and the RBAC check (CBL-12.1).
package httpmw

import (
	"context"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/cbl/cbl-kit/ginmw"
	"github.com/cbl/cbl-kit/grpcmw"

	"github.com/cbl/cbl-backoffice/internal/domain"
)

// operatorContextKey sets the gin context key for the authenticated operator.
const operatorContextKey = "backoffice.operator"

// Verifier checks an operator bearer token and returns the identity.
type Verifier func(ctx context.Context, token string) (domain.Operator, error)

// Authn returns the authentication middleware: a strict single Bearer header
// (schema case-insensitive per RFC 7235), token verification, and a uniform
// 401 on every rejection; on success the operator is put into the context.
func Authn(verify Verifier) gin.HandlerFunc {
	return func(c *gin.Context) {
		if len(c.Request.Header.Values("Authorization")) != 1 {
			AbortUnauthorized(c)
			return
		}
		token, ok := bearerToken(c.GetHeader("Authorization"))
		if !ok {
			AbortUnauthorized(c)
			return
		}
		operator, err := verify(c.Request.Context(), token)
		if err != nil {
			if grpcmw.KindOf(err) != grpcmw.KindUnauthenticated {
				// Verification is unavailable (dependency): not a token failure.
				ginmw.AbortFromError(c, err)
				return
			}
			AbortUnauthorized(c)
			return
		}
		c.Set(operatorContextKey, operator)
		c.Next()
	}
}

// OperatorFrom returns the authenticated operator of the request (zero if the
// middleware was not applied).
func OperatorFrom(c *gin.Context) domain.Operator {
	value, ok := c.Get(operatorContextKey)
	if !ok {
		return domain.Operator{}
	}
	operator, _ := value.(domain.Operator)
	return operator
}

// AbortUnauthorized responds 401 without explaining the failure reason.
func AbortUnauthorized(c *gin.Context) {
	c.Header("WWW-Authenticate", `Bearer realm="admin"`)
	ginmw.AbortFromError(c, grpcmw.E(grpcmw.KindUnauthenticated, "unauthenticated",
		"operator token is missing or invalid", nil))
}

// AbortPermissionDenied responds 403 without details of the missing right.
func AbortPermissionDenied(c *gin.Context) {
	ginmw.AbortFromError(c, grpcmw.E(grpcmw.KindPermissionDenied, "permission_denied",
		"operator role has no rights for this action", nil))
}

// bearerToken extracts the token from Authorization: the Bearer scheme is
// case-insensitive, the token has no spaces inside.
func bearerToken(header string) (string, bool) {
	scheme, token, found := strings.Cut(strings.TrimSpace(header), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	if token == "" || strings.ContainsAny(token, " \t") {
		return "", false
	}
	return token, true
}
