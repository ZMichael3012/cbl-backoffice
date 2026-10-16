package httpmw_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cbl/cbl-kit/ginmw"
	"github.com/cbl/cbl-kit/grpcmw"

	"github.com/cbl/cbl-backoffice/internal/domain"
	"github.com/cbl/cbl-backoffice/internal/transport/httpmw"
)

// newRouter builds a router with request-id and the authn middleware.
func newRouter(verify httpmw.Verifier) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(ginmw.RequestID())
	router.GET("/admin/v1/audit-log", httpmw.Authn(verify), func(c *gin.Context) {
		operator := httpmw.OperatorFrom(c)
		c.JSON(http.StatusOK, gin.H{"subject": operator.Subject})
	})
	return router
}

// do performs one request against the router.
func do(router *gin.Engine, headers map[string][]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/admin/v1/audit-log", nil)
	for key, values := range headers {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestAuthnRejections(t *testing.T) {
	t.Parallel()

	verify := func(context.Context, string) (domain.Operator, error) {
		return domain.Operator{}, grpcmw.E(grpcmw.KindUnauthenticated, "unauthenticated", "bad", nil)
	}
	router := newRouter(verify)

	cases := []struct {
		name    string
		headers map[string][]string
	}{
		{name: "no header", headers: nil},
		{name: "two headers", headers: map[string][]string{"Authorization": {"Bearer a", "Bearer b"}}},
		{name: "wrong scheme", headers: map[string][]string{"Authorization": {"Basic abc"}}},
		{name: "empty token", headers: map[string][]string{"Authorization": {"Bearer "}}},
		{name: "space inside", headers: map[string][]string{"Authorization": {"Bearer a b"}}},
		{name: "verify rejection", headers: map[string][]string{"Authorization": {"Bearer whatever"}}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := do(router, tc.headers)
			require.Equal(t, http.StatusUnauthorized, rec.Code)
			assert.Equal(t, `Bearer realm="admin"`, rec.Header().Get("WWW-Authenticate"))

			var problem ginmw.Problem
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &problem))
			assert.Equal(t, "unauthenticated", problem.Code)
			assert.NotEmpty(t, problem.RequestID)
		})
	}
}

func TestAuthnSuccess(t *testing.T) {
	t.Parallel()

	verify := func(_ context.Context, token string) (domain.Operator, error) {
		if token != "good" {
			return domain.Operator{}, grpcmw.E(grpcmw.KindUnauthenticated, "unauthenticated", "bad", nil)
		}
		return domain.Operator{Subject: "op-1", Roles: []domain.Role{domain.RoleAuditor}}, nil
	}
	router := newRouter(verify)

	rec := do(router, map[string][]string{"Authorization": {"bearer good"}})
	require.Equal(t, http.StatusOK, rec.Code)

	var body map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "op-1", body["subject"])
}

func TestAuthnDependencyFailure(t *testing.T) {
	t.Parallel()

	verify := func(context.Context, string) (domain.Operator, error) {
		return domain.Operator{}, grpcmw.E(grpcmw.KindUnavailable, "service_unavailable", "down", nil)
	}
	router := newRouter(verify)

	rec := do(router, map[string][]string{"Authorization": {"Bearer good"}})
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestRequire(t *testing.T) {
	t.Parallel()

	verify := func(_ context.Context, token string) (domain.Operator, error) {
		roles := []domain.Role{domain.RolePayoutOperator}
		if token == "auditor" {
			roles = []domain.Role{domain.RoleAuditor}
		}
		return domain.Operator{Subject: "op-1", Roles: roles}, nil
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(ginmw.RequestID())
	router.GET("/audit", httpmw.Authn(verify), httpmw.Require(domain.PermissionAuditRead), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/audit", nil)
	req.Header.Set("Authorization", "Bearer payout")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusForbidden, rec.Code)
	var problem ginmw.Problem
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &problem))
	assert.Equal(t, "permission_denied", problem.Code)

	req = httptest.NewRequest(http.MethodGet, "/audit", nil)
	req.Header.Set("Authorization", "Bearer auditor")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}
