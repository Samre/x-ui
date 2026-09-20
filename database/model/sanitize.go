package model

import (
	"encoding/json"
	"strings"
)

// 新版 Xray-core 移除了部分旧字段/取值。存量入站（尤其是从上游版本升级上来的库）里
// 仍带着这些内容，而配置生成是把库里的 JSON 原样下发的，直接启动 Xray 会报错。
// 这里在下发给 Xray 之前做一次清洗；注意：**只影响生成给 Xray 的 JSON，
// 不改写数据库中的原始配置**（数据库仍由前端"保存入站"时清洗）。
//
// 两个清洗函数都返回 (结果, 是否发生了清洗)；未发生清洗时逐字节原样返回。

// acceptProxyProtocol 所在的子节点（新版 tcp/ws 已不接受该字段）
var proxyProtocolSections = []string{"tcpSettings", "wsSettings", "rawSettings"}

// 已移除的 flow 取值；空 flow 也不再序列化（与前端行为一致）
var removedFlows = map[string]bool{
	"":                 true,
	"xtls-rprx-direct": true,
	"xtls-rprx-origin": true,
}

// sanitizeStreamSettings 删除 tcp/ws 子节点中遗留的 acceptProxyProtocol
func sanitizeStreamSettings(raw string) (string, bool) {
	if raw == "" || !strings.Contains(raw, "acceptProxyProtocol") {
		return raw, false
	}
	obj, ok := decodeJSONObject(raw)
	if !ok {
		return raw, false
	}
	changed := false
	for _, section := range proxyProtocolSections {
		sub, ok := obj[section].(map[string]interface{})
		if !ok {
			continue
		}
		if _, exists := sub["acceptProxyProtocol"]; exists {
			delete(sub, "acceptProxyProtocol")
			changed = true
		}
	}
	if !changed {
		return raw, false
	}
	return encodeJSON(obj, raw), true
}

// sanitizeSettings 删除 clients 中已废弃或为空的 flow（vision 等有效取值不动）
func sanitizeSettings(raw string) (string, bool) {
	if raw == "" || !strings.Contains(raw, "flow") {
		return raw, false
	}
	obj, ok := decodeJSONObject(raw)
	if !ok {
		return raw, false
	}
	clients, ok := obj["clients"].([]interface{})
	if !ok {
		return raw, false
	}
	changed := false
	for _, item := range clients {
		client, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		if flow, ok := client["flow"].(string); ok && removedFlows[flow] {
			delete(client, "flow")
			changed = true
		}
	}
	if !changed {
		return raw, false
	}
	return encodeJSON(obj, raw), true
}

// decodeJSONObject 解析顶层为对象的 JSON。UseNumber 保留数字原始写法，
// 避免大整数被转成浮点后写回（例如 alterId）。
func decodeJSONObject(raw string) (map[string]interface{}, bool) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var obj map[string]interface{}
	if err := decoder.Decode(&obj); err != nil || obj == nil {
		return nil, false
	}
	return obj, true
}

func encodeJSON(obj map[string]interface{}, fallback string) string {
	data, err := json.Marshal(obj)
	if err != nil {
		return fallback
	}
	return string(data)
}
