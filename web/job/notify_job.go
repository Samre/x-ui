package job

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"x-ui/logger"
	"x-ui/util/common"
	"x-ui/web/service"
)

type LoginStatus byte

const (
	LoginSuccess LoginStatus = 1
	LoginFail    LoginStatus = 0
)

// PushPlus 通知是事件驱动的：不再按 crontab 定时推送流量汇总，
// 而是在值得告警的时刻立即推送——登录成功/失败、入站因流量超限或到期被停用、
// Xray 进程退出。因此设置里不需要"通知时间"这一项。
type PushPlusNotifyJob struct {
	xrayService    service.XrayService
	inboundService service.InboundService
	settingService service.SettingService
}

func NewPushPlusNotifyJob() *PushPlusNotifyJob {
	return new(PushPlusNotifyJob)
}

// InboundAlert 描述一个因超限或到期而被停用的入站
type InboundAlert struct {
	Tag         string
	Remark      string
	Port        int
	Up          int64
	Down        int64
	Total       int64
	ExpiryTime  int64
	QuotaExceed bool
	Expired     bool
}

// SendPushPlusMsg 通过 pushplus 微信推送渠道发送消息
func (j *PushPlusNotifyJob) SendPushPlusMsg(msg string) {
	enabled, err := j.settingService.GetPushPlusEnable()
	if err != nil || !enabled {
		return
	}
	token, err := j.settingService.GetPushPlusToken()
	if err != nil {
		logger.Warning("sendMsgToPushPlus failed,GetPushPlusToken fail:", err)
		return
	}
	if token == "" {
		logger.Warning("sendMsgToPushPlus failed,pushPlusToken is empty")
		return
	}

	title := strings.TrimSpace(msg)
	if idx := strings.IndexAny(title, "\r\n"); idx > 0 {
		title = strings.TrimSpace(title[:idx])
	}

	postData, err := json.Marshal(map[string]string{
		"token":    token,
		"title":    title,
		"content":  msg,
		"template": "txt",
	})
	if err != nil {
		logger.Warning("sendMsgToPushPlus failed,marshal post data error:", err)
		return
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post("https://www.pushplus.plus/send", "application/json", bytes.NewBuffer(postData))
	if err != nil {
		logger.Warning("sendMsgToPushPlus failed,request pushplus error:", err)
		return
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		logger.Warning("sendMsgToPushPlus failed,read response error:", err)
		return
	}
	if resp.StatusCode != http.StatusOK {
		logger.Warning("sendMsgToPushPlus failed,http status:", resp.StatusCode, "body:", string(respBody))
		return
	}
	var result struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		logger.Warning("sendMsgToPushPlus failed,unmarshal response error:", err)
		return
	}
	if result.Code != 200 {
		logger.Warning("sendMsgToPushPlus failed,pushplus code:", result.Code, "msg:", result.Msg)
		return
	}
	logger.Info("sendMsgToPushPlus success")
}

// InboundDisabledNotify 在入站因流量超限或到期被自动停用时立即推送。
// 只列出本次被停用的入站，而不是全量快照——告警要能直接看出是谁出了问题。
func (j *PushPlusNotifyJob) InboundDisabledNotify(alerts []InboundAlert) {
	if len(alerts) == 0 {
		return
	}

	var msg strings.Builder
	name, err := os.Hostname()
	if err != nil {
		name = "unknown"
	}
	msg.WriteString(fmt.Sprintf("节点已停用提醒（共 %d 个）\r\n主机名称:%s\r\n", len(alerts), name))
	msg.WriteString(fmt.Sprintf("时间:%s\r\n\r\n", time.Now().Format("2006-01-02 15:04:05")))

	for _, a := range alerts {
		title := a.Remark
		if title == "" {
			title = a.Tag
		}
		reason := "已到期"
		switch {
		case a.QuotaExceed && a.Expired:
			reason = "流量超限且已到期"
		case a.QuotaExceed:
			reason = "流量超限"
		}

		msg.WriteString(fmt.Sprintf("节点:%s\r\n端口:%d\r\n停用原因:%s\r\n", title, a.Port, reason))
		if a.Total > 0 {
			msg.WriteString(fmt.Sprintf("流量:↑%s ↓%s / 限额 %s\r\n",
				common.FormatTraffic(a.Up), common.FormatTraffic(a.Down), common.FormatTraffic(a.Total)))
		} else {
			msg.WriteString(fmt.Sprintf("流量:↑%s ↓%s / 未限额\r\n",
				common.FormatTraffic(a.Up), common.FormatTraffic(a.Down)))
		}
		if a.ExpiryTime > 0 {
			msg.WriteString(fmt.Sprintf("到期时间:%s\r\n",
				time.Unix(a.ExpiryTime/1000, 0).Format("2006-01-02 15:04:05")))
		}
		msg.WriteString("\r\n")
	}

	j.SendPushPlusMsg(msg.String())
}

