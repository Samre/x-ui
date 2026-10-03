package job

import (
	"time"

	"x-ui/logger"
	"x-ui/web/service"
)

type CheckInboundJob struct {
	xrayService    service.XrayService
	inboundService service.InboundService
}

func NewCheckInboundJob() *CheckInboundJob {
	return new(CheckInboundJob)
}

func (j *CheckInboundJob) Run() {
	// 先取出本次将被停用的入站：停用之后就查不到它们了，
	// 而告警需要说清是哪个节点因何被停用。
	alerts := j.findAlerts()

	count, err := j.inboundService.DisableInvalidInbounds()
	if err != nil {
		logger.Warning("disable invalid inbounds err:", err)
		return
	}
	if count <= 0 {
		return
	}

	logger.Debugf("disabled %v inbounds", count)
	j.xrayService.SetToNeedRestart()
	new(PushPlusNotifyJob).InboundDisabledNotify(alerts)
}

// findAlerts 查询当前启用、且已触发停用条件的入站，用于生成告警内容。
// 它只读不写，真正的停用由 DisableInvalidInbounds 完成。
func (j *CheckInboundJob) findAlerts() []InboundAlert {
	inbounds, err := j.inboundService.FindInvalidInbounds()
	if err != nil {
		logger.Warning("find invalid inbounds err:", err)
		return nil
	}
	alerts := make([]InboundAlert, 0, len(inbounds))
	for _, inbound := range inbounds {
		alerts = append(alerts, InboundAlert{
			Tag:         inbound.Tag,
			Remark:      inbound.Remark,
			Port:        inbound.Port,
			Up:          inbound.Up,
			Down:        inbound.Down,
			Total:       inbound.Total,
			ExpiryTime:  inbound.ExpiryTime,
			QuotaExceed: inbound.Total > 0 && inbound.Up+inbound.Down >= inbound.Total,
			Expired:     inbound.ExpiryTime > 0 && inbound.ExpiryTime <= time.Now().Unix()*1000,
		})
	}
	return alerts
}
