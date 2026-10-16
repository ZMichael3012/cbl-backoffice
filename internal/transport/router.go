// Package transport assembles the HTTP surface of the back-office admin API:
// the service endpoints, the /admin/v1 group with operator auth and RBAC, and
// the audit journal handler (CBL-12.1).
package transport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/cbl/cbl-kit/ginmw"
	"github.com/cbl/cbl-kit/grpcmw"

	"github.com/cbl/cbl-backoffice/internal/domain"
	"github.com/cbl/cbl-backoffice/internal/transport/httpmw"
	"github.com/cbl/cbl-backoffice/internal/usecase"
)

// auditTimeFormat is the RFC 3339 UTC format with milliseconds of the contract.
const auditTimeFormat = "2006-01-02T15:04:05.000Z07:00"

// maxFilterValueLen bounds the exact-match filter inputs.
const maxFilterValueLen = 256

// Options collects the router dependencies.
type Options struct {
	// Log is the structured logger.
	Log *zap.Logger
	// Verify checks operator tokens.
	Verify httpmw.Verifier
	// Audit serves journal pages.
	Audit *usecase.AuditReader
	// Ping reports database readiness for /readyz.
	Ping func(ctx context.Context) error
}

// NewRouter builds the HTTP router of the service.
func NewRouter(opts Options) *gin.Engine {
	router := gin.New()
	router.Use(ginmw.RequestID(), ginmw.AccessLog(opts.Log), ginmw.Recovery(opts.Log))

	router.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	router.GET("/readyz", func(c *gin.Context) {
		readyCtx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
		defer cancel()
		if opts.Ping != nil {
			if err := opts.Ping(readyCtx); err != nil {
				c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unavailable"})
				return
			}
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	admin := router.Group("/admin/v1")
	admin.Use(httpmw.Authn(opts.Verify))
	admin.GET("/audit-log", httpmw.Require(domain.PermissionAuditRead), auditListHandler(opts.Audit))

	router.NoRoute(func(c *gin.Context) {
		ginmw.AbortWithProblem(c, http.StatusNotFound, "not_found", "Not Found", "")
	})
	return router
}

// auditRecordJSON is the wire form of one journal record.
type auditRecordJSON struct {
	ID           string          `json:"id"`
	OccurredAt   string          `json:"occurred_at"`
	OperatorID   string          `json:"operator_id"`
	Action       string          `json:"action"`
	ResourceType string          `json:"resource_type"`
	ResourceID   string          `json:"resource_id"`
	RequestID    string          `json:"request_id"`
	Before       json.RawMessage `json:"before"`
	After        json.RawMessage `json:"after"`
}

// auditPageJSON is the wire form of a journal page.
type auditPageJSON struct {
	Items      []auditRecordJSON `json:"items"`
	NextCursor string            `json:"next_cursor"`
}

// auditListHandler serves GET /admin/v1/audit-log.
func auditListHandler(reader *usecase.AuditReader) gin.HandlerFunc {
	return func(c *gin.Context) {
		filter, limit, cursor, err := parseAuditQuery(c)
		if err != nil {
			AbortValidation(c, err.Error())
			return
		}
		page, err := reader.List(c.Request.Context(), filter, limit, cursor)
		if err != nil {
			switch grpcmw.KindOf(err) {
			case grpcmw.KindInvalidArgument:
				AbortValidation(c, problemMessage(err))
			case grpcmw.KindUnavailable:
				c.Header("Retry-After", "1")
				ginmw.AbortWithProblem(c, http.StatusServiceUnavailable, "service_unavailable",
					"Service Unavailable", problemMessage(err))
			default:
				ginmw.AbortFromError(c, err)
			}
			return
		}
		c.JSON(http.StatusOK, auditPageJSON{
			Items:      auditItemsJSON(page.Items),
			NextCursor: page.NextCursor,
		})
	}
}

// parseAuditQuery extracts and validates the journal query parameters; errors
// are plain validation messages for the 400 envelope.
func parseAuditQuery(c *gin.Context) (domain.AuditFilter, int, string, error) {
	var filter domain.AuditFilter
	filter.OperatorID = strings.TrimSpace(c.Query("operator_id"))
	filter.Action = strings.TrimSpace(c.Query("action"))
	if len(filter.OperatorID) > maxFilterValueLen || len(filter.Action) > maxFilterValueLen {
		return filter, 0, "", errors.New("filter value is too long")
	}

	from, err := parseTimeParam(c.Query("period_from"))
	if err != nil {
		return filter, 0, "", errors.New("period_from must be an RFC 3339 timestamp")
	}
	to, err := parseTimeParam(c.Query("period_to"))
	if err != nil {
		return filter, 0, "", errors.New("period_to must be an RFC 3339 timestamp")
	}
	if from != nil && to != nil && to.Before(*from) {
		return filter, 0, "", errors.New("period_to must not be earlier than period_from")
	}
	filter.PeriodFrom = from
	filter.PeriodTo = to

	limit := 0
	if raw := c.Query("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			return filter, 0, "", errors.New("limit must be between 1 and 100")
		}
		limit = parsed
	}
	return filter, limit, c.Query("cursor"), nil
}

// parseTimeParam parses an optional RFC 3339 bound into UTC.
func parseTimeParam(raw string) (*time.Time, error) {
	if raw == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, err
	}
	utc := parsed.UTC()
	return &utc, nil
}

// auditItemsJSON maps the domain page for the response.
func auditItemsJSON(items []domain.AuditRecord) []auditRecordJSON {
	out := make([]auditRecordJSON, 0, len(items))
	for _, item := range items {
		out = append(out, auditRecordJSON{
			ID:           item.ID,
			OccurredAt:   item.OccurredAt.UTC().Format(auditTimeFormat),
			OperatorID:   item.OperatorID,
			Action:       item.Action,
			ResourceType: item.ResourceType,
			ResourceID:   item.ResourceID,
			RequestID:    item.RequestID,
			Before:       item.Before,
			After:        item.After,
		})
	}
	return out
}

// AbortValidation responds 400 validation_failed with a safe detail.
func AbortValidation(c *gin.Context, detail string) {
	ginmw.AbortWithProblem(c, http.StatusBadRequest, "validation_failed", "Bad Request", detail)
}

// problemMessage extracts the exposed message of a domain error.
func problemMessage(err error) string {
	var target *grpcmw.Error
	if errors.As(err, &target) {
		return target.Message
	}
	return ""
}