// XrayDownNotify 在 Xray 进程确认已退出时立即推送。
// 注意它是"持续 60 秒以上没能自行恢复"的告警：置位后 InboundController
// 每 10 秒会重试重启，能拉起来就不会走到这里。
func (j *PushPlusNotifyJob) XrayDownNotify(version string, exitErr error) {
	name, err := os.Hostname()
	if err != nil {
		name = "unknown"
	}
	if version == "" {
		version = "Unknown"
	}

	var msg strings.Builder
	msg.WriteString("Xray 进程已退出提醒\r\n")
	msg.WriteString(fmt.Sprintf("主机名称:%s\r\n", name))
	msg.WriteString(fmt.Sprintf("时间:%s\r\n", time.Now().Format("2006-01-02 15:04:05")))
	msg.WriteString(fmt.Sprintf("Xray 版本:%s\r\n", version))
	if exitErr != nil {
		msg.WriteString(fmt.Sprintf("退出错误:%v\r\n", exitErr))
	}
	msg.WriteString("面板已自动尝试重启但未成功，请登录面板确认 Xray 配置与二进制文件。\r\n")

	j.SendPushPlusMsg(msg.String())
}

// XrayRecoverNotify 在推送过退出告警之后、Xray 重新跑起来时推送一条闭环通知。
// 只有真的报过「退出」才会有这条：瞬时抖动（恰好落在重启窗口里）不会产生任何噪音。
func (j *PushPlusNotifyJob) XrayRecoverNotify(version string, downtime time.Duration) {
	name, err := os.Hostname()
	if err != nil {
		name = "unknown"
	}
	if version == "" {
		version = "Unknown"
	}
	if downtime < time.Second {
		downtime = 0
	}

	var msg strings.Builder
	msg.WriteString("Xray 已恢复运行\r\n")
	msg.WriteString(fmt.Sprintf("主机名称:%s\r\n", name))
	msg.WriteString(fmt.Sprintf("时间:%s\r\n", time.Now().Format("2006-01-02 15:04:05")))
	msg.WriteString(fmt.Sprintf("Xray 版本:%s\r\n", version))
	if downtime > 0 {
		msg.WriteString(fmt.Sprintf("中断时长:%s\r\n", downtime.Round(time.Second)))
	}

	j.SendPushPlusMsg(msg.String())
}

func (j *PushPlusNotifyJob) LoginNotify(username string, ip string, timeStr string, status LoginStatus) {
	if username == "" || ip == "" || timeStr == "" {
		logger.Warning("LoginNotify failed,invalid info")
		return
	}
	name, err := os.Hostname()
	if err != nil {
		name = "unknown"
	}

	var msg string
	if status == LoginSuccess {
		msg = fmt.Sprintf("面板登录成功提醒\r\n主机名称:%s\r\n", name)
	} else {
		msg = fmt.Sprintf("面板登录失败提醒\r\n主机名称:%s\r\n", name)
	}
	msg += fmt.Sprintf("时间:%s\r\n", timeStr)
	msg += fmt.Sprintf("用户:%s\r\n", username)
	msg += fmt.Sprintf("IP:%s\r\n", ip)
	j.SendPushPlusMsg(msg)
}
