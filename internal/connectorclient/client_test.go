package connectorclient

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/cbl/cbl-kit/grpcmw"

	connectorv1 "github.com/cbl/cbl-protos/gen/go/billing/connector/v1"

	"github.com/cbl/cbl-backoffice/internal/domain"
)

func TestMapError(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		code     codes.Code
		wantKind grpcmw.Kind
		wantCode string
	}{
		{name: "not found", code: codes.NotFound, wantKind: grpcmw.KindNotFound, wantCode: "provider_not_found"},
		{name: "failed precondition", code: codes.FailedPrecondition, wantKind: grpcmw.KindFailedPrecondition, wantCode: "revision_conflict"},
		{name: "invalid argument", code: codes.InvalidArgument, wantKind: grpcmw.KindInvalidArgument, wantCode: "validation_failed"},
		{name: "resource exhausted", code: codes.ResourceExhausted, wantKind: grpcmw.KindInvalidArgument, wantCode: "validation_failed"},
		{name: "unavailable", code: codes.Unavailable, wantKind: grpcmw.KindUnavailable, wantCode: "service_unavailable"},
		{name: "deadline", code: codes.DeadlineExceeded, wantKind: grpcmw.KindUnavailable, wantCode: "service_unavailable"},
		{name: "internal", code: codes.Internal, wantKind: grpcmw.KindInternal, wantCode: "internal_error"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mapped := mapError(status.Error(tc.code, "boom"))
			assert.Equal(t, tc.wantKind, grpcmw.KindOf(mapped))
			assert.Equal(t, tc.wantCode, grpcmw.CodeOf(mapped))
		})
	}

	mapped := mapError(errors.New("not a grpc error"))
	assert.Equal(t, grpcmw.KindInternal, grpcmw.KindOf(mapped))
}

func TestRuleConversionRoundTrip(t *testing.T) {
	t.Parallel()

	rules := []domain.RoutingRule{{
		Provider:  "mock",
		Operation: "invoice",
		Networks:  []string{"trc20"},
		Assets:    []string{"USDT"},
		Priority:  1,
		Weight:    3,
		MinAmount: 100,
		MaxAmount: 900,
		Enabled:   true,
	}}
	items := toProtoRules(rules)
	require := func(condition bool, message string) {
		if !condition {
			t.Fatalf("assertion failed: %s", message)
		}
	}
	require(len(items) == 1, "one protobuf item")
	require(items[0].GetProvider() == "mock", "provider")
	require(items[0].GetWeight() == 3, "weight")
	require(items[0].GetMinAmount() == 100, "min amount")

	back := fromProtoRules(items)
	assert.Equal(t, rules, back, "the round trip preserves every field")

	// Nil items are skipped defensively.
	fromNil := fromProtoRules([]*connectorv1.RoutingRuleAdmin{nil})
	assert.Empty(t, fromNil)
}
