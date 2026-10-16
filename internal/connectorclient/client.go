// Package connectorclient is the thin gRPC client of the ConnectorAdminService
// for the back-office routing and provider management (CBL-12.2).
package connectorclient

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/cbl/cbl-kit/grpcmw"

	connectorv1 "github.com/cbl/cbl-protos/gen/go/billing/connector/v1"

	"github.com/cbl/cbl-backoffice/internal/domain"
)

// Client calls the ConnectorAdminService.
type Client struct {
	conn    *grpc.ClientConn
	api     connectorv1.ConnectorAdminServiceClient
	timeout time.Duration
}

// New dials the connector admin service; the connection is plaintext inside the
// trusted network (mTLS - 0.6). The receive limit matches the connector server.
func New(addr string, timeout time.Duration) (*Client, error) {
	conn, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(1<<20)))
	if err != nil {
		return nil, fmt.Errorf("connector client: dial %s: %w", addr, err)
	}
	return &Client{conn: conn, api: connectorv1.NewConnectorAdminServiceClient(conn), timeout: timeout}, nil
}

// Close releases the connection.
func (c *Client) Close() error {
	return c.conn.Close()
}

// GetRules returns the current routing revision and every rule.
func (c *Client) GetRules(ctx context.Context) (int64, []domain.RoutingRule, error) {
	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	response, err := c.api.GetRoutingRules(callCtx, &connectorv1.GetRoutingRulesRequest{})
	if err != nil {
		return 0, nil, mapError(err)
	}
	return response.GetRevision(), fromProtoRules(response.GetItems()), nil
}

// PutRules replaces the routing configuration with optimistic concurrency.
func (c *Client) PutRules(ctx context.Context, revision int64, rules []domain.RoutingRule) (int64, []domain.RoutingRule, error) {
	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	response, err := c.api.PutRoutingRules(callCtx, &connectorv1.PutRoutingRulesRequest{
		Revision: revision,
		Items:    toProtoRules(rules),
	})
	if err != nil {
		return 0, nil, mapError(err)
	}
	return response.GetRevision(), fromProtoRules(response.GetItems()), nil
}

// DisableProvider disables a provider manually; already reports a repeat.
func (c *Client) DisableProvider(ctx context.Context, provider, reason string) (bool, error) {
	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	response, err := c.api.DisableProvider(callCtx, &connectorv1.DisableProviderRequest{
		Provider: provider,
		Reason:   reason,
	})
	if err != nil {
		return false, mapError(err)
	}
	return response.GetAlreadyDisabled(), nil
}

// mapError converts a transport error into the platform error kinds.
func mapError(err error) error {
	st, ok := status.FromError(err)
	if !ok {
		return grpcmw.E(grpcmw.KindInternal, "internal_error", "connector admin call failed", err)
	}
	switch st.Code() {
	case codes.NotFound:
		return grpcmw.E(grpcmw.KindNotFound, "provider_not_found", "provider not found", err)
	case codes.FailedPrecondition:
		return grpcmw.E(grpcmw.KindFailedPrecondition, "revision_conflict", "routing revision conflict", err)
	case codes.InvalidArgument:
		return grpcmw.E(grpcmw.KindInvalidArgument, "validation_failed", st.Message(), err)
	case codes.ResourceExhausted:
		// The payload exceeded the connector receive limit: an input problem,
		// not an internal failure (review finding).
		return grpcmw.E(grpcmw.KindInvalidArgument, "validation_failed", "payload is too large", err)
	case codes.Unavailable, codes.DeadlineExceeded:
		return grpcmw.E(grpcmw.KindUnavailable, "service_unavailable", "connector is unavailable", err)
	default:
		return grpcmw.E(grpcmw.KindInternal, "internal_error", "connector admin call failed", err)
	}
}

// toProtoRules converts domain rules into the contract form.
func toProtoRules(rules []domain.RoutingRule) []*connectorv1.RoutingRuleAdmin {
	items := make([]*connectorv1.RoutingRuleAdmin, 0, len(rules))
	for i := range rules {
		rule := rules[i]
		items = append(items, &connectorv1.RoutingRuleAdmin{
			Provider:  rule.Provider,
			Operation: rule.Operation,
			Networks:  rule.Networks,
			Assets:    rule.Assets,
			// The routing payload validation bounds priority and weight
			// below the int32 range (transport validateRoutingRequest).
			Priority:  int32(rule.Priority), //nolint:gosec // G115: bounded by payload validation
			Weight:    int32(rule.Weight),   //nolint:gosec // G115: bounded by payload validation
			MinAmount: rule.MinAmount,
			MaxAmount: rule.MaxAmount,
			Enabled:   rule.Enabled,
		})
	}
	return items
}

// fromProtoRules converts contract rules into the domain form.
func fromProtoRules(items []*connectorv1.RoutingRuleAdmin) []domain.RoutingRule {
	rules := make([]domain.RoutingRule, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		rules = append(rules, domain.RoutingRule{
			Provider:  item.GetProvider(),
			Operation: item.GetOperation(),
			Networks:  item.GetNetworks(),
			Assets:    item.GetAssets(),
			Priority:  int(item.GetPriority()),
			Weight:    int(item.GetWeight()),
			MinAmount: item.GetMinAmount(),
			MaxAmount: item.GetMaxAmount(),
			Enabled:   item.GetEnabled(),
		})
	}
	return rules
}
