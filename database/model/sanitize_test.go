package model

import (
	"encoding/json"
	"strings"
	"testing"
)

// 无可清洗内容时必须是逐字节原样返回（避免影响现有生成结果）
func TestSanitizeKeepsRawWhenNothingToClean(t *testing.T) {
	stream := `{"network":"tcp","security":"none","tcpSettings":{"header":{"type":"none"}}}`
	if got, changed := sanitizeStreamSettings(stream); got != stream || changed {
		t.Fatalf("streamSettings 被改写:\n got=%s\nwant=%s", got, stream)
	}

	settings := `{"clients":[{"id":"a","flow":"xtls-rprx-vision"}],"decryption":"none"}`
	if got, changed := sanitizeSettings(settings); got != settings || changed {
		t.Fatalf("settings 被改写:\n got=%s\nwant=%s", got, settings)
	}

	for _, raw := range []string{"", "null", "{不是 JSON", `[1,2,3]`, "123"} {
		if got, changed := sanitizeStreamSettings(raw); got != raw || changed {
			t.Fatalf("非法/非对象输入应原样返回: in=%q got=%q changed=%v", raw, got, changed)
		}
		if got, changed := sanitizeSettings(raw); got != raw || changed {
			t.Fatalf("非法/非对象输入应原样返回: in=%q got=%q changed=%v", raw, got, changed)
		}
	}
}

// tcp/ws 里的 acceptProxyProtocol 必须被删除，其余字段原样保留
func TestSanitizeStreamSettingsRemovesProxyProtocol(t *testing.T) {
	raw := `{"network":"ws","security":"none","tcpSettings":{"acceptProxyProtocol":false,"header":{"type":"none"}},` +
		`"wsSettings":{"acceptProxyProtocol":true,"path":"/ws","headers":{"Host":"a.com"}}}`

	got, changed := sanitizeStreamSettings(raw)
	if !changed {
		t.Fatalf("应报告发生了清洗: %s", got)
	}
	if strings.Contains(got, "acceptProxyProtocol") {
		t.Fatalf("acceptProxyProtocol 未被删除: %s", got)
	}

	var obj map[string]interface{}
	if err := json.Unmarshal([]byte(got), &obj); err != nil {
		t.Fatalf("清洗后不是合法 JSON: %v (%s)", err, got)
	}
	tcp, _ := obj["tcpSettings"].(map[string]interface{})
	if tcp == nil {
		t.Fatalf("tcpSettings 丢失: %s", got)
	}
	header, _ := tcp["header"].(map[string]interface{})
	if header == nil || header["type"] != "none" {
		t.Fatalf("tcpSettings.header 被破坏: %s", got)
	}
	ws, _ := obj["wsSettings"].(map[string]interface{})
	if ws == nil || ws["path"] != "/ws" {
		t.Fatalf("wsSettings.path 被破坏: %s", got)
	}
	headers, _ := ws["headers"].(map[string]interface{})
	if headers == nil || headers["Host"] != "a.com" {
		t.Fatalf("wsSettings.headers 被破坏: %s", got)
	}
	if obj["network"] != "ws" || obj["security"] != "none" {
		t.Fatalf("顶层字段被破坏: %s", got)
	}
}

