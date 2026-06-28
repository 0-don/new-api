package operation_setting

import (
	"os"
	"strconv"

	"github.com/QuantumNous/new-api/setting/config"
)

type MonitorSetting struct {
	AutoTestChannelEnabled           bool    `json:"auto_test_channel_enabled"`
	AutoTestChannelMinutes           float64 `json:"auto_test_channel_minutes"`
	AutoTestDisabledChannelsOnly     bool    `json:"auto_test_disabled_channels_only"`
	ChannelTestMode                  string  `json:"channel_test_mode"`
	ChannelStatusNotifyEnabled       bool    `json:"channel_status_notify_enabled"`
	SnapshotModelStatusEnabled       bool    `json:"snapshot_model_status_enabled"`
	SnapshotModelStatusRetentionDays int     `json:"snapshot_model_status_retention_days"`
}

const (
	ChannelTestModeScheduledAll    = "scheduled_all"
	ChannelTestModePassiveRecovery = "passive_recovery"
)

// 默认配置
var monitorSetting = MonitorSetting{
	AutoTestChannelEnabled:           false,
	AutoTestChannelMinutes:           10,
	AutoTestDisabledChannelsOnly:     false,
	ChannelTestMode:                  ChannelTestModeScheduledAll,
	ChannelStatusNotifyEnabled:       true,
	SnapshotModelStatusEnabled:       true,
	SnapshotModelStatusRetentionDays: 30,
}

func init() {
	// 注册到全局配置管理器
	config.GlobalConfig.Register("monitor_setting", &monitorSetting)
}

func GetMonitorSetting() *MonitorSetting {
	if os.Getenv("CHANNEL_TEST_FREQUENCY") != "" {
		frequency, err := strconv.Atoi(os.Getenv("CHANNEL_TEST_FREQUENCY"))
		if err == nil && frequency > 0 {
			monitorSetting.AutoTestChannelEnabled = true
			monitorSetting.AutoTestChannelMinutes = float64(frequency)
			monitorSetting.ChannelTestMode = ChannelTestModeScheduledAll
		}
	}
	if enabled, ok := os.LookupEnv("CHANNEL_TEST_ENABLED"); ok {
		parsed, err := strconv.ParseBool(enabled)
		if err == nil {
			monitorSetting.AutoTestChannelEnabled = parsed
		}
	}
	if monitorSetting.ChannelTestMode != ChannelTestModePassiveRecovery {
		monitorSetting.ChannelTestMode = ChannelTestModeScheduledAll
	}
	return &monitorSetting
}
