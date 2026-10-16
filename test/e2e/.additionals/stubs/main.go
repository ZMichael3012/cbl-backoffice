// stubs serves the test PayoutService and ConnectorAdminService for the
// cbl-backoffice e2e scenarios (CBL-12.2): the stand stays autonomous, the
// back-office talks to deterministic in-memory doubles with the real contract
// surfaces. Never part of the production contour.
package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"

	"github.com/cbl/cbl-kit/grpcmw"

	connectorv1 "github.com/cbl/cbl-protos/gen/go/billing/connector/v1"
	payoutv1 "github.com/cbl/cbl-protos/gen/go/billing/payout/v1"
)

// main starts both stub servers and stops them on SIGINT/SIGTERM.
func main() {
	log, err := zap.NewProduction()
	if err != nil {
		panic(err)
	}
	defer func() { _ = log.Sync() }()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	payoutSrv := grpc.NewServer(grpc.UnaryInterceptor(grpcmw.UnaryServer(log)))
	payoutv1.RegisterPayoutServiceServer(payoutSrv, &payoutStub{
		required: intEnv("STUB_PAYOUT_REQUIRED", 2),
		states:   map[string]payoutv1.PayoutState{},
		votes:    map[string]map[string]bool{},
	})
	registerHealth(payoutSrv)

	connectorSrv := grpc.NewServer(grpc.UnaryInterceptor(grpcmw.UnaryServer(log)))
	connectorv1.RegisterConnectorAdminServiceServer(connectorSrv, &connectorStub{
		registry: splitCSV(os.Getenv("STUB_CONNECTOR_PROVIDERS")),
	})
	registerHealth(connectorSrv)

	payoutAddr := getenv("STUB_PAYOUT_ADDR", ":9081")
	connectorAddr := getenv("STUB_CONNECTOR_ADDR", ":9082")
	payoutListener, err := net.Listen("tcp", payoutAddr)
	if err != nil {
		log.Error("payout stub listen failed", zap.String("addr", payoutAddr), zap.Error(err))
		os.Exit(1)
	}
	connectorListener, err := net.Listen("tcp", connectorAddr)
	if err != nil {
		log.Error("connector stub listen failed", zap.String("addr", connectorAddr), zap.Error(err))
		os.Exit(1)
	}

	go func() {
		log.Info("payout stub started", zap.String("addr", payoutAddr))
		if serveErr := payoutSrv.Serve(payoutListener); serveErr != nil {
			log.Error("payout stub failed", zap.Error(serveErr))
			stop()
		}
	}()
	go func() {
		log.Info("connector stub started", zap.String("addr", connectorAddr))
		if serveErr := connectorSrv.Serve(connectorListener); serveErr != nil {
			log.Error("connector stub failed", zap.Error(serveErr))
			stop()
		}
	}()

	<-ctx.Done()
	log.Info("stub shutdown started")
	payoutSrv.GracefulStop()
	connectorSrv.GracefulStop()
	log.Info("stub shutdown finished")
}

// payoutStub implements the PayoutService semantics needed by the scenario:
// two distinct operators approve, a rejection is a veto, a repeated vote is
// idempotent, a changed decision and a decided payout are FailedPrecondition;
// payout ids starting with "small-" report "approval not required".
type payoutStub struct {
	payoutv1.UnimplementedPayoutServiceServer
	mu       sync.Mutex
	required int
	states   map[string]payoutv1.PayoutState
	votes    map[string]map[string]bool
}

// CreatePayout returns a fresh requested payout.
func (s *payoutStub) CreatePayout(_ context.Context, req *payoutv1.CreatePayoutRequest) (*payoutv1.CreatePayoutResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := "po-stub-" + req.GetMerchantId()
	s.states[id] = payoutv1.PayoutState_PAYOUT_STATE_REQUESTED
	return &payoutv1.CreatePayoutResponse{PayoutId: id, State: payoutv1.PayoutState_PAYOUT_STATE_REQUESTED}, nil
}

// GetPayout returns the current stub state.
func (s *payoutStub) GetPayout(_ context.Context, req *payoutv1.GetPayoutRequest) (*payoutv1.GetPayoutResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return &payoutv1.GetPayoutResponse{Payout: s.view(req.GetPayoutId())}, nil
}