// clients 里已废弃/为空的 flow 删除，vision 等有效取值与其他字段保留
func TestSanitizeSettingsRemovesRemovedFlows(t *testing.T) {
	raw := `{"clients":[{"id":"a","flow":"xtls-rprx-direct"},{"id":"b","flow":"xtls-rprx-origin"},` +
		`{"id":"c","flow":"xtls-rprx-vision"},{"id":"d","flow":""},{"id":"e"}],` +
		`"decryption":"none","fallbacks":[{"name":"x","dest":80,"xver":0}]}`

	got, changed := sanitizeSettings(raw)
	if !changed {
		t.Fatalf("应报告发生了清洗: %s", got)
	}
	if strings.Contains(got, "xtls-rprx-direct") || strings.Contains(got, "xtls-rprx-origin") {
		t.Fatalf("废弃 flow 未被删除: %s", got)
	}

	var obj struct {
		Clients    []map[string]interface{} `json:"clients"`
		Decryption string                   `json:"decryption"`
		Fallbacks  []map[string]interface{} `json:"fallbacks"`
	}
	if err := json.Unmarshal([]byte(got), &obj); err != nil {
		t.Fatalf("清洗后不是合法 JSON: %v (%s)", err, got)
	}
	if len(obj.Clients) != 5 {
		t.Fatalf("clients 数量变化: %d", len(obj.Clients))
	}
	if _, exists := obj.Clients[0]["flow"]; exists {
		t.Fatalf("xtls-rprx-direct 仍在: %v", obj.Clients[0])
	}
	if _, exists := obj.Clients[1]["flow"]; exists {
		t.Fatalf("xtls-rprx-origin 仍在: %v", obj.Clients[1])
	}
	if obj.Clients[2]["flow"] != "xtls-rprx-vision" {
		t.Fatalf("有效 flow 被误删: %v", obj.Clients[2])
	}
	if _, exists := obj.Clients[3]["flow"]; exists {
		t.Fatalf("空 flow 未被删除: %v", obj.Clients[3])
	}
	if obj.Clients[4]["id"] != "e" {
		t.Fatalf("无 flow 的客户端被破坏: %v", obj.Clients[4])
	}
	if obj.Decryption != "none" || len(obj.Fallbacks) != 1 {
		t.Fatalf("settings 其他字段被破坏: %s", got)
	}
}

// 数字必须原样保留（UseNumber），不能被转成浮点写法
func TestSanitizeSettingsKeepsBigNumbers(t *testing.T) {
	raw := `{"clients":[{"id":"a","alterId":1234567890123,"flow":"xtls-rprx-direct"}],"decryption":"none"}`
	got, changed := sanitizeSettings(raw)
	if !changed {
		t.Fatalf("应报告发生了清洗: %s", got)
	}
	if !strings.Contains(got, "1234567890123") {
		t.Fatalf("大整数被改写: %s", got)
	}
	if strings.Contains(got, "flow") {
		t.Fatalf("废弃 flow 未被删除: %s", got)
	}
}

// 端到端：生成下发给 Xray 的配置时完成清洗
func TestGenXrayInboundConfigSanitizes(t *testing.T) {
	inbound := &Inbound{
		Id:       1,
		Port:     443,
		Protocol: VLESS,
		Settings: `{"clients":[{"id":"a","flow":"xtls-rprx-origin"}],"decryption":"none"}`,
		StreamSettings: `{"network":"ws","security":"none",` +
			`"wsSettings":{"acceptProxyProtocol":true,"path":"/x"}}`,
		Tag: "inbound-443",
	}

	conf := inbound.GenXrayInboundConfig()
	if got := string(conf.Settings); strings.Contains(got, "flow") {
		t.Fatalf("settings 未清洗: %s", got)
	}
	if got := string(conf.StreamSettings); strings.Contains(got, "acceptProxyProtocol") {
		t.Fatalf("streamSettings 未清洗: %s", got)
	}
	if conf.Tag != "inbound-443" || conf.Port != 443 || string(conf.Protocol) != "vless" {
		t.Fatalf("基础字段被改变: %+v", conf)
	}

	// 干净配置必须完全不受影响
	clean := &Inbound{
		Id:             2,
		Port:           8443,
		Protocol:       Trojan,
		Settings:       `{"clients":[{"password":"p"}]}`,
		StreamSettings: `{"network":"tcp","security":"tls","tcpSettings":{"header":{"type":"none"}}}`,
		Tag:            "inbound-8443",
	}
	conf2 := clean.GenXrayInboundConfig()
	if string(conf2.Settings) != clean.Settings {
		t.Fatalf("干净 settings 被改写: %s", string(conf2.Settings))
	}
	if string(conf2.StreamSettings) != clean.StreamSettings {
		t.Fatalf("干净 streamSettings 被改写: %s", string(conf2.StreamSettings))
	}
}
