package job

import (
	"time"

	"x-ui/web/service"
)

type CheckXrayRunningJob struct {
	xrayService service.XrayService

	checkTime int
	// lastAlertedErr 记录已经推送过的退出原因，用于去重：
	// 进程一直挂着时（例如 xray 二进制缺失）每 30 秒推一条会变成骚扰，
	// 因此只在「首次确认退出」和「退出原因发生变化」时推送。
	// 进程恢复运行时清空，下次再挂会重新告警。
	lastAlertedErr string
	// alerted 表示当前这次故障已经推送过退出告警；
	// downSince 是首次确认退出的时刻，仅用于在恢复通知里报中断时长。
	alerted   bool
	downSince time.Time
}

func NewCheckXrayRunningJob() *CheckXrayRunningJob {
	return new(CheckXrayRunningJob)
}

func (j *CheckXrayRunningJob) Run() {
	if j.xrayService.IsXrayRunning() {
		// 只有此前真的推送过退出告警才发恢复通知：
		// 瞬时抖动（恰好落在重启窗口里）不会产生任何噪音。
		if j.alerted {
			new(PushPlusNotifyJob).XrayRecoverNotify(j.xrayService.GetXrayVersion(), time.Since(j.downSince))
		}
		j.alerted = false
		j.downSince = time.Time{}
		j.checkTime = 0
		j.lastAlertedErr = ""
		return
	}

	j.checkTime++
	// 连续两次检不到才判定退出：第一次可能只是恰好落在重启的时间窗里
	if j.checkTime < 2 {
		return
	}

	exitErr := j.xrayService.GetXrayErr()

	// 这里只置位、不直接重启：置位后 InboundController 每 10 秒会重试
	// RestartXray(false)，本 job 每 30 秒才跑一次，直接重启既冗余又会
	// 改变原有的重启节奏。
	j.xrayService.SetToNeedRestart()

	// 去重键只取退出原因：周期性重试失败的原因通常不变，
	// 不该因此反复推送；退出原因变了才说明是新的故障形态。
	alertKey := "down"
	if exitErr != nil {
		alertKey += "|exit:" + exitErr.Error()
	}
	if alertKey == j.lastAlertedErr {
		return
	}
	if !j.alerted {
		j.alerted = true
		j.downSince = time.Now()
	}
	j.lastAlertedErr = alertKey

	new(PushPlusNotifyJob).XrayDownNotify(j.xrayService.GetXrayVersion(), exitErr)
}
