package controller

import (
	"errors"
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// shouldChargeOnError decides whether a failed relay keeps the user's
// pre-consumed quota (no refund). The billing invariant under test: a user is
// never charged for a request the upstream did not actually process. Two classes
// must always be refunded regardless of the ChargeOnError toggle:
//   - local new_api_error failures (never reached an upstream)
//   - deterministic upstream rejections 400/415/422/451 (malformed request,
//     unsupported param, validation, policy block) - the upstream rejected up
//     front and did zero billable work. Charging here is a phantom charge
//     (regression guard for the uno233 gpt-5.5 "stream_options" 400 incident:
//     104 rejected requests kept ~34k quota each, $7.12, for $0 upstream cost).
//
// Only a genuinely upstream-processed failure (e.g. 500/429/502/503 after the
// provider started work) may keep quota, and only when ChargeOnError is enabled.
func TestShouldChargeOnError(t *testing.T) {
	upstream400 := func() *types.NewAPIError {
		return types.NewOpenAIError(errors.New("Unsupported parameter: stream_options"),
			types.ErrorCodeBadResponseStatusCode, http.StatusBadRequest)
	}
	upstream500 := func() *types.NewAPIError {
		return types.NewOpenAIError(errors.New("internal server error"),
			types.ErrorCodeBadResponseStatusCode, http.StatusInternalServerError)
	}
	upstream429 := func() *types.NewAPIError {
		return types.NewOpenAIError(errors.New("rate limited"),
			types.ErrorCodeBadResponseStatusCode, http.StatusTooManyRequests)
	}
	upstream451 := func() *types.NewAPIError {
		return types.NewOpenAIError(errors.New("blocked for legal reasons"),
			types.ErrorCodeBadResponseStatusCode, http.StatusUnavailableForLegalReasons)
	}
	local400 := func() *types.NewAPIError {
		return types.NewErrorWithStatusCode(errors.New("invalid request"),
			types.ErrorCodeBadResponseBody, http.StatusBadRequest)
	}

	cases := []struct {
		name          string
		chargeOnError bool
		err           func() *types.NewAPIError
		want          bool
	}{
		{"toggle off never charges, even upstream 500", false, upstream500, false},
		{"nil error never charges", true, func() *types.NewAPIError { return nil }, false},
		{"local new_api_error never charges", true, local400, false},
		{"deterministic upstream 400 never charges (phantom-charge guard)", true, upstream400, false},
		{"deterministic upstream 451 never charges", true, upstream451, false},
		{"upstream 500 charges (provider did work)", true, upstream500, true},
		{"upstream 429 charges (provider did work)", true, upstream429, true},
	}

	orig := operation_setting.GetQuotaSetting().ChargeOnError
	t.Cleanup(func() { operation_setting.GetQuotaSetting().ChargeOnError = orig })

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			operation_setting.GetQuotaSetting().ChargeOnError = tc.chargeOnError
			got := shouldChargeOnError(tc.err())
			assert.Equal(t, tc.want, got)
		})
	}
}

// Guards the precise relationship the refund path depends on: a deterministic
// upstream rejection IS classified as a deterministic error (so the no-charge
// branch fires) while a non-deterministic upstream failure is NOT. If this
// classification drifts, shouldChargeOnError silently over- or under-charges.
func TestDeterministicUpstreamErrorClassification(t *testing.T) {
	det := types.NewOpenAIError(errors.New("bad request"),
		types.ErrorCodeBadResponseStatusCode, http.StatusBadRequest)
	require.True(t, types.IsDeterministicUpstreamError(det))

	transient := types.NewOpenAIError(errors.New("server error"),
		types.ErrorCodeBadResponseStatusCode, http.StatusInternalServerError)
	require.False(t, types.IsDeterministicUpstreamError(transient))

	// A local 400 is request-side but not an UPSTREAM error, so it is excluded
	// from the deterministic-upstream set (it is refunded via the new_api_error
	// branch instead).
	local := types.NewErrorWithStatusCode(errors.New("invalid"),
		types.ErrorCodeBadResponseBody, http.StatusBadRequest)
	require.False(t, types.IsDeterministicUpstreamError(local))
}
