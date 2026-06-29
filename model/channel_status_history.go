package model

import (
	"regexp"
	"sort"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// Trigger sources for a channel status transition.
const (
	ChannelStatusTriggerLiveRequest    = "live_request"
	ChannelStatusTriggerScheduledTest  = "scheduled_test"
	ChannelStatusTriggerManual         = "manual"
	ChannelStatusTriggerByTag          = "by_tag"
	ChannelStatusTriggerBalance        = "balance"
)

// ChannelStatusHistory is one row per channel status transition (enable <->
// disable, manual or automatic). Unlike channels.other_info.status_reason
// (which is overwritten on every flip), this is an append-only fact table so we
// can answer "which channels flap most / have the worst uptime / what error
// keeps disabling channel X" with plain SQL. Written async from the single
// chokepoint UpdateChannelStatus; a failed insert never blocks the status flip.
type ChannelStatusHistory struct {
	Id                  int64  `json:"id" gorm:"primaryKey"`
	ChannelId           int    `json:"channel_id" gorm:"index;index:idx_csh_created_channel,priority:2"`
	ChannelName         string `json:"channel_name" gorm:"type:varchar(255)"`
	ChannelType         int    `json:"channel_type"`
	BaseURL             string `json:"base_url" gorm:"type:varchar(512)"`
	Group               string `json:"group" gorm:"type:varchar(255)"`
	FromStatus          int    `json:"from_status"`
	ToStatus            int    `json:"to_status" gorm:"index"`
	StatusReason        string `json:"status_reason" gorm:"type:text"`
	StatusCode          int    `json:"status_code" gorm:"index"`
	ErrorCode           string `json:"error_code" gorm:"type:varchar(64)"`
	ModelName           string `json:"model_name" gorm:"type:varchar(255);index"`
	TriggerSource       string `json:"trigger_source" gorm:"type:varchar(32);index"`
	ResponseTimeMs      int    `json:"response_time_ms"`
	MultiKeyIndex       int    `json:"multi_key_index"`
	SecondsInPrevStatus int64  `json:"seconds_in_prev_status"`
	CreatedAt           int64  `json:"created_at" gorm:"bigint;index:idx_csh_created_channel,priority:1"`
}

func (ChannelStatusHistory) TableName() string {
	return "channel_status_histories"
}

var statusCodeRe = regexp.MustCompile(`status_code=(\d+)`)

// statusTokenRe matches both the bare `status: VALUE` and the JSON
// `"status": "VALUE"` forms Google/Gemini-style errors use, capturing the token.
var statusTokenRe = regexp.MustCompile(`(?i)"?status"?\s*:\s*"?([A-Za-z_][A-Za-z0-9_]*)`)

// ParseStatusReason pulls the numeric status_code and (best-effort) a short
// error-code token out of a channel status reason string such as
// "status_code=400, {error: {... status: INVALID_ARGUMENT}}". Returns (0, "")
// when nothing parseable is present (e.g. a clean enable with empty reason).
func ParseStatusReason(reason string) (int, string) {
	if reason == "" {
		return 0, ""
	}
	code := 0
	if m := statusCodeRe.FindStringSubmatch(reason); len(m) == 2 {
		code, _ = strconv.Atoi(m[1])
	}
	errCode := ""
	// Google-style status token: `status: INVALID_ARGUMENT` / `"status":"..."`.
	// The regex's leading [A-Za-z_] excludes a bare numeric "status: 400" (the
	// numeric code is already captured separately), keeping textual tokens only.
	if m := statusTokenRe.FindStringSubmatch(reason); len(m) == 2 && len(m[1]) <= 64 {
		errCode = m[1]
	}
	return code, errCode
}

// lastHistoryCreatedAt returns the created_at of the most recent transition for
// a channel, used to compute how long it sat in the previous status. 0 = no
// prior row.
func lastHistoryCreatedAt(channelId int) int64 {
	var ts int64
	DB.Model(&ChannelStatusHistory{}).
		Where("channel_id = ?", channelId).
		Order("created_at DESC").
		Limit(1).
		Pluck("created_at", &ts)
	return ts
}

// InsertChannelStatusHistory records one transition. Best-effort: callers run it
// async and a DB error must never propagate to the status flip. SecondsInPrev /
// CreatedAt are filled here when unset so callers stay terse.
func InsertChannelStatusHistory(row *ChannelStatusHistory) {
	if row == nil {
		return
	}
	if row.CreatedAt == 0 {
		row.CreatedAt = common.GetTimestamp()
	}
	if row.SecondsInPrevStatus == 0 {
		if prev := lastHistoryCreatedAt(row.ChannelId); prev > 0 && row.CreatedAt >= prev {
			row.SecondsInPrevStatus = row.CreatedAt - prev
		}
	}
	if row.StatusCode == 0 && row.ErrorCode == "" {
		row.StatusCode, row.ErrorCode = ParseStatusReason(row.StatusReason)
	}
	if err := DB.Create(row).Error; err != nil {
		common.SysLog("failed to insert channel status history: " + err.Error())
	}
}

// ChannelStatusHistoryFilter narrows a history list query. Zero-value fields are
// ignored.
type ChannelStatusHistoryFilter struct {
	ChannelId      int
	ToStatus       int
	TriggerSource  string
	StatusCode     int
	ModelName      string
	StartTimestamp int64
	EndTimestamp   int64
}

func (f ChannelStatusHistoryFilter) apply(db *gorm.DB) *gorm.DB {
	if f.ChannelId != 0 {
		db = db.Where("channel_id = ?", f.ChannelId)
	}
	if f.ToStatus != 0 {
		db = db.Where("to_status = ?", f.ToStatus)
	}
	if f.TriggerSource != "" {
		db = db.Where("trigger_source = ?", f.TriggerSource)
	}
	if f.StatusCode != 0 {
		db = db.Where("status_code = ?", f.StatusCode)
	}
	if f.ModelName != "" {
		db = db.Where("model_name = ?", f.ModelName)
	}
	if f.StartTimestamp != 0 {
		db = db.Where("created_at >= ?", f.StartTimestamp)
	}
	if f.EndTimestamp != 0 {
		db = db.Where("created_at <= ?", f.EndTimestamp)
	}
	return db
}

// GetChannelStatusHistory returns a paginated, filtered slice of transitions
// (newest first) plus the total count for the filter.
func GetChannelStatusHistory(filter ChannelStatusHistoryFilter, startIdx int, num int) ([]*ChannelStatusHistory, int64, error) {
	var total int64
	if err := filter.apply(DB.Model(&ChannelStatusHistory{})).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if total == 0 {
		return []*ChannelStatusHistory{}, 0, nil
	}
	var rows []*ChannelStatusHistory
	err := filter.apply(DB.Model(&ChannelStatusHistory{})).
		Order("created_at DESC, id DESC").
		Limit(num).Offset(startIdx).
		Find(&rows).Error
	return rows, total, err
}

// ChannelFlapStatRow is one channel's transition/uptime summary over a window.
// UptimePercent is computed in Go from the up/down second sums (AVG/SUM return
// portable types but the ratio is cleaner in Go).
type ChannelFlapStatRow struct {
	ChannelId     int    `json:"channel_id" gorm:"column:channel_id"`
	ChannelName   string `json:"channel_name" gorm:"column:channel_name"`
	BaseURL       string `json:"base_url" gorm:"column:base_url"`
	Transitions   int    `json:"transitions" gorm:"column:transitions"`
	DisableCount  int    `json:"disable_count" gorm:"column:disable_count"`
	EnableCount   int    `json:"enable_count" gorm:"column:enable_count"`
	SecondsUp     int64  `json:"seconds_up" gorm:"column:seconds_up"`
	SecondsDown   int64  `json:"seconds_down" gorm:"column:seconds_down"`
	UptimePercent float64 `json:"uptime_percent" gorm:"-"`
	LastToStatus  int    `json:"last_to_status" gorm:"column:last_to_status"`
}

// ChannelFlapStats aggregates per-channel transition counts and up/down time
// over [since, now]. Cross-DB: uses SUM(CASE WHEN ...) (no Postgres FILTER).
// seconds_up/down attribute the time spent in the PREVIOUS status to whichever
// status the channel was leaving: a transition arriving AT enabled (to_status=1)
// means it was DOWN for seconds_in_prev_status; arriving at a disabled status
// means it was UP. orderBy: "transitions" | "uptime" | "downtime" (default
// transitions). limit 0 = no limit.
func ChannelFlapStats(since int64, orderBy string, limit int) ([]*ChannelFlapStatRow, error) {
	enabled := common.ChannelStatusEnabled
	autoDis := common.ChannelStatusAutoDisabled
	manDis := common.ChannelStatusManuallyDisabled

	db := DB.Table("channel_status_histories").
		Select(`
			channel_id,
			MAX(channel_name) AS channel_name,
			MAX(base_url) AS base_url,
			COUNT(*) AS transitions,
			SUM(CASE WHEN to_status IN (?, ?) THEN 1 ELSE 0 END) AS disable_count,
			SUM(CASE WHEN to_status = ? THEN 1 ELSE 0 END) AS enable_count,
			SUM(CASE WHEN to_status = ? THEN seconds_in_prev_status ELSE 0 END) AS seconds_down,
			SUM(CASE WHEN to_status IN (?, ?) THEN seconds_in_prev_status ELSE 0 END) AS seconds_up,
			MAX(id) AS last_id
		`, autoDis, manDis, enabled, enabled, autoDis, manDis).
		Where("created_at >= ?", since).
		Group("channel_id")

	var rows []*ChannelFlapStatRow
	if err := db.Scan(&rows).Error; err != nil {
		return nil, err
	}

	// Resolve last_to_status per channel (the status of the most recent
	// transition in the window) and compute uptime%.
	for _, r := range rows {
		total := r.SecondsUp + r.SecondsDown
		if total > 0 {
			r.UptimePercent = float64(r.SecondsUp) * 100 / float64(total)
		} else {
			r.UptimePercent = 100
		}
		var lastStatus int
		DB.Model(&ChannelStatusHistory{}).
			Where("channel_id = ? AND created_at >= ?", r.ChannelId, since).
			Order("created_at DESC, id DESC").Limit(1).
			Pluck("to_status", &lastStatus)
		r.LastToStatus = lastStatus
	}

	sortFlapRows(rows, orderBy)
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

func sortFlapRows(rows []*ChannelFlapStatRow, orderBy string) {
	less := func(i, j int) bool { return rows[i].Transitions > rows[j].Transitions }
	switch orderBy {
	case "uptime":
		less = func(i, j int) bool { return rows[i].UptimePercent < rows[j].UptimePercent }
	case "downtime":
		less = func(i, j int) bool { return rows[i].SecondsDown > rows[j].SecondsDown }
	}
	sort.SliceStable(rows, less)
}

// PruneChannelStatusHistoryBefore deletes transitions older than a cutoff
// (retention).
func PruneChannelStatusHistoryBefore(beforeTs int64) (int64, error) {
	res := DB.Where("created_at < ?", beforeTs).Delete(&ChannelStatusHistory{})
	return res.RowsAffected, res.Error
}
