package controller

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"

	"github.com/go-fuego/fuego"
)

// GetChannelStatusHistory returns a paginated, filtered list of channel status
// transitions (newest first). Admin-only.
func GetChannelStatusHistory(c fuego.ContextWithParams[dto.GetChannelStatusHistoryParams]) (*dto.Response[dto.PageData[*model.ChannelStatusHistory]], error) {
	pageInfo := dto.PageInfo(c)
	p, _ := dto.ParseParams[dto.GetChannelStatusHistoryParams](c)
	filter := model.ChannelStatusHistoryFilter{
		ChannelId:      p.ChannelId,
		ToStatus:       p.ToStatus,
		TriggerSource:  p.TriggerSource,
		StatusCode:     p.StatusCode,
		ModelName:      p.ModelName,
		StartTimestamp: p.StartTimestamp,
		EndTimestamp:   p.EndTimestamp,
	}
	rows, total, err := model.GetChannelStatusHistory(filter, pageInfo.GetStartIdx(), pageInfo.GetPageSize())
	if err != nil {
		return dto.FailPage[*model.ChannelStatusHistory](err.Error())
	}
	return dto.OkPage(pageInfo, rows, int(total))
}

// GetChannelFlapStats returns per-channel transition / uptime aggregates over a
// time window, sorted by the requested dimension. Powers the "flappiest /
// worst-uptime / most-downtime" admin view. Admin-only.
func GetChannelFlapStats(c fuego.ContextWithParams[dto.GetChannelFlapStatsParams]) (*dto.Response[[]*model.ChannelFlapStatRow], error) {
	p, _ := dto.ParseParams[dto.GetChannelFlapStatsParams](c)
	since := p.StartTimestamp
	if since == 0 {
		since = common.GetTimestamp() - 7*24*3600
	}
	rows, err := model.ChannelFlapStats(since, p.OrderBy, p.Limit)
	if err != nil {
		return dto.Fail[[]*model.ChannelFlapStatRow](err.Error())
	}
	return dto.Ok(rows)
}

// PruneChannelStatusHistory deletes transitions older than a cutoff. Admin-only.
func PruneChannelStatusHistory(c fuego.ContextWithParams[dto.PruneChannelStatusHistoryParams]) (*dto.Response[int64], error) {
	p, _ := dto.ParseParams[dto.PruneChannelStatusHistoryParams](c)
	if p.BeforeTimestamp == 0 {
		return dto.Fail[int64]("before_timestamp is required")
	}
	deleted, err := model.PruneChannelStatusHistoryBefore(p.BeforeTimestamp)
	if err != nil {
		return dto.Fail[int64](err.Error())
	}
	return dto.Ok(deleted)
}
