package types

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestWithOpenAIErrorChannelFaultReclassify locks the disable/failover contract for
// WithOpenAIError: a message that means THIS channel is broken (dead key, drained
// balance, missing model) must be reclassified to a channel: code so it fails over
// and auto-disables, while a genuine client-request 400 must stay a deterministic
// error that never bans a healthy channel.
func TestWithOpenAIErrorChannelFaultReclassify(t *testing.T) {
	cases := []struct {
		name        string
		message     string
		status      int
		wantChannel bool // errorCode has channel: prefix -> IsChannelError true
		wantCode    ErrorCode
	}{
		// Model-availability 400s: channel fault -> failover + disable.
		{"model_currently_unavailable", "Model 'qwen3-235b' is currently unavailable.", http.StatusBadRequest, true, ErrorCodeChannelModelMappedError},
		{"model_not_found_available", "Model 'jina-embeddings-v4:free' not found. Available models: ...", http.StatusBadRequest, true, ErrorCodeChannelModelMappedError},
		{"model_does_not_exist", "The model `bge-multilingual-gemma2:free` does not exist.", http.StatusBadRequest, true, ErrorCodeChannelModelMappedError},

		// Generic client-request 400s: MUST stay deterministic (no channel: code),
		// else one bad request bans a healthy channel. These are the regression guards.
		{"extra_inputs", "1 validation error for SystemMessage: Extra inputs are not permitted", http.StatusBadRequest, false, ""},
		{"unsupported_param", "Unsupported parameter: 'top_p' is not supported with this model.", http.StatusBadRequest, false, ""},
		{"system_message_order", "System message must be at the beginning.", http.StatusBadRequest, false, ""},
		{"context_window", "Context window exceeded: maximum is 32000 tokens.", http.StatusBadRequest, false, ""},

		// Transient infra "unavailable" must NOT be reclassified by the model set.
		{"transient_temporarily", "Upstream provider temporarily unavailable.", http.StatusServiceUnavailable, false, ""},
		{"transient_provider_currently", "The upstream provider is currently unavailable", http.StatusUnauthorized, false, ""},

		// Credential faults still reclassify (no regression on the existing branch).
		{"api_key_not_valid", "API key not valid. Please pass a valid API key.", http.StatusBadRequest, true, ErrorCodeChannelInvalidKey},
		{"reseller_quota_403", "用户额度不足, 剩余额度: ¥-0.001984", http.StatusForbidden, true, ErrorCodeChannelInvalidKey},
		// Bailian free-tier exhaustion (verified live body): 403 -> disable + failover.
		{"bailian_free_quota_403", `The free quota has been exhausted. To continue accessing the model on a paid basis, please complete your payment information (or disable the "use free tier only" mode in the management console if already completed)`, http.StatusForbidden, true, ErrorCodeChannelInvalidKey},
	}

	// The keyword list is a DB-backed option owned by operation_setting; wire the
	// provider with the shipped seed defaults.
	prev := ChannelFaultKeywordsProvider
	ChannelFaultKeywordsProvider = func() []string {
		return []string{
			"api key not valid",
			"api key expired",
			"api_key_invalid",
			"external billing pre-consume: insufficient balance",
			"用户额度不足",
			"剩余额度",
			"the free quota has been exhausted",
			"免费额度已用尽",
		}
	}
	t.Cleanup(func() { ChannelFaultKeywordsProvider = prev })

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := WithOpenAIError(OpenAIError{Message: tc.message, Code: "unknown_error"}, tc.status)
			assert.Equal(t, tc.wantChannel, IsChannelError(e), "IsChannelError for %q", tc.message)
			if tc.wantCode != "" {
				assert.Equal(t, tc.wantCode, e.GetErrorCode(), "errorCode for %q", tc.message)
			}
		})
	}
}
