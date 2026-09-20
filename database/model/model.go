package model

import (
	"fmt"
	"x-ui/logger"
	"x-ui/util/json_util"
	"x-ui/xray"
)

type Protocol string

const (
	VMess       Protocol = "vmess"
	VLESS       Protocol = "vless"
	Dokodemo    Protocol = "Dokodemo-door"
	Http        Protocol = "http"
	Trojan      Protocol = "trojan"
	Shadowsocks Protocol = "shadowsocks"
)

type User struct {
	Id       int    `json:"id" gorm:"primaryKey;autoIncrement"`
	Username string `json:"username"`
	Password string `json:"password"`
}

type Inbound struct {
	Id         int    `json:"id" form:"id" gorm:"primaryKey;autoIncrement"`
	UserId     int    `json:"-"`
	Up         int64  `json:"up" form:"up"`
	Down       int64  `json:"down" form:"down"`
	Total      int64  `json:"total" form:"total"`
	Remark     string `json:"remark" form:"remark"`
	Enable     bool   `json:"enable" form:"enable"`
	ExpiryTime int64  `json:"expiryTime" form:"expiryTime"`

	// config part
	Listen         string   `json:"listen" form:"listen"`
	Port           int      `json:"port" form:"port" gorm:"unique"`
	Protocol       Protocol `json:"protocol" form:"protocol"`
	Settings       string   `json:"settings" form:"settings"`
	StreamSettings string   `json:"streamSettings" form:"streamSettings"`
	Tag            string   `json:"tag" form:"tag" gorm:"unique"`
	Sniffing       string   `json:"sniffing" form:"sniffing"`
}

// GenXrayInboundConfig 把库里的入站配置转换成下发给 Xray 的配置。
// settings/streamSettings 在此做一次兼容清洗（去掉新版 Xray 已移除的字段），
// 但不修改数据库中的原始配置。
func (i *Inbound) GenXrayInboundConfig() *xray.InboundConfig {
	listen := i.Listen
	if listen != "" {
		listen = fmt.Sprintf("\"%v\"", listen)
	}
	settings, settingsCleaned := sanitizeSettings(i.Settings)
	streamSettings, streamCleaned := sanitizeStreamSettings(i.StreamSettings)
	if settingsCleaned || streamCleaned {
		logger.Warningf("入站 [%s] 含新版 Xray 已移除的字段(acceptProxyProtocol/废弃 flow)，已在下发配置时忽略，数据库原配置未改动", i.Tag)
	}
	return &xray.InboundConfig{
		Listen:         json_util.RawMessage(listen),
		Port:           i.Port,
		Protocol:       string(i.Protocol),
		Settings:       json_util.RawMessage(settings),
		StreamSettings: json_util.RawMessage(streamSettings),
		Tag:            i.Tag,
		Sniffing:       json_util.RawMessage(i.Sniffing),
	}
}

type Setting struct {
	Id    int    `json:"id" form:"id" gorm:"primaryKey;autoIncrement"`
	Key   string `json:"key" form:"key"`
	Value string `json:"value" form:"value"`
}

// TrafficSnapshot 每分钟落一次库的入站流量增量，用于流量统计报表
type TrafficSnapshot struct {
	Id         int    `json:"id" gorm:"primaryKey;autoIncrement"`
	InboundTag string `json:"inboundTag" gorm:"index:idx_traffic_snapshot_tag_time"`
	Up         int64  `json:"up"`
	Down       int64  `json:"down"`
	// 单独建索引：概览与清理都只按 created_at 过滤，
	// 与 InboundTag 共用同一索引名会变成 (inbound_tag, created_at) 复合索引而无法用于范围扫描
	CreatedAt int64 `json:"createdAt" gorm:"index:idx_traffic_snapshot_created_at"`
}
