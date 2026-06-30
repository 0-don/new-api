package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting"
)

// CREEM prompt moderation gate for image/video generation (merchant-of-record
// content-safety requirement). Toggled from the Creem payment-gateway admin tab
// (setting.CreemModerationEnabled), reusing the stored setting.CreemApiKey.
//
// Fails closed: when enabled, any error (missing key, timeout, bad response,
// flag/deny decision) blocks the request rather than letting unmoderated
// content through.

const (
	creemModerationTimeout = 5 * time.Second
	creemModerationURL     = "https://api.creem.io/v1/moderation/prompt"
)

var (
	ErrCreemPromptDenied      = errors.New("creem.prompt_rejected")
	ErrCreemModerationFailure = errors.New("creem.moderation_unavailable")
)

func CreemModerationEnabled() bool {
	return setting.CreemModerationEnabled
}

type creemModerationRequest struct {
	Prompt     string `json:"prompt"`
	ExternalID string `json:"external_id,omitempty"`
}

type creemModerationResponse struct {
	Id       string `json:"id"`
	Object   string `json:"object"`
	Decision string `json:"decision"`
	Usage    struct {
		Units int `json:"units"`
	} `json:"usage"`
}

// AssertCreemPromptAllowed screens a generation prompt through CREEM before
// dispatch. No-op when disabled or the prompt is empty. Returns
// ErrCreemPromptDenied for a flag/deny decision and ErrCreemModerationFailure
// for any operational failure (both block the request).
func AssertCreemPromptAllowed(ctx context.Context, prompt, externalID string) error {
	if !CreemModerationEnabled() {
		return nil
	}
	if prompt == "" {
		return nil
	}

	apiKey := setting.CreemApiKey
	if apiKey == "" {
		common.SysError("CREEM moderation enabled but CreemApiKey is empty; failing closed")
		return ErrCreemModerationFailure
	}

	body, err := common.Marshal(creemModerationRequest{Prompt: prompt, ExternalID: externalID})
	if err != nil {
		common.SysError(fmt.Sprintf("CREEM moderation marshal failed: %s", err.Error()))
		return ErrCreemModerationFailure
	}

	reqCtx, cancel := context.WithTimeout(ctx, creemModerationTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, creemModerationURL, bytes.NewReader(body))
	if err != nil {
		common.SysError(fmt.Sprintf("CREEM moderation request build failed: %s", err.Error()))
		return ErrCreemModerationFailure
	}
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("content-type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		common.SysError(fmt.Sprintf("CREEM moderation call failed: %s", err.Error()))
		return ErrCreemModerationFailure
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		common.SysError(fmt.Sprintf("CREEM moderation returned http %d; failing closed", resp.StatusCode))
		return ErrCreemModerationFailure
	}

	var parsed creemModerationResponse
	if err := common.DecodeJson(resp.Body, &parsed); err != nil {
		common.SysError(fmt.Sprintf("CREEM moderation decode failed: %s", err.Error()))
		return ErrCreemModerationFailure
	}

	switch parsed.Decision {
	case "allow":
		return nil
	case "flag", "deny":
		return ErrCreemPromptDenied
	default:
		common.SysError(fmt.Sprintf("CREEM moderation unexpected decision %q; failing closed", parsed.Decision))
		return ErrCreemModerationFailure
	}
}
