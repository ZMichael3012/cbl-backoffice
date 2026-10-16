// Package payoutclient is the thin gRPC client of the PayoutService for the
// back-office payout decisions (CBL-12.2): per-call deadlines and mapping of
// transport codes to the platform error kinds.
package payoutclient

import (
	"context"
	"fmt"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/cbl/cbl-kit/grpcmw"

	payoutv1 "github.com/cbl/cbl-protos/gen/go/billing/payout/v1"

	"github.com/cbl/cbl-backoffice/internal/domain"
)

// Client calls the PayoutService.
type Client struct {
	conn    *grpc.ClientConn
	api     payoutv1.PayoutServiceClient
	timeout time.Duration
}

// New dials the payout service; the connection is plaintext inside the trusted
// network (mTLS - 0.6). The receive limit matches the payout server.
func New(addr string, timeout time.Duration) (*Client, error) {
	conn, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(1<<20)))
	if err != nil {
		return nil, fmt.Errorf("payout client: dial %s: %w", addr, err)
	}
	return &Client{conn: conn, api: payoutv1.NewPayoutServiceClient(conn), timeout: timeout}, nil
}

// Close releases the connection.
func (c *Client) Close() error {
	return c.conn.Close()
}

// Approve records an operator vote: once the policy threshold is reached the
// payout becomes approved, a rejection moves it to rejected (10.5).
func (c *Client) Approve(ctx context.Context, payoutID, operatorID string, approve bool, reason string) (domain.PayoutView, error) {
	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	response, err := c.api.ApprovePayout(callCtx, &payoutv1.ApprovePayoutRequest{
		PayoutId:   payoutID,
		OperatorId: operatorID,
		Approve:    approve,
		Reason:     reason,
	})
	if err != nil {
		return domain.PayoutView{}, mapError(err)
	}
	payout := response.GetPayout()
	return domain.PayoutView{
		ID:         payout.GetPayoutId(),
		Status:     stateName(payout.GetState()),
		ApprovedBy: payout.GetApprovedBy(),
	}, nil
}

// mapError converts a transport error into the platform error kinds.
func mapError(err error) error {
	st, ok := status.FromError(err)
	if !ok {
		return grpcmw.E(grpcmw.KindInternal, "internal_error", "payout service call failed", err)
	}
	switch st.Code() {
	case codes.NotFound:
		return grpcmw.E(grpcmw.KindNotFound, "payout_not_found", "payout not found", err)
	case codes.FailedPrecondition:
		return grpcmw.E(grpcmw.KindFailedPrecondition, "conflict", st.Message(), err)
	case codes.Aborted:
		return grpcmw.E(grpcmw.KindFailedPrecondition, "state_conflict", "state conflict, retry", err)
	case codes.InvalidArgument:
		return grpcmw.E(grpcmw.KindInvalidArgument, "validation_failed", st.Message(), err)
	case codes.Unavailable, codes.DeadlineExceeded:
		return grpcmw.E(grpcmw.KindUnavailable, "service_unavailable", "payout service is unavailable", err)
	default:
		return grpcmw.E(grpcmw.KindInternal, "internal_error", "payout service call failed", err)
	}
}

// stateName converts the contract enum into the canonical lowercase state.
func stateName(state payoutv1.PayoutState) string {
	return strings.ToLower(strings.TrimPrefix(state.String(), "PAYOUT_STATE_"))
}