// ApprovePayout records a vote with the 10.5 semantics.
func (s *payoutStub) ApprovePayout(_ context.Context, req *payoutv1.ApprovePayoutRequest) (*payoutv1.ApprovePayoutResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := req.GetPayoutId()
	operator := req.GetOperatorId()
	if strings.HasPrefix(id, "small-") {
		return nil, status.Error(codes.FailedPrecondition, "approval not required")
	}
	if operator == "" {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	state := s.states[id]
	if state == payoutv1.PayoutState_PAYOUT_STATE_UNSPECIFIED {
		state = payoutv1.PayoutState_PAYOUT_STATE_REQUESTED
	}
	votes := s.votes[id]
	if votes == nil {
		votes = map[string]bool{}
		s.votes[id] = votes
	}
	if decided, ok := votes[operator]; ok {
		if decided != req.GetApprove() {
			return nil, status.Error(codes.FailedPrecondition, "operator vote is immutable")
		}
	} else {
		if state != payoutv1.PayoutState_PAYOUT_STATE_REQUESTED {
			return nil, status.Error(codes.FailedPrecondition, "payout already decided")
		}
		votes[operator] = req.GetApprove()
		switch {
		case !req.GetApprove():
			state = payoutv1.PayoutState_PAYOUT_STATE_REJECTED
		case s.approvals(votes) >= s.required:
			state = payoutv1.PayoutState_PAYOUT_STATE_APPROVED
		}
	}
	s.states[id] = state
	return &payoutv1.ApprovePayoutResponse{Payout: s.view(id)}, nil
}

// view builds the payout message of the current state.
func (s *payoutStub) view(id string) *payoutv1.Payout {
	state := s.states[id]
	if state == payoutv1.PayoutState_PAYOUT_STATE_UNSPECIFIED {
		state = payoutv1.PayoutState_PAYOUT_STATE_REQUESTED
	}
	return &payoutv1.Payout{
		PayoutId:   id,
		State:      state,
		ApprovedBy: s.approvers(id),
	}
}

// approvals counts the positive votes.
func (s *payoutStub) approvals(votes map[string]bool) int {
	count := 0
	for _, approve := range votes {
		if approve {
			count++
		}
	}
	return count
}

// approvers lists the operators with a positive vote, sorted.
func (s *payoutStub) approvers(id string) []string {
	operators := make([]string, 0, len(s.votes[id]))
	for operator, approve := range s.votes[id] {
		if approve {
			operators = append(operators, operator)
		}
	}
	sort.Strings(operators)
	return operators
}

// connectorStub implements the ConnectorAdminService in memory: the revision
// counter, the rule set and the disabled providers.
type connectorStub struct {
	connectorv1.UnimplementedConnectorAdminServiceServer
	mu       sync.Mutex
	registry []string
	revision int64
	items    []*connectorv1.RoutingRuleAdmin
	disabled map[string]bool
}

// GetRoutingRules returns the current revision and rules.
func (s *connectorStub) GetRoutingRules(context.Context, *connectorv1.GetRoutingRulesRequest) (*connectorv1.GetRoutingRulesResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return &connectorv1.GetRoutingRulesResponse{Revision: s.revision, Items: s.items}, nil
}

// PutRoutingRules replaces the configuration with optimistic concurrency.
func (s *connectorStub) PutRoutingRules(_ context.Context, req *connectorv1.PutRoutingRulesRequest) (*connectorv1.PutRoutingRulesResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if req.GetRevision() != s.revision {
		return nil, status.Error(codes.FailedPrecondition, "routing revision conflict")
	}
	for _, item := range req.GetItems() {
		if strings.TrimSpace(item.GetProvider()) == "" {
			return nil, status.Error(codes.InvalidArgument, "invalid rules")
		}
	}
	s.items = req.GetItems()
	s.revision++
	return &connectorv1.PutRoutingRulesResponse{Revision: s.revision, Items: s.items}, nil
}

// DisableProvider disables a known provider; the repeat reports already_disabled.
func (s *connectorStub) DisableProvider(_ context.Context, req *connectorv1.DisableProviderRequest) (*connectorv1.DisableProviderResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	provider := req.GetProvider()
	if !s.known(provider) {
		return nil, status.Error(codes.NotFound, "provider not found")
	}
	already := s.disabled[provider]
	if s.disabled == nil {
		s.disabled = map[string]bool{}
	}
	s.disabled[provider] = true
	return &connectorv1.DisableProviderResponse{AlreadyDisabled: already}, nil
}

// known reports whether the provider is in the seeded registry or the rules.
func (s *connectorStub) known(provider string) bool {
	for _, name := range s.registry {
		if name == provider {
			return true
		}
	}
	for _, item := range s.items {
		if item.GetProvider() == provider {
			return true
		}
	}
	return false
}

// registerHealth attaches the gRPC health service to a stub server.
func registerHealth(server *grpc.Server) {
	healthSrv := health.NewServer()
	healthSrv.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)
	grpc_health_v1.RegisterHealthServer(server, healthSrv)
}

// getenv returns the environment variable value or the fallback.
func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// intEnv parses an integer environment variable with a fallback.
func intEnv(key string, fallback int) int {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	var value int
	if _, err := fmt.Sscanf(raw, "%d", &value); err != nil || value < 0 {
		return fallback
	}
	return value
}

// splitCSV parses a comma-separated list.
func splitCSV(raw string) []string {
	parts := strings.Split(raw, ",")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			values = append(values, trimmed)
		}
	}
	return values
}
