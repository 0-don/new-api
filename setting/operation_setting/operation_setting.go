package operation_setting

import (
	"strings"

	"github.com/QuantumNous/new-api/types"
)

func init() {
	// Feed the admin-editable channel-fault keyword list into the types package
	// (which does the error reclassification but cannot import this package).
	types.ChannelFaultKeywordsProvider = func() []string { return ChannelFaultKeywords }
}

var DemoSiteEnabled = false
var SelfUseModeEnabled = false
var ShowOriginalPriceEnabled = false

var AutomaticDisableKeywords = []string{
	"Your credit balance is too low",
	"This organization has been disabled.",
	"You exceeded your current quota",
	"Permission denied",
	"The security token included in the request is invalid",
	"Operation not allowed",
	"Your account is not authorized",
}

// ChannelFaultKeywords: upstream error fragments that mean THIS channel is at
// fault (dead key, drained upstream wallet, exhausted free quota) rather than the
// client's request. Unlike AutomaticDisableKeywords (disable only), a match here
// on a 400/403 ALSO reclassifies the error to a channel fault so the SAME request
// fails over to a healthy sibling. DB-backed option: this literal only seeds the
// row on first boot, after that the DB list is authoritative (admin-editable,
// matched case-insensitively).
var ChannelFaultKeywords = []string{
	"api key not valid",
	"api key expired",
	"api_key_invalid",
	"external billing pre-consume: insufficient balance",
	"用户额度不足",
	"剩余额度",
	// Alibaba Bailian/DashScope free-tier quota exhausted (Stop-on-Exhaust).
	"the free quota has been exhausted",
	"免费额度已用尽",
}

func keywordsToString(kw []string) string {
	return strings.Join(kw, "\n")
}

func keywordsFromString(s string) []string {
	out := []string{}
	for _, k := range strings.Split(s, "\n") {
		k = strings.ToLower(strings.TrimSpace(k))
		if k != "" {
			out = append(out, k)
		}
	}
	return out
}

func AutomaticDisableKeywordsToString() string {
	return keywordsToString(AutomaticDisableKeywords)
}

func AutomaticDisableKeywordsFromString(s string) {
	AutomaticDisableKeywords = keywordsFromString(s)
}

func ChannelFaultKeywordsToString() string {
	return keywordsToString(ChannelFaultKeywords)
}

func ChannelFaultKeywordsFromString(s string) {
	ChannelFaultKeywords = keywordsFromString(s)
}
