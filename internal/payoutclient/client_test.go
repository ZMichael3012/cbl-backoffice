package payoutclient

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/cbl/cbl-kit/grpcmw"

	payoutv1 "github.com/cbl/cbl-protos/gen/go/billing/payout/v1"
)

func TestMapError(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		code     codes.Code
		wantKind grpcmw.Kind
		wantCode string
	}{
		{name: "not found", code: codes.NotFound, wantKind: grpcmw.KindNotFound, wantCode: "payout_not_found"},
		{name: "failed precondition", code: codes.FailedPrecondition, wantKind: grpcmw.KindFailedPrecondition, wantCode: "conflict"},
		{name: "aborted", code: codes.Aborted, wantKind: grpcmw.KindFailedPrecondition, wantCode: "state_conflict"},
		{name: "invalid argument", code: codes.InvalidArgument, wantKind: grpcmw.KindInvalidArgument, wantCode: "validation_failed"},
		{name: "unavailable", code: codes.Unavailable, wantKind: grpcmw.KindUnavailable, wantCode: "service_unavailable"},
		{name: "deadline", code: codes.DeadlineExceeded, wantKind: grpcmw.KindUnavailable, wantCode: "service_unavailable"},
		{name: "internal", code: codes.Internal, wantKind: grpcmw.KindInternal, wantCode: "internal_error"},
		{name: "unknown", code: codes.Unknown, wantKind: grpcmw.KindInternal, wantCode: "internal_error"},
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

func TestStateName(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "requested", stateName(payoutv1.PayoutState_PAYOUT_STATE_REQUESTED))
	assert.Equal(t, "approved", stateName(payoutv1.PayoutState_PAYOUT_STATE_APPROVED))
	assert.Equal(t, "rejected", stateName(payoutv1.PayoutState_PAYOUT_STATE_REJECTED))
	assert.Equal(t, "compensated", stateName(payoutv1.PayoutState_PAYOUT_STATE_COMPENSATED))
	assert.Equal(t, "unspecified", stateName(payoutv1.PayoutState_PAYOUT_STATE_UNSPECIFIED))
}
