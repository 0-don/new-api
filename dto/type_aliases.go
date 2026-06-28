package dto

import "github.com/QuantumNous/new-api/types"

// Type aliases for backward compatibility.
// These types were moved from dto to types to break the dto↔model import cycle.

type UserSetting = types.UserSetting
type ChannelSettings = types.ChannelSettings
type ChannelOtherSettings = types.ChannelOtherSettings
type AdvancedCustomConfig = types.AdvancedCustomConfig
type AdvancedCustomRoute = types.AdvancedCustomRoute
type AdvancedCustomRouteAuth = types.AdvancedCustomRouteAuth
type VertexKeyType = types.VertexKeyType
type AwsKeyType = types.AwsKeyType
type OpenAIVideo = types.OpenAIVideo
type OpenAIVideoError = types.OpenAIVideoError

// Re-export constants.
var (
	NotifyTypeEmail   = types.NotifyTypeEmail
	NotifyTypeWebhook = types.NotifyTypeWebhook
	NotifyTypeBark    = types.NotifyTypeBark
	NotifyTypeGotify  = types.NotifyTypeGotify
)

const (
	VideoStatusUnknown    = types.VideoStatusUnknown
	VideoStatusQueued     = types.VideoStatusQueued
	VideoStatusInProgress = types.VideoStatusInProgress
	VideoStatusCompleted  = types.VideoStatusCompleted
	VideoStatusFailed     = types.VideoStatusFailed
)

var (
	VertexKeyTypeAPIKey = types.VertexKeyTypeAPIKey
	AwsKeyTypeApiKey    = types.AwsKeyTypeApiKey
)

const (
	AdvancedCustomConverterNone                                         = types.AdvancedCustomConverterNone
	AdvancedCustomConverterAnthropicMessagesToOpenAIChatCompletions     = types.AdvancedCustomConverterAnthropicMessagesToOpenAIChatCompletions
	AdvancedCustomConverterOpenAIChatCompletionsToAnthropicMessages     = types.AdvancedCustomConverterOpenAIChatCompletionsToAnthropicMessages
	AdvancedCustomConverterOpenAIChatCompletionsToOpenAIResponses       = types.AdvancedCustomConverterOpenAIChatCompletionsToOpenAIResponses
	AdvancedCustomConverterOpenAIResponsesToOpenAIChatCompletions       = types.AdvancedCustomConverterOpenAIResponsesToOpenAIChatCompletions
	AdvancedCustomConverterGeminiGenerateContentToOpenAIChatCompletions = types.AdvancedCustomConverterGeminiGenerateContentToOpenAIChatCompletions
	AdvancedCustomConverterOpenAIChatCompletionsToGeminiGenerateContent = types.AdvancedCustomConverterOpenAIChatCompletionsToGeminiGenerateContent
)

const (
	AdvancedCustomAuthTypeNone   = types.AdvancedCustomAuthTypeNone
	AdvancedCustomAuthTypeHeader = types.AdvancedCustomAuthTypeHeader
	AdvancedCustomAuthTypeQuery  = types.AdvancedCustomAuthTypeQuery
)

var NewOpenAIVideo = types.NewOpenAIVideo
