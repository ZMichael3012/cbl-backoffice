package httpmw

import (
	"github.com/gin-gonic/gin"

	"github.com/cbl/cbl-backoffice/internal/domain"
)

// Require returns the RBAC middleware: the authenticated operator must hold
// the permission, otherwise 403 (deny by default for every route).
func Require(permission domain.Permission) gin.HandlerFunc {
	return func(c *gin.Context) {
		operator := OperatorFrom(c)
		if !operator.Has(permission) {
			AbortPermissionDenied(c)
			return
		}
		c.Next()
	}
}
