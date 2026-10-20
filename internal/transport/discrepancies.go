package transport

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/cbl/cbl-kit/ginmw"
	"github.com/cbl/cbl-kit/grpcmw"

	"github.com/cbl/cbl-backoffice/internal/domain"
	"github.com/cbl/cbl-backoffice/internal/transport/httpmw"
)

// DiscrepancyAPI serves the reconciliation operator routes (CBL-12.3).
type DiscrepancyAPI interface {
	// List returns one page of the discrepancies.
	List(ctx context.Context, filter domain.DiscrepancyFilter, limit int, cursor string) (domain.DiscrepancyPage, error)
	// Resolve applies one operator decision.
	Resolve(ctx context.Context, operatorID, requestID, id, resolution, comment string) (domain.Discrepancy, error)
}

// discrepancyJSON is the wire form of the contract Discrepancy schema.
type discrepancyJSON struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	RefID      string `json:"ref_id"`
	MerchantID string `json:"merchant_id"`
	Severity   string `json:"severity"`
	Expected   string `json:"expected"`
	Actual     string `json:"actual"`
	Status     string `json:"status"`
	CreatedAt  string `json:"created_at"`
}

// discrepancyPageJSON is the wire form of one listing page.
type discrepancyPageJSON struct {
	Items      []discrepancyJSON `json:"items"`
	NextCursor string            `json:"next_cursor"`
}

// discrepancyResolveRequest is the body of the resolve route.
type discrepancyResolveRequest struct {
	OperatorID string `json:"operator_id"`
	Resolution string `json:"resolution"`
	Comment    string `json:"comment"`
}

// discrepancyListHandler serves GET /admin/v1/reconciliation/discrepancies.
func discrepancyListHandler(api DiscrepancyAPI) gin.HandlerFunc {
	return func(c *gin.Context) {
		filter := domain.DiscrepancyFilter{
			Severity:   strings.TrimSpace(c.Query("severity")),
			MerchantID: strings.TrimSpace(c.Query("merchant_id")),
		}
		limit, err := parseLimitValue(c.Query("limit"))
		if err != nil {
			AbortValidation(c, err.Error())
			return
		}
		page, err := api.List(c.Request.Context(), filter, limit, c.Query("cursor"))
		if err != nil {
			abortActionError(c, err)
			return
		}
		items := make([]discrepancyJSON, 0, len(page.Items))
		for _, item := range page.Items {
			items = append(items, discrepancyMessage(item))
		}
		c.JSON(http.StatusOK, discrepancyPageJSON{Items: items, NextCursor: page.NextCursor})
	}
}

// discrepancyResolveHandler serves POST
// /admin/v1/reconciliation/discrepancies/{id}/resolve.
func discrepancyResolveHandler(api DiscrepancyAPI) gin.HandlerFunc {
	return func(c *gin.Context) {
		operator := httpmw.OperatorFrom(c)
		var body discrepancyResolveRequest
		if !bindStrict(c, &body) {
			return
		}
		if strings.TrimSpace(body.OperatorID) == "" || strings.TrimSpace(body.OperatorID) != operator.Subject {
			ginmw.AbortFromError(c, grpcmw.E(grpcmw.KindPermissionDenied, "operator_mismatch",
				"operator_id must match the token subject", nil))
			return
		}
		resolved, err := api.Resolve(c.Request.Context(), operator.Subject, ginmw.RequestIDFrom(c), c.Param("id"),
			strings.TrimSpace(body.Resolution), body.Comment)
		if err != nil {
			abortActionError(c, err)
			return
		}
		c.JSON(http.StatusOK, discrepancyMessage(resolved))
	}
}

// discrepancyMessage maps one domain row onto the wire form.
func discrepancyMessage(item domain.Discrepancy) discrepancyJSON {
	return discrepancyJSON{
		ID:         item.ID,
		Type:       item.Type,
		RefID:      item.RefID,
		MerchantID: item.MerchantID,
		Severity:   item.Severity,
		Expected:   item.Expected,
		Actual:     item.Actual,
		Status:     item.Status,
		CreatedAt:  item.CreatedAt.UTC().Format(auditTimeFormat),
	}
}
